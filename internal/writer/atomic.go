package writer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrPostReplaceVerification обозначает сбой чтения или SHA-256 после rename.
// В этой точке target уже заменён, поэтому результат всегда имеет Replaced=true.
var ErrPostReplaceVerification = errors.New("проверка target после replace не пройдена")

// atomicFile содержит операции temp-файла, необходимые для fault injection.
type atomicFile interface {
	io.Writer
	Sync() error
	Close() error
	Fd() uintptr
	Name() string
}

// syncCloser описывает descriptor родительского каталога.
type syncCloser interface {
	Sync() error
	Close() error
}

// ReplaceResult сообщает фактический исход rename, а не только наличие ошибки.
type ReplaceResult struct {
	// Path является canonical target path, использованным для replace.
	Path string
	// SHA256 содержит ожидаемый SHA-256 candidate.
	SHA256 [sha256.Size]byte
	// Replaced равен true после успешного rename, включая последующие ошибки.
	Replaced bool
}

// replaceOperations является immutable per-call seam: тесты копируют полностью
// заполненный production config и заменяют отдельные function fields.
type replaceOperations struct {
	openTarget    func(string) (*os.File, error)
	openDirectory func(string) (syncCloser, error)
	createTemp    func(string, string) (atomicFile, error)
	fstat         func(int, *unix.Stat_t) error
	fchown        func(int, int, int) error
	fchmod        func(int, uint32) error
	flistxattr    func(int, []byte) (int, error)
	fgetxattr     func(int, string, []byte) (int, error)
	fsetxattr     func(int, string, []byte, int) error
	rename        func(string, string) error
	remove        func(string) error
	postRead      func(string) (Snapshot, error)
}

// productionReplaceOperations заполняет каждый seam прямой os/unix операцией.
func productionReplaceOperations() replaceOperations {
	return replaceOperations{
		openTarget: openTargetFile,
		openDirectory: func(path string) (syncCloser, error) {
			return os.Open(path)
		},
		createTemp: func(directory, pattern string) (atomicFile, error) {
			return os.CreateTemp(directory, pattern)
		},
		fstat:      unix.Fstat,
		fchown:     unix.Fchown,
		fchmod:     unix.Fchmod,
		flistxattr: unix.Flistxattr,
		fgetxattr:  unix.Fgetxattr,
		fsetxattr:  unix.Fsetxattr,
		rename:     os.Rename,
		remove:     os.Remove,
		postRead:   ReadSnapshot,
	}
}

// validateReplaceOperations отклоняет incomplete config до открытия target или
// создания temp file, превращая программную ошибку в обычную ошибку вызова.
func validateReplaceOperations(operations replaceOperations) error {
	fields := []struct {
		name    string
		missing bool
	}{
		{name: "openTarget", missing: operations.openTarget == nil},
		{name: "openDirectory", missing: operations.openDirectory == nil},
		{name: "createTemp", missing: operations.createTemp == nil},
		{name: "fstat", missing: operations.fstat == nil},
		{name: "fchown", missing: operations.fchown == nil},
		{name: "fchmod", missing: operations.fchmod == nil},
		{name: "flistxattr", missing: operations.flistxattr == nil},
		{name: "fgetxattr", missing: operations.fgetxattr == nil},
		{name: "fsetxattr", missing: operations.fsetxattr == nil},
		{name: "rename", missing: operations.rename == nil},
		{name: "remove", missing: operations.remove == nil},
		{name: "postRead", missing: operations.postRead == nil},
	}
	for _, field := range fields {
		if field.missing {
			return fmt.Errorf("replace operations: nil field %s", field.name)
		}
	}
	return nil
}

// Replace атомарно заменяет существующий regular target содержимым candidate.
// Lock, backup и rollback сознательно не вызываются: их lifetime принадлежит
// будущей transaction, которая обязана учитывать Replaced при любой ошибке.
func Replace(path string, candidate []byte) (ReplaceResult, error) {
	return replaceWithOperations(path, candidate, productionReplaceOperations())
}

// replaceWithOperations реализует один вызов без mutable global state. До rename
// deferred cleanup удаляет temp; после rename writer никогда сам не откатывает target.
func replaceWithOperations(path string, candidate []byte, operations replaceOperations) (result ReplaceResult, err error) {
	if err := validateReplaceOperations(operations); err != nil {
		return ReplaceResult{}, err
	}
	canonical, err := CanonicalTargetPath(path)
	if err != nil {
		return ReplaceResult{}, err
	}
	expectedHash := sha256.Sum256(candidate)
	result = ReplaceResult{Path: canonical, SHA256: expectedHash}

	source, err := operations.openTarget(canonical)
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("закрыть source target %s: %w", canonical, closeErr))
		}
	}()
	var sourceStat unix.Stat_t
	if err := operations.fstat(int(source.Fd()), &sourceStat); err != nil {
		return result, fmt.Errorf("прочитать metadata source target: %w", err)
	}
	if sourceStat.Mode&unix.S_IFMT != unix.S_IFREG {
		return result, fmt.Errorf("%w: descriptor source target не является regular file", ErrInvalidTarget)
	}

	parent := filepath.Dir(canonical)
	directory, err := operations.openDirectory(parent)
	if err != nil {
		return result, fmt.Errorf("открыть parent directory %s: %w", parent, err)
	}
	defer func() {
		if closeErr := directory.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("закрыть parent directory %s: %w", parent, closeErr))
		}
	}()

	temp, err := operations.createTemp(parent, ".fstab-edit-*")
	if err != nil {
		return result, fmt.Errorf("создать temp file в %s: %w", parent, err)
	}
	tempPath := temp.Name()
	tempOpen := true
	removeTemp := true
	defer func() {
		if !removeTemp {
			return
		}
		var cleanupErr error
		if tempOpen {
			if closeErr := temp.Close(); closeErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("закрыть temp file %s: %w", tempPath, closeErr))
			}
			tempOpen = false
		}
		if removeErr := operations.remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("удалить temp file %s: %w", tempPath, removeErr))
		}
		// Primary error остаётся доступен через errors.Is даже при failed cleanup.
		err = errors.Join(err, cleanupErr)
	}()

	var tempStat unix.Stat_t
	if err := operations.fstat(int(temp.Fd()), &tempStat); err != nil {
		return result, fmt.Errorf("прочитать metadata temp file: %w", err)
	}
	if tempStat.Mode&unix.S_IFMT != unix.S_IFREG || tempStat.Mode&0o7777 != 0o600 {
		return result, fmt.Errorf("temp file должен быть regular file 0600, получено mode %04o", tempStat.Mode&0o7777)
	}

	written, err := io.Copy(temp, bytes.NewReader(candidate))
	if err != nil {
		return result, fmt.Errorf("записать candidate в temp file: %w", err)
	}
	if written != int64(len(candidate)) {
		return result, fmt.Errorf("записать candidate в temp file: %w: %d из %d bytes", io.ErrShortWrite, written, len(candidate))
	}
	if err := copyMetadata(int(source.Fd()), int(temp.Fd()), &sourceStat, operations); err != nil {
		return result, err
	}
	if err := temp.Sync(); err != nil {
		return result, fmt.Errorf("синхронизировать temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		tempOpen = false
		return result, fmt.Errorf("закрыть temp file перед rename: %w", err)
	}
	tempOpen = false

	if err := operations.rename(tempPath, canonical); err != nil {
		return result, fmt.Errorf("атомарно заменить target %s: %w", canonical, err)
	}
	result.Replaced = true
	removeTemp = false

	if err := directory.Sync(); err != nil {
		return result, fmt.Errorf("синхронизировать parent directory %s: %w", parent, err)
	}
	current, err := operations.postRead(canonical)
	if err != nil {
		// Два %w сохраняют и класс post-rename сбоя, и фактическую причину чтения.
		return result, fmt.Errorf("%w: повторно прочитать target %s: %w", ErrPostReplaceVerification, canonical, err)
	}
	if current.SHA256 != expectedHash {
		return result, fmt.Errorf("%w: SHA-256 target %s: получено %x, ожидалось %x", ErrPostReplaceVerification, canonical, current.SHA256, expectedHash)
	}
	return result, nil
}
