// Пакет writer содержит примитивы безопасной работы с целевым файлом.
package writer

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var (
	// ErrInvalidTarget обозначает путь, который нельзя безопасно прочитать как regular file.
	ErrInvalidTarget = errors.New("недопустимый target")
	// ErrBaseHashConflict обозначает внешнее изменение target после исходного snapshot.
	ErrBaseHashConflict = errors.New("target изменился после исходного snapshot")
)

// Snapshot сохраняет канонический путь, точные байты target и их SHA-256.
type Snapshot struct {
	// Path является абсолютным очищенным путём без symlink в компонентах.
	Path string
	// Data содержит новый срез с неизменёнными байтами прочитанного файла.
	Data []byte
	// SHA256 позволяет обнаружить внешнее изменение перед последующей записью.
	SHA256 [sha256.Size]byte
}

// CanonicalTargetPath возвращает абсолютный путь к существующему regular file.
// Symlink разрешаются только в родительских компонентах, а не для самого target.
func CanonicalTargetPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: пустой путь", ErrInvalidTarget)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: получить абсолютный путь: %w", ErrInvalidTarget, err)
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("%w: прочитать metadata %s: %w", ErrInvalidTarget, absolutePath, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: target не является regular file: %s", ErrInvalidTarget, absolutePath)
	}

	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", fmt.Errorf("%w: разрешить parent symlinks %s: %w", ErrInvalidTarget, absolutePath, err)
	}
	// Повторный Lstat исключает подмену типа файла при разрешении пути.
	info, err = os.Lstat(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("%w: прочитать resolved metadata %s: %w", ErrInvalidTarget, resolvedPath, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: resolved target не является regular file: %s", ErrInvalidTarget, resolvedPath)
	}
	return filepath.Clean(resolvedPath), nil
}

// openTargetFile открывает только regular file без следования финальному symlink.
// O_NONBLOCK предотвращает ожидание при ошибочной подмене target на FIFO.
func openTargetFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: открыть target %s: %w", ErrInvalidTarget, path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		// Ошибка Close вторична и не должна заменять первичную причину отказа open.
		_ = file.Close()
		return nil, fmt.Errorf("прочитать metadata target: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		// Ошибка Close вторична и не должна заменять первичную причину отказа open.
		_ = file.Close()
		return nil, fmt.Errorf("%w: target не является regular file", ErrInvalidTarget)
	}
	return file, nil
}

// ReadSnapshot читает target через descriptor, проверенный после безопасного open.
// Ошибка Close присоединяется к первичной ошибке либо возвращается после успешного чтения.
func ReadSnapshot(path string) (snapshot Snapshot, err error) {
	canonicalPath, err := CanonicalTargetPath(path)
	if err != nil {
		return Snapshot{}, err
	}
	file, err := openTargetFile(canonicalPath)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("закрыть target %s: %w", canonicalPath, closeErr))
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		return Snapshot{}, fmt.Errorf("прочитать target %s: %w", canonicalPath, err)
	}
	return Snapshot{
		Path:   canonicalPath,
		Data:   data,
		SHA256: sha256.Sum256(data),
	}, nil
}

// VerifyBaseHash повторно читает target и сравнивает его SHA-256 с исходным.
// Snapshot при конфликте предназначен для отображения или повторной загрузки, не для записи.
func VerifyBaseHash(path string, expected [sha256.Size]byte) (Snapshot, error) {
	current, err := ReadSnapshot(path)
	if err != nil {
		return Snapshot{}, err
	}
	if current.SHA256 != expected {
		return current, fmt.Errorf("%w: %s", ErrBaseHashConflict, current.Path)
	}
	return current, nil
}
