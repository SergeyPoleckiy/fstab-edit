package writer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// operationFile делегирует реальному temp-файлу и позволяет одному тесту
// заменить только нужную операцию без process-wide fault injection.
type operationFile struct {
	file       atomicFile
	write      func([]byte) (int, error)
	sync       func() error
	close      func() error
	afterWrite func()
}

func (file *operationFile) Write(data []byte) (int, error) {
	if file.write != nil {
		return file.write(data)
	}
	written, err := file.file.Write(data)
	if file.afterWrite != nil {
		file.afterWrite()
	}
	return written, err
}

func (file *operationFile) Sync() error {
	if file.sync != nil {
		return file.sync()
	}
	return file.file.Sync()
}

func (file *operationFile) Close() error {
	if file.close != nil {
		return file.close()
	}
	return file.file.Close()
}

func (file *operationFile) Fd() uintptr {
	return file.file.Fd()
}

func (file *operationFile) Name() string {
	return file.file.Name()
}

// operationSyncCloser аналогично оборачивает descriptor родительского каталога.
type operationSyncCloser struct {
	file  syncCloser
	sync  func() error
	close func() error
}

func (file *operationSyncCloser) Sync() error {
	if file.sync != nil {
		return file.sync()
	}
	return file.file.Sync()
}

func (file *operationSyncCloser) Close() error {
	if file.close != nil {
		return file.close()
	}
	return file.file.Close()
}

// writeReplaceTarget создаёт regular file только внутри изолированного TempDir.
func writeReplaceTarget(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// setTestXattr различает неподдерживаемую temp FS и настоящую ошибку теста.
func setTestXattr(t *testing.T, path, name string, value []byte) {
	t.Helper()
	if err := unix.Setxattr(path, name, value, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			t.Skipf("temp FS %s не поддерживает user xattrs: %v", filepath.Dir(path), err)
		}
		t.Fatal(err)
	}
}

// assertNoReplaceTempFiles запрещает leftovers независимо от случайного suffix.
func assertNoReplaceTempFiles(t *testing.T, target string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".fstab-edit-") {
			t.Fatalf("остался temp file %s", filepath.Join(filepath.Dir(target), entry.Name()))
		}
	}
}

// assertReplaceResult проверяет единый публичный контракт результата.
func assertReplaceResult(t *testing.T, result ReplaceResult, path string, candidate []byte) {
	t.Helper()
	canonical, err := CanonicalTargetPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != canonical {
		t.Fatalf("неожиданный canonical path: получено %q, ожидалось %q", result.Path, canonical)
	}
	if !result.Replaced {
		t.Fatal("успешный Replace обязан вернуть Replaced=true")
	}
	if result.SHA256 != sha256.Sum256(candidate) {
		t.Fatalf("неожиданный SHA-256: %x", result.SHA256)
	}
}

// TestReplacePreservesBytesModeAndUserXattr проверяет бинарную запись и копию
// ownership, permissions и user xattr через итоговый regular file.
func TestReplacePreservesBytesModeAndUserXattr(t *testing.T) {
	oldData := []byte("old\x00\n")
	path := writeReplaceTarget(t, oldData, 0o640)
	var before unix.Stat_t
	if err := unix.Stat(path, &before); err != nil {
		t.Fatal(err)
	}
	setTestXattr(t, path, "user.fstab_edit_test", []byte("context-v1"))

	candidate := []byte("new\xff\n")
	originalCandidate := bytes.Clone(candidate)
	result, err := Replace(path, candidate)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	assertReplaceResult(t, result, path, candidate)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, candidate) {
		t.Fatalf("target bytes: получено %v, ожидалось %v", got, candidate)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("target metadata не содержит syscall.Stat_t")
	}
	if stat.Mode&0o7777 != 0o640 {
		t.Fatalf("mode: получено %04o, ожидалось 0640", stat.Mode&0o7777)
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Uid != before.Uid || stat.Gid != before.Gid {
		t.Fatalf("ownership: получено %d:%d, исходное %d:%d, euid %d", stat.Uid, stat.Gid, before.Uid, before.Gid, os.Geteuid())
	}
	value := make([]byte, len("context-v1"))
	read, err := unix.Getxattr(path, "user.fstab_edit_test", value)
	if err != nil {
		t.Fatalf("прочитать сохранённый user xattr: %v", err)
	}
	if string(value[:read]) != "context-v1" {
		t.Fatalf("xattr: получено %q", value[:read])
	}
	assertNoReplaceTempFiles(t, path)
	if !bytes.Equal(candidate, originalCandidate) {
		t.Fatalf("исходный candidate изменён: получено %v, ожидалось %v", candidate, originalCandidate)
	}
}

// TestReplaceWithoutXattrsAndWithZeroLengthXattr отдельно покрывает пустой list
// и сохранение существующего атрибута с нулевой длиной значения.
func TestReplaceWithoutXattrsAndWithZeroLengthXattr(t *testing.T) {
	t.Run("без xattrs", func(t *testing.T) {
		path := writeReplaceTarget(t, []byte("old"), 0o600)
		candidate := []byte("new")
		result, err := Replace(path, candidate)
		if err != nil {
			t.Fatal(err)
		}
		assertReplaceResult(t, result, path, candidate)
		assertNoReplaceTempFiles(t, path)
	})

	t.Run("zero-length xattr", func(t *testing.T) {
		path := writeReplaceTarget(t, []byte("old"), 0o600)
		const name = "user.fstab_edit_empty"
		setTestXattr(t, path, name, []byte{})
		result, err := Replace(path, []byte("new"))
		if err != nil {
			t.Fatal(err)
		}
		assertReplaceResult(t, result, path, []byte("new"))
		size, err := unix.Getxattr(path, name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if size != 0 {
			t.Fatalf("zero-length xattr изменился: size=%d", size)
		}
		assertNoReplaceTempFiles(t, path)
	})
}

// TestReplaceEventOrder фиксирует durability barrier: rename допустим только
// после записи candidate, metadata, fsync и закрытия temp descriptor.
func TestReplaceEventOrder(t *testing.T) {
	path := writeReplaceTarget(t, []byte("old"), 0o640)
	setTestXattr(t, path, "user.fstab_edit_order", []byte("order"))
	operations := productionReplaceOperations()
	events := make([]string, 0, 9)

	operations.fchown = func(fd int, uid, gid int) error {
		events = append(events, "fchown")
		return unix.Fchown(fd, uid, gid)
	}
	operations.fchmod = func(fd int, mode uint32) error {
		events = append(events, "fchmod")
		return unix.Fchmod(fd, mode)
	}
	operations.fsetxattr = func(fd int, name string, value []byte, flags int) error {
		events = append(events, "fsetxattr")
		return unix.Fsetxattr(fd, name, value, flags)
	}
	createTemp := operations.createTemp
	operations.createTemp = func(directory, pattern string) (atomicFile, error) {
		file, err := createTemp(directory, pattern)
		if err != nil {
			return nil, err
		}
		return &operationFile{
			file:       file,
			afterWrite: func() { events = append(events, "write") },
			sync: func() error {
				events = append(events, "file sync")
				return file.Sync()
			},
			close: func() error {
				events = append(events, "file close")
				return file.Close()
			},
		}, nil
	}
	operations.rename = func(oldPath, newPath string) error {
		events = append(events, "rename")
		return os.Rename(oldPath, newPath)
	}
	openDirectory := operations.openDirectory
	operations.openDirectory = func(path string) (syncCloser, error) {
		directory, err := openDirectory(path)
		if err != nil {
			return nil, err
		}
		return &operationSyncCloser{file: directory, sync: func() error {
			events = append(events, "directory sync")
			return directory.Sync()
		}}, nil
	}
	operations.postRead = func(path string) (Snapshot, error) {
		events = append(events, "post read")
		return ReadSnapshot(path)
	}

	result, err := replaceWithOperations(path, []byte("new"), operations)
	if err != nil {
		t.Fatal(err)
	}
	assertReplaceResult(t, result, path, []byte("new"))
	expected := []string{
		"write", "fchown", "fchmod", "fsetxattr", "file sync",
		"file close", "rename", "directory sync", "post read",
	}
	if strings.Join(events, " → ") != strings.Join(expected, " → ") {
		t.Fatalf("порядок операций: получено %v, ожидалось %v", events, expected)
	}
	assertNoReplaceTempFiles(t, path)
}

// wrapFaultTemp сохраняет реальный descriptor и изменяет только методы,
// выбранные конкретным fault-сценарием.
func wrapFaultTemp(operations *replaceOperations, configure func(*operationFile)) {
	createTemp := operations.createTemp
	operations.createTemp = func(directory, pattern string) (atomicFile, error) {
		file, err := createTemp(directory, pattern)
		if err != nil {
			return nil, err
		}
		wrapped := &operationFile{file: file}
		configure(wrapped)
		return wrapped, nil
	}
}

// wrapFaultDirectory аналогично сохраняет реальный directory descriptor.
func wrapFaultDirectory(operations *replaceOperations, configure func(*operationSyncCloser)) {
	openDirectory := operations.openDirectory
	operations.openDirectory = func(path string) (syncCloser, error) {
		directory, err := openDirectory(path)
		if err != nil {
			return nil, err
		}
		wrapped := &operationSyncCloser{file: directory}
		configure(wrapped)
		return wrapped, nil
	}
}

// staticXattrList моделирует NUL-separated list без зависимости fault-теста от
// поддержки xattrs конкретной temp FS.
func staticXattrList(name string) func(int, []byte) (int, error) {
	encoded := append([]byte(name), 0)
	return func(_ int, destination []byte) (int, error) {
		if len(destination) == 0 {
			return len(encoded), nil
		}
		if len(destination) < len(encoded) {
			return 0, unix.ERANGE
		}
		return copy(destination, encoded), nil
	}
}

// staticXattrValue моделирует size/read пару Fgetxattr, включая zero-length.
func staticXattrValue(value []byte) func(int, string, []byte) (int, error) {
	return func(_ int, _ string, destination []byte) (int, error) {
		if len(destination) == 0 {
			return len(value), nil
		}
		if len(destination) < len(value) {
			return 0, unix.ERANGE
		}
		return copy(destination, value), nil
	}
}

// replaceFaultState фиксирует достижение rename и cleanup для каждого сбоя.
type replaceFaultState struct {
	renameCalled bool
	removeCalled bool
}

// TestReplaceFaults проверяет target bytes и Replaced на обеих сторонах rename,
// сохранение primary error и отсутствие самостоятельного rollback writer.
func TestReplaceFaults(t *testing.T) {
	oldData := []byte("old\x00\n")
	candidate := []byte("new\xff\n")
	tests := []struct {
		name             string
		cause            error
		configure        func(*replaceOperations, *replaceFaultState, error)
		wantReplaced     bool
		wantRename       bool
		wantRemove       bool
		wantCandidate    bool
		wantVerification bool
		cleanupCause     error
	}{
		{
			name:  "CreateTemp error",
			cause: unix.EACCES,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				operations.createTemp = func(string, string) (atomicFile, error) { return nil, cause }
			},
		},
		{
			name:  "write ENOSPC",
			cause: unix.ENOSPC,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				wrapFaultTemp(operations, func(file *operationFile) {
					file.write = func([]byte) (int, error) { return 0, cause }
				})
			},
			wantRemove: true,
		},
		{
			name:  "fchown EPERM",
			cause: unix.EPERM,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				operations.fchown = func(int, int, int) error { return cause }
			},
			wantRemove: true,
		},
		{
			name:  "fchmod EACCES",
			cause: unix.EACCES,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				operations.fchmod = func(int, uint32) error { return cause }
			},
			wantRemove: true,
		},
		{
			name:  "flistxattr EPERM",
			cause: unix.EPERM,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				operations.flistxattr = func(int, []byte) (int, error) { return 0, cause }
			},
			wantRemove: true,
		},
		{
			name:  "fgetxattr EACCES",
			cause: unix.EACCES,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				operations.flistxattr = staticXattrList("user.fstab_edit_fault")
				operations.fgetxattr = func(int, string, []byte) (int, error) { return 0, cause }
			},
			wantRemove: true,
		},
		{
			name:  "fsetxattr security context EPERM",
			cause: unix.EPERM,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				operations.flistxattr = staticXattrList("security.selinux")
				operations.fgetxattr = staticXattrValue([]byte("не выводить в ошибку"))
				operations.fsetxattr = func(int, string, []byte, int) error { return cause }
			},
			wantRemove: true,
		},
		{
			name:  "file sync error",
			cause: unix.EIO,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				wrapFaultTemp(operations, func(file *operationFile) { file.sync = func() error { return cause } })
			},
			wantRemove: true,
		},
		{
			name:  "file close error",
			cause: unix.EIO,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				wrapFaultTemp(operations, func(file *operationFile) {
					file.close = func() error {
						closeErr := file.file.Close()
						return errors.Join(closeErr, cause)
					}
				})
			},
			wantRemove: true,
		},
		{
			name:  "rename EROFS",
			cause: unix.EROFS,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				operations.rename = func(string, string) error { return cause }
			},
			wantRename: true,
			wantRemove: true,
		},
		{
			name:  "directory sync error",
			cause: unix.EIO,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				wrapFaultDirectory(operations, func(directory *operationSyncCloser) {
					directory.sync = func() error { return cause }
				})
			},
			wantReplaced:  true,
			wantRename:    true,
			wantCandidate: true,
		},
		{
			name:  "post-read error",
			cause: unix.EIO,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				operations.postRead = func(string) (Snapshot, error) { return Snapshot{}, cause }
			},
			wantReplaced:     true,
			wantRename:       true,
			wantCandidate:    true,
			wantVerification: true,
		},
		{
			name:  "wrong-hash snapshot",
			cause: ErrPostReplaceVerification,
			configure: func(operations *replaceOperations, _ *replaceFaultState, _ error) {
				operations.postRead = func(path string) (Snapshot, error) {
					snapshot, err := ReadSnapshot(path)
					snapshot.SHA256 = [sha256.Size]byte{}
					return snapshot, err
				}
			},
			wantReplaced:     true,
			wantRename:       true,
			wantCandidate:    true,
			wantVerification: true,
		},
		{
			name:         "write failure и cleanup failure",
			cause:        unix.ENOSPC,
			cleanupCause: unix.EACCES,
			configure: func(operations *replaceOperations, _ *replaceFaultState, cause error) {
				wrapFaultTemp(operations, func(file *operationFile) {
					file.write = func([]byte) (int, error) { return 0, cause }
				})
			},
			wantRemove: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeReplaceTarget(t, oldData, 0o640)
			operations := productionReplaceOperations()
			state := &replaceFaultState{}
			test.configure(&operations, state, test.cause)
			if test.cleanupCause != nil {
				operations.remove = func(string) error { return test.cleanupCause }
			}
			rename := operations.rename
			operations.rename = func(oldPath, newPath string) error {
				state.renameCalled = true
				return rename(oldPath, newPath)
			}
			remove := operations.remove
			operations.remove = func(path string) error {
				state.removeCalled = true
				return remove(path)
			}

			originalCandidate := bytes.Clone(candidate)
			result, err := replaceWithOperations(path, candidate, operations)
			if !errors.Is(err, test.cause) {
				t.Fatalf("primary error потерян: ожидалось %v, получено %v", test.cause, err)
			}
			if test.cleanupCause != nil && !errors.Is(err, test.cleanupCause) {
				t.Fatalf("cleanup error потерян: ожидалось %v, получено %v", test.cleanupCause, err)
			}
			if test.wantVerification && !errors.Is(err, ErrPostReplaceVerification) {
				t.Fatalf("ожидалась ErrPostReplaceVerification, получено %v", err)
			}
			if result.Replaced != test.wantReplaced {
				t.Fatalf("Replaced: получено %v, ожидалось %v", result.Replaced, test.wantReplaced)
			}
			if state.renameCalled != test.wantRename {
				t.Fatalf("rename called: получено %v, ожидалось %v", state.renameCalled, test.wantRename)
			}
			if state.removeCalled != test.wantRemove {
				t.Fatalf("remove called: получено %v, ожидалось %v", state.removeCalled, test.wantRemove)
			}
			expectedTarget := oldData
			if test.wantCandidate {
				expectedTarget = candidate
			}
			actualTarget, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(actualTarget, expectedTarget) {
				t.Fatalf("target после fault: bytes=%v err=%v, ожидалось %v", actualTarget, readErr, expectedTarget)
			}
			if test.cleanupCause == nil {
				assertNoReplaceTempFiles(t, path)
			}
			if !bytes.Equal(candidate, originalCandidate) {
				t.Fatal("fault path изменил исходный candidate")
			}
			if test.name == "fsetxattr security context EPERM" && strings.Contains(err.Error(), "не выводить") {
				t.Fatal("значение security xattr попало в error message")
			}
		})
	}
}

// TestReplaceRejectsIncompleteOperations проверяет все function fields и отказ
// до mutation, а не panic на nil в середине atomic pipeline.
func TestReplaceRejectsIncompleteOperations(t *testing.T) {
	tests := []struct {
		name    string
		disable func(*replaceOperations)
	}{
		{name: "openTarget", disable: func(operations *replaceOperations) { operations.openTarget = nil }},
		{name: "openDirectory", disable: func(operations *replaceOperations) { operations.openDirectory = nil }},
		{name: "createTemp", disable: func(operations *replaceOperations) { operations.createTemp = nil }},
		{name: "fstat", disable: func(operations *replaceOperations) { operations.fstat = nil }},
		{name: "fchown", disable: func(operations *replaceOperations) { operations.fchown = nil }},
		{name: "fchmod", disable: func(operations *replaceOperations) { operations.fchmod = nil }},
		{name: "flistxattr", disable: func(operations *replaceOperations) { operations.flistxattr = nil }},
		{name: "fgetxattr", disable: func(operations *replaceOperations) { operations.fgetxattr = nil }},
		{name: "fsetxattr", disable: func(operations *replaceOperations) { operations.fsetxattr = nil }},
		{name: "rename", disable: func(operations *replaceOperations) { operations.rename = nil }},
		{name: "remove", disable: func(operations *replaceOperations) { operations.remove = nil }},
		{name: "postRead", disable: func(operations *replaceOperations) { operations.postRead = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeReplaceTarget(t, []byte("old"), 0o600)
			operations := productionReplaceOperations()
			test.disable(&operations)
			result, err := replaceWithOperations(path, []byte("new"), operations)
			if err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("incomplete config: result=%+v err=%v", result, err)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != "old" {
				t.Fatalf("incomplete config изменил target: %q, %v", data, readErr)
			}
			assertNoReplaceTempFiles(t, path)
		})
	}
}

// TestReplaceRejectsInvalidTargetsBeforeTemp проверяет symlink, directory и FIFO
// без риска блокировки на FIFO или создания temp рядом с небезопасным target.
func TestReplaceRejectsInvalidTargetsBeforeTemp(t *testing.T) {
	directory := t.TempDir()
	regular := filepath.Join(directory, "regular")
	if err := os.WriteFile(regular, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "symlink")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(directory, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{symlink, directory, fifo} {
		operations := productionReplaceOperations()
		operations.createTemp = func(string, string) (atomicFile, error) {
			t.Fatal("CreateTemp вызван для invalid target")
			return nil, nil
		}
		operations.rename = func(string, string) error {
			t.Fatal("rename вызван для invalid target")
			return nil
		}
		result, err := replaceWithOperations(path, []byte("new"), operations)
		if !errors.Is(err, ErrInvalidTarget) || result.Replaced {
			t.Fatalf("invalid target %s: result=%+v err=%v", path, result, err)
		}
	}
	data, err := os.ReadFile(regular)
	if err != nil || string(data) != "old" {
		t.Fatalf("destination symlink изменён: %q, %v", data, err)
	}
}

// TestReplaceRejectsUnsafeTempMode проверяет обязательный начальный mode 0600
// до записи candidate и до rename.
func TestReplaceRejectsUnsafeTempMode(t *testing.T) {
	path := writeReplaceTarget(t, []byte("old"), 0o600)
	operations := productionReplaceOperations()
	wrapFaultTemp(&operations, func(file *operationFile) {
		if err := unix.Fchmod(int(file.Fd()), 0o644); err != nil {
			t.Fatal(err)
		}
		file.write = func([]byte) (int, error) {
			t.Fatal("запись началась до проверки temp mode")
			return 0, nil
		}
	})
	result, err := replaceWithOperations(path, []byte("new"), operations)
	if err == nil || result.Replaced {
		t.Fatalf("небезопасный temp mode принят: result=%+v err=%v", result, err)
	}
	assertNoReplaceTempFiles(t, path)
}

// TestReplaceRetriesChangingXattrSizes проверяет повторную size/read пару для
// list и value после ERANGE, рост обоих размеров и выделение новых buffers.
func TestReplaceRetriesChangingXattrSizes(t *testing.T) {
	path := writeReplaceTarget(t, []byte("old"), 0o600)
	operations := productionReplaceOperations()
	initialEncoded := append([]byte("user.a"), 0)
	grownEncoded := append([]byte("user.fstab_edit_growing"), 0)
	listSizeCalls := 0
	listReads := 0
	listBufferSizes := make([]int, 0, 2)
	operations.flistxattr = func(_ int, destination []byte) (int, error) {
		if len(destination) == 0 {
			listSizeCalls++
			if listSizeCalls == 1 {
				return len(initialEncoded), nil
			}
			return len(grownEncoded), nil
		}
		listReads++
		listBufferSizes = append(listBufferSizes, len(destination))
		if listReads == 1 {
			return 0, unix.ERANGE
		}
		if len(destination) != len(grownEncoded) {
			t.Fatalf("list buffer не вырос: получено %d, ожидалось %d", len(destination), len(grownEncoded))
		}
		return copy(destination, grownEncoded), nil
	}
	initialValue := []byte("x")
	value := []byte("grown-value")
	valueSizeCalls := 0
	valueReads := 0
	valueBufferSizes := make([]int, 0, 2)
	operations.fgetxattr = func(_ int, _ string, destination []byte) (int, error) {
		if len(destination) == 0 {
			valueSizeCalls++
			if valueSizeCalls == 1 {
				return len(initialValue), nil
			}
			return len(value), nil
		}
		valueReads++
		valueBufferSizes = append(valueBufferSizes, len(destination))
		if valueReads == 1 {
			return 0, unix.ERANGE
		}
		if len(destination) != len(value) {
			t.Fatalf("value buffer не вырос: получено %d, ожидалось %d", len(destination), len(value))
		}
		return copy(destination, value), nil
	}
	var copiedName string
	var copiedValue []byte
	operations.fsetxattr = func(_ int, name string, value []byte, _ int) error {
		copiedName = name
		copiedValue = bytes.Clone(value)
		return nil
	}
	result, err := replaceWithOperations(path, []byte("new"), operations)
	if err != nil || !result.Replaced {
		t.Fatalf("retry replace: result=%+v err=%v", result, err)
	}
	if listReads != 2 || valueReads != 2 {
		t.Fatalf("неожиданное число read попыток: list=%d value=%d", listReads, valueReads)
	}
	if listSizeCalls != 2 || valueSizeCalls != 2 {
		t.Fatalf("неожиданное число size probes: list=%d value=%d", listSizeCalls, valueSizeCalls)
	}
	if len(listBufferSizes) != 2 || listBufferSizes[0] != len(initialEncoded) || listBufferSizes[1] != len(grownEncoded) {
		t.Fatalf("list buffers не перераспределены по выросшему size: %v", listBufferSizes)
	}
	if len(valueBufferSizes) != 2 || valueBufferSizes[0] != len(initialValue) || valueBufferSizes[1] != len(value) {
		t.Fatalf("value buffers не перераспределены по выросшему size: %v", valueBufferSizes)
	}
	if copiedName != "user.fstab_edit_growing" || !bytes.Equal(copiedValue, value) {
		t.Fatalf("скопирован неверный xattr: name=%q value=%q", copiedName, copiedValue)
	}
}

// TestReplaceRejectsUnsupportedReadOfNonemptyXattrList запрещает подавлять
// EOPNOTSUPP на read после probe, который уже подтвердил непустой list.
func TestReplaceRejectsUnsupportedReadOfNonemptyXattrList(t *testing.T) {
	path := writeReplaceTarget(t, []byte("old"), 0o600)
	operations := productionReplaceOperations()
	encoded := append([]byte("user.fstab_edit_confirmed"), 0)
	sizeCalls := 0
	readCalls := 0
	operations.flistxattr = func(_ int, destination []byte) (int, error) {
		if len(destination) == 0 {
			sizeCalls++
			return len(encoded), nil
		}
		readCalls++
		return 0, unix.EOPNOTSUPP
	}
	renameCalled := false
	operations.rename = func(oldPath, newPath string) error {
		renameCalled = true
		return os.Rename(oldPath, newPath)
	}

	result, err := replaceWithOperations(path, []byte("new"), operations)
	if !errors.Is(err, unix.EOPNOTSUPP) {
		t.Errorf("ожидалась исходная EOPNOTSUPP, получено %v", err)
	}
	if result.Replaced {
		t.Error("Replaced=true после ошибки чтения подтверждённого xattr list")
	}
	if renameCalled {
		t.Error("rename вызван после ошибки чтения подтверждённого xattr list")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old" {
		t.Errorf("target изменился: %q, %v", data, readErr)
	}
	if sizeCalls != 1 || readCalls != 1 {
		t.Errorf("неожиданные вызовы list: size=%d read=%d", sizeCalls, readCalls)
	}
	assertNoReplaceTempFiles(t, path)
}

// TestReplaceBoundsXattrRetries доказывает ровно четыре повторных read после
// первоначальной попытки и запрет rename при постоянном ERANGE.
func TestReplaceBoundsXattrRetries(t *testing.T) {
	for _, operation := range []string{"list", "get"} {
		t.Run(operation, func(t *testing.T) {
			path := writeReplaceTarget(t, []byte("old"), 0o600)
			operations := productionReplaceOperations()
			reads := 0
			operations.flistxattr = staticXattrList("user.fstab_edit_retry")
			if operation == "list" {
				operations.flistxattr = func(_ int, destination []byte) (int, error) {
					if len(destination) == 0 {
						return 1, nil
					}
					reads++
					return 0, unix.ERANGE
				}
			} else {
				operations.fgetxattr = func(_ int, _ string, destination []byte) (int, error) {
					if len(destination) == 0 {
						return 1, nil
					}
					reads++
					return 0, unix.ERANGE
				}
			}
			operations.rename = func(string, string) error {
				t.Fatal("rename вызван после исчерпания xattr retries")
				return nil
			}
			result, err := replaceWithOperations(path, []byte("new"), operations)
			if !errors.Is(err, unix.ERANGE) || result.Replaced {
				t.Fatalf("ERANGE: result=%+v err=%v", result, err)
			}
			if reads != xattrMaxRetries+1 {
				t.Fatalf("read attempts: получено %d, ожидалось %d", reads, xattrMaxRetries+1)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != "old" {
				t.Fatalf("target изменился: %q, %v", data, readErr)
			}
			assertNoReplaceTempFiles(t, path)
		})
	}
}

// TestReplaceUnsupportedXattrList проверяет единственный подавляемый xattr case:
// неподдерживаемый пустой list, но не ошибку существующего атрибута.
func TestReplaceUnsupportedXattrList(t *testing.T) {
	path := writeReplaceTarget(t, []byte("old"), 0o600)
	operations := productionReplaceOperations()
	operations.flistxattr = func(int, []byte) (int, error) { return 0, unix.ENOTSUP }
	result, err := replaceWithOperations(path, []byte("new"), operations)
	if err != nil {
		t.Fatal(err)
	}
	assertReplaceResult(t, result, path, []byte("new"))
	assertNoReplaceTempFiles(t, path)
}

// TestReplaceDirectoryCloseErrorKeepsReplaced проверяет, что даже ошибка close
// после post-read не может вернуть ложный Replaced=false или вызвать rollback.
func TestReplaceDirectoryCloseErrorKeepsReplaced(t *testing.T) {
	path := writeReplaceTarget(t, []byte("old"), 0o600)
	operations := productionReplaceOperations()
	cause := unix.EIO
	wrapFaultDirectory(&operations, func(directory *operationSyncCloser) {
		directory.close = func() error {
			return errors.Join(directory.file.Close(), cause)
		}
	})
	result, err := replaceWithOperations(path, []byte("new"), operations)
	if !errors.Is(err, cause) || !result.Replaced {
		t.Fatalf("directory close: result=%+v err=%v", result, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "new" {
		t.Fatalf("target после close error: %q, %v", data, readErr)
	}
	assertNoReplaceTempFiles(t, path)
}

// TestReplaceRejectsShortWrite отдельно проверяет writer, который принял только
// часть candidate без собственной ошибки: io.ErrShortWrite запрещает rename.
func TestReplaceRejectsShortWrite(t *testing.T) {
	path := writeReplaceTarget(t, []byte("old"), 0o600)
	operations := productionReplaceOperations()
	wrapFaultTemp(&operations, func(file *operationFile) {
		file.write = func(data []byte) (int, error) { return len(data) - 1, nil }
	})
	operations.rename = func(string, string) error {
		t.Fatal("rename вызван после short write")
		return nil
	}
	result, err := replaceWithOperations(path, []byte("new"), operations)
	if !errors.Is(err, io.ErrShortWrite) || result.Replaced {
		t.Fatalf("short write: result=%+v err=%v", result, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old" {
		t.Fatalf("target после short write: %q, %v", data, readErr)
	}
	assertNoReplaceTempFiles(t, path)
}

// TestReplaceRechecksSourceDescriptorType изолирует повторный Fstat после open:
// даже валидный путь не разрешает CreateTemp для non-regular descriptor.
func TestReplaceRechecksSourceDescriptorType(t *testing.T) {
	path := writeReplaceTarget(t, []byte("old"), 0o600)
	operations := productionReplaceOperations()
	fstat := operations.fstat
	sourceCheck := true
	operations.fstat = func(fd int, stat *unix.Stat_t) error {
		if err := fstat(fd, stat); err != nil {
			return err
		}
		if sourceCheck {
			sourceCheck = false
			stat.Mode = unix.S_IFIFO | 0o600
		}
		return nil
	}
	operations.createTemp = func(string, string) (atomicFile, error) {
		t.Fatal("CreateTemp вызван для non-regular source descriptor")
		return nil, nil
	}
	result, err := replaceWithOperations(path, []byte("new"), operations)
	if !errors.Is(err, ErrInvalidTarget) || result.Replaced {
		t.Fatalf("source descriptor type: result=%+v err=%v", result, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old" {
		t.Fatalf("target после source type fault: %q, %v", data, readErr)
	}
	assertNoReplaceTempFiles(t, path)
}
