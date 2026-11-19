package writer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestReadSnapshotReturnsCanonicalBinarySnapshot проверяет чтение бинарного
// содержимого по относительному пути без текстовых преобразований.
func TestReadSnapshotReturnsCanonicalBinarySnapshot(t *testing.T) {
	dir := t.TempDir()
	data := []byte{0xff, 0x00, 'f', 's', 't', 'a', 'b'}
	if err := os.WriteFile(filepath.Join(dir, "fstab"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Errorf("восстановить рабочий каталог: %v", err)
		}
	})

	snapshot, err := ReadSnapshot("fstab")
	if err != nil {
		t.Fatalf("прочитать snapshot: %v", err)
	}

	// Путь должен быть абсолютным и очищенным, а не исходной относительной строкой.
	if snapshot.Path != filepath.Join(dir, "fstab") {
		t.Fatalf("неожиданный canonical path: %q", snapshot.Path)
	}
	// Invalid UTF-8 и отсутствие final newline должны сохраниться байт в байт.
	if !bytes.Equal(snapshot.Data, data) {
		t.Fatalf("данные изменились: получено %v, ожидалось %v", snapshot.Data, data)
	}
	if snapshot.SHA256 != sha256.Sum256(data) {
		t.Fatalf("неожиданный SHA-256: %x", snapshot.SHA256)
	}
}

// TestReadSnapshotEmptyFile проверяет SHA-256 пустого содержимого.
func TestReadSnapshotEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := ReadSnapshot(path)
	if err != nil {
		t.Fatalf("прочитать пустой target: %v", err)
	}
	if len(snapshot.Data) != 0 {
		t.Fatalf("ожидались пустые данные, получено: %v", snapshot.Data)
	}
	if snapshot.SHA256 != sha256.Sum256(nil) {
		t.Fatalf("неожиданный SHA-256 пустого файла: %x", snapshot.SHA256)
	}
}

// TestReadSnapshotRejectsInvalidTargets проверяет отказ для всех небезопасных
// видов target до чтения содержимого.
func TestReadSnapshotRejectsInvalidTargets(t *testing.T) {
	dir := t.TempDir()
	regularPath := filepath.Join(dir, "regular")
	if err := os.WriteFile(regularPath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(dir, "symlink")
	if err := os.Symlink(regularPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	fifoPath := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "пустой путь", path: ""},
		{name: "отсутствующий файл", path: filepath.Join(dir, "missing")},
		{name: "symlink на regular file", path: symlinkPath},
		{name: "каталог", path: dir},
		{name: "FIFO", path: fifoPath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := ReadSnapshot(test.path)
			if !errors.Is(err, ErrInvalidTarget) {
				t.Fatalf("ожидалась ErrInvalidTarget, получено: %v", err)
			}
			// Ошибочный target не должен возвращать данные, в том числе destination symlink.
			if snapshot.Data != nil || snapshot.Path != "" {
				t.Fatalf("ошибочный target вернул snapshot: %+v", snapshot)
			}
		})
	}
}

// TestReadSnapshotOpenTargetFileRejectsSymlink доказывает enforcement O_NOFOLLOW
// непосредственно при открытии descriptor, без опоры на предварительный Lstat.
func TestReadSnapshotOpenTargetFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "destination")
	if err := os.WriteFile(destination, []byte("не читать"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "target")
	if err := os.Symlink(destination, path); err != nil {
		t.Fatal(err)
	}

	file, err := openTargetFile(path)
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("ожидалась ErrInvalidTarget для symlink, получено: %v", err)
	}
	if file != nil {
		file.Close()
		t.Fatal("для symlink не должен возвращаться открытый descriptor")
	}
}

// TestCanonicalTargetPathResolvesSymlinkedParent проверяет разрешение symlink
// только в родительских компонентах при сохранении запрета symlink самого target.
func TestCanonicalTargetPathResolvesSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(realDir, "fstab")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(dir, "linked")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}

	canonical, err := CanonicalTargetPath(filepath.Join(linkedDir, ".", "fstab"))
	if err != nil {
		t.Fatalf("разрешить parent symlink: %v", err)
	}
	if canonical != path {
		t.Fatalf("неожиданный canonical path: получено %q, ожидалось %q", canonical, path)
	}
}

// TestVerifyBaseHashDetectsExternalChange проверяет конфликт при внешнем
// изменении target после исходного snapshot.
func TestVerifyBaseHashDetectsExternalChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	// Изменение имеет ту же длину: проверяются bytes, а не size или mtime.
	if err := os.WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := VerifyBaseHash(path, before.SHA256)
	if !errors.Is(err, ErrBaseHashConflict) {
		t.Fatalf("ожидался конфликт base hash, получено: %v", err)
	}
	if string(current.Data) != "new\n" {
		t.Fatalf("current snapshot должен описывать фактическую версию: %q", current.Data)
	}
}

// TestVerifyBaseHashAcceptsUnchangedTarget проверяет успешную повторную проверку
// неизменённого target и возврат его актуального snapshot.
func TestVerifyBaseHashAcceptsUnchangedTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	data := []byte("unchanged\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}

	current, err := VerifyBaseHash(path, before.SHA256)
	if err != nil {
		t.Fatalf("неизменённый target не должен конфликтовать: %v", err)
	}
	if !bytes.Equal(current.Data, data) || current.SHA256 != before.SHA256 {
		t.Fatalf("неожиданный current snapshot: %+v", current)
	}
}
