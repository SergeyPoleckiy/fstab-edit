// Пакет backup реализует создание резервных копий fstab с sidecar-метаданными.
//
// Этот тестовый файл покрывает Task 1: exact filenames/JSON, metadata schema,
// collision naming и create lifecycle с fault injection. Тесты используют только
// t.TempDir и никогда не трогают реальный /etc/fstab; root не требуется.
package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"fstab-edit/internal/writer"
	"golang.org/x/sys/unix"
)

// fixedNow — детерминированное UTC-время, которое делает backup ID и имена
// файлов предсказуемыми во всех тестах создания.
var fixedNow = time.Date(2026, 11, 18, 15, 30, 45, 123456789, time.UTC)

// expectedFixedID — backup ID, обязанный получиться из fixedNow по контракту.
const expectedFixedID = "2026-11-18_15-30-45.123456789Z"

// newTargetSnapshot создаёт temp target с заданным содержимым и возвращает его
// snapshot через writer.ReadSnapshot, то есть ровно тот вход, который получает Create.
// Mode 0640 выбран отличным от backup mode 0600, чтобы metadata mode была осмысленной.
func newTargetSnapshot(t *testing.T, content []byte) writer.Snapshot {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, content, 0o640); err != nil {
		t.Fatalf("создать temp target: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("установить mode temp target: %v", err)
	}
	snapshot, err := writer.ReadSnapshot(path)
	if err != nil {
		t.Fatalf("прочитать snapshot temp target: %v", err)
	}
	return snapshot
}

// metadataFileMode возвращает permission bits файла metadata для сравнения.
func metadataFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("прочитать metadata %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s не является regular file: %v", path, info.Mode())
	}
	return info.Mode().Perm()
}

// TestCreateSuccess проверяет полный happy path: exact ID, имена файлов, mode 0600,
// побайтовое равенство data, все поля metadata, strict JSON, неизменность target
// и совпадение хеша при повторном чтении backup.
func TestCreateSuccess(t *testing.T) {
	source := newTargetSnapshot(t, []byte("# test fstab\nUUID=abc / ext4 defaults 0 1\n"))

	backup, err := Create(source, CreateOptions{
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Backup ID обязан быть ядром имени, а не суффиксом коллизии.
	if backup.ID != expectedFixedID {
		t.Errorf("backup.ID: получено %q, ожидалось %q", backup.ID, expectedFixedID)
	}

	// Exact filenames: data рядом с target, sidecar — data + ".json".
	targetDir := filepath.Dir(source.Path)
	expectedDataPath := filepath.Join(targetDir, "fstab.bak."+expectedFixedID)
	expectedSidecarPath := expectedDataPath + ".json"
	if backup.Path != expectedDataPath {
		t.Errorf("backup.Path: получено %q, ожидалось %q", backup.Path, expectedDataPath)
	}
	if backup.MetadataPath != expectedSidecarPath {
		t.Errorf("backup.MetadataPath: получено %q, ожидалось %q", backup.MetadataPath, expectedSidecarPath)
	}
	if backup.Path+".json" != backup.MetadataPath {
		t.Errorf("data и sidecar не образуют пару: %q + .json != %q", backup.Path, backup.MetadataPath)
	}

	// Оба файла — regular 0600: backup непривилегированный и не читается посторонними.
	for _, path := range []string{backup.Path, backup.MetadataPath} {
		if mode := metadataFileMode(t, path); mode != 0o600 {
			t.Errorf("mode %s: получено %04o, ожидалось 0600", path, mode)
		}
	}

	// Data побайтово равна исходному snapshot, а её SHA-256 совпадает с хешем backup.
	data, err := os.ReadFile(backup.Path)
	if err != nil {
		t.Fatalf("прочитать data backup: %v", err)
	}
	if !bytes.Equal(data, source.Data) {
		t.Errorf("data backup не равна source.Data: получено %q, ожидалось %q", data, source.Data)
	}
	if backup.SHA256 != source.SHA256 {
		t.Errorf("SHA256 backup: получено %x, ожидалось %x", backup.SHA256, source.SHA256)
	}

	// Все поля metadata содержат exact values из контракта.
	targetInfo, err := os.Lstat(source.Path)
	if err != nil {
		t.Fatalf("Lstat source: %v", err)
	}
	metadata := backup.Metadata
	if metadata.SchemaVersion != MetadataSchemaVersion {
		t.Errorf("schema_version: получено %d, ожидалось %d", metadata.SchemaVersion, MetadataSchemaVersion)
	}
	if metadata.BackupID != expectedFixedID {
		t.Errorf("metadata.BackupID: получено %q, ожидалось %q", metadata.BackupID, expectedFixedID)
	}
	expectedCreatedAt := fixedNow.Format(time.RFC3339Nano)
	if metadata.CreatedAt != expectedCreatedAt {
		t.Errorf("created_at: получено %q, ожидалось %q", metadata.CreatedAt, expectedCreatedAt)
	}
	if metadata.SourcePath != source.Path {
		t.Errorf("source_path: получено %q, ожидалось canonical %q", metadata.SourcePath, source.Path)
	}
	if metadata.SourceSHA256 != hex.EncodeToString(source.SHA256[:]) {
		t.Errorf("source_sha256: получено %q, ожидалось %q", metadata.SourceSHA256, hex.EncodeToString(source.SHA256[:]))
	}
	if metadata.BackupSHA256 != hex.EncodeToString(backup.SHA256[:]) {
		t.Errorf("backup_sha256: получено %q, ожидалось %q", metadata.BackupSHA256, hex.EncodeToString(backup.SHA256[:]))
	}
	stat, ok := targetInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("Stat_t source недоступен: %T", targetInfo.Sys())
	}
	if metadata.UID != stat.Uid || metadata.GID != stat.Gid {
		t.Errorf("uid/gid: получено %d/%d, ожидалось %d/%d", metadata.UID, metadata.GID, stat.Uid, stat.Gid)
	}
	if metadata.Mode != uint32(targetInfo.Mode().Perm()) {
		t.Errorf("mode: получено %04o, ожидалось %04o", metadata.Mode, targetInfo.Mode().Perm())
	}
	if metadata.Operation != OperationManual {
		t.Errorf("operation: получено %q, ожидалось %q", metadata.Operation, OperationManual)
	}
	if metadata.ProgramVersion != ProgramVersion {
		t.Errorf("program_version: получено %q, ожидалось %q", metadata.ProgramVersion, ProgramVersion)
	}
	if metadata.SourceVerification != SourceNotChecked {
		t.Errorf("source_verification: получено %q, ожидалось %q", metadata.SourceVerification, SourceNotChecked)
	}

	// Sidecar — валидный strict JSON: decoder с DisallowUnknownFields принимает его,
	// а json.Valid подтверждает формат. Регрессия: лишние ключи schema v1 запрещены.
	sidecar, err := os.ReadFile(backup.MetadataPath)
	if err != nil {
		t.Fatalf("прочитать sidecar: %v", err)
	}
	if !json.Valid(sidecar) {
		t.Errorf("sidecar не является валидным JSON: %q", sidecar)
	}
	decoder := json.NewDecoder(bytes.NewReader(sidecar))
	decoder.DisallowUnknownFields()
	var decoded Metadata
	if err := decoder.Decode(&decoded); err != nil {
		t.Errorf("sidecar отклонён DisallowUnknownFields decoder: %v", err)
	}

	// Target не изменён созданием backup.
	after, err := os.ReadFile(source.Path)
	if err != nil {
		t.Fatalf("прочитать target после Create: %v", err)
	}
	if !bytes.Equal(after, source.Data) {
		t.Errorf("target изменён после Create: получено %q, ожидалось %q", after, source.Data)
	}

	// Повторное открытие/чтение backup даёт тот же хеш: файл устойчив на диске.
	reread, err := writer.ReadSnapshot(backup.Path)
	if err != nil {
		t.Fatalf("повторно прочитать backup: %v", err)
	}
	if reread.SHA256 != backup.SHA256 {
		t.Errorf("повторное чтение backup: хеш %x, ожидалось %x", reread.SHA256, backup.SHA256)
	}
}

// TestCreateCollision проверяет именование при коллизии: занятый base даёт суффикс
// ".1", повторная коллизия — ".2", а backup_id в metadata остаётся одним и тем же.
func TestCreateCollision(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	options := CreateOptions{
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	}

	baseData := filepath.Join(filepath.Dir(source.Path), "fstab.bak."+expectedFixedID)
	baseSidecar := baseData + ".json"
	// Занимаем base пару заранее произвольным содержимым, имитируя чужие файлы.
	if err := os.WriteFile(baseData, []byte("чужой data"), 0o600); err != nil {
		t.Fatalf("создать чужой data: %v", err)
	}
	if err := os.WriteFile(baseSidecar, []byte("{}"), 0o600); err != nil {
		t.Fatalf("создать чужой sidecar: %v", err)
	}

	first, err := Create(source, options)
	if err != nil {
		t.Fatalf("Create при первой коллизии: %v", err)
	}
	expectedFirstData := filepath.Join(filepath.Dir(source.Path), "fstab.bak."+expectedFixedID+".1")
	if first.Path != expectedFirstData {
		t.Errorf("первый collision path: получено %q, ожидалось %q", first.Path, expectedFirstData)
	}
	if first.MetadataPath != expectedFirstData+".json" {
		t.Errorf("первый collision sidecar: получено %q, ожидалось %q", first.MetadataPath, expectedFirstData+".json")
	}

	second, err := Create(source, options)
	if err != nil {
		t.Fatalf("Create при второй коллизии: %v", err)
	}
	expectedSecondData := filepath.Join(filepath.Dir(source.Path), "fstab.bak."+expectedFixedID+".2")
	if second.Path != expectedSecondData {
		t.Errorf("второй collision path: получено %q, ожидалось %q", second.Path, expectedSecondData)
	}

	// Backup ID в metadata не заменяется числовым суффиксом: все три копии одного момента.
	for _, backup := range []Backup{first, second} {
		if backup.ID != expectedFixedID {
			t.Errorf("backup_id при коллизии: получено %q, ожидалось %q", backup.ID, expectedFixedID)
		}
		if backup.Metadata.BackupID != expectedFixedID {
			t.Errorf("metadata backup_id при коллизии: получено %q, ожидалось %q", backup.Metadata.BackupID, expectedFixedID)
		}
	}
}

// TestCreateDefaults проверяет: пустой prefix использует fstab.bak, а явный Directory
// размещает оба файла в нём, даже если он отличается от каталога target.
func TestCreateDefaults(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	options := CreateOptions{
		Operation:          OperationBeforeEdit,
		SourceVerification: SourceValid,
		Now:                fixedNow,
	}

	// Пустой prefix — default fstab.bak.
	backup, err := Create(source, options)
	if err != nil {
		t.Fatalf("Create с пустым prefix: %v", err)
	}
	expected := filepath.Join(filepath.Dir(source.Path), DefaultBackupPrefix+"."+expectedFixedID)
	if backup.Path != expected {
		t.Errorf("пустой prefix: получено %q, ожидалось %q", backup.Path, expected)
	}

	// Явный Directory: оба файла размещаются именно там.
	customDir := t.TempDir()
	backup, err = Create(source, CreateOptions{
		Directory:          customDir,
		Prefix:             "custom.bak",
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Create с явным Directory: %v", err)
	}
	expected = filepath.Join(customDir, "custom.bak."+expectedFixedID)
	if backup.Path != expected {
		t.Errorf("explicit Directory data: получено %q, ожидалось %q", backup.Path, expected)
	}
	if backup.MetadataPath != expected+".json" {
		t.Errorf("explicit Directory sidecar: получено %q, ожидалось %q", backup.MetadataPath, expected+".json")
	}
}

// TestCreateOperationWhitelist проверяет, что каждая operation из whitelist принимается,
// а пустая или неизвестная operation и неизвестная source verification отклоняются
// до создания любых файлов.
func TestCreateOperationWhitelist(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))

	// Все whitelist-значения принимаются.
	for _, operation := range []Operation{
		OperationBeforeAdd, OperationBeforeEdit, OperationBeforeDelete,
		OperationBeforeComment, OperationBeforeUncomment, OperationBeforeRollback,
		OperationManual,
	} {
		t.Run("accept/"+string(operation), func(t *testing.T) {
			backup, err := Create(source, CreateOptions{
				Directory:          t.TempDir(),
				Operation:          operation,
				SourceVerification: SourceNotChecked,
				Now:                fixedNow,
			})
			if err != nil {
				t.Fatalf("Create с operation %q: %v", operation, err)
			}
			if backup.Metadata.Operation != operation {
				t.Errorf("metadata operation: получено %q, ожидалось %q", backup.Metadata.Operation, operation)
			}
		})
	}

	// Недопустимые значения отклоняются до создания файлов.
	rejected := []struct {
		name               string
		operation          Operation
		sourceVerification SourceVerification
	}{
		{name: "empty operation", operation: "", sourceVerification: SourceNotChecked},
		{name: "unknown operation", operation: "before-hack", sourceVerification: SourceNotChecked},
		{name: "unknown verification", operation: OperationManual, sourceVerification: "maybe"},
		{name: "empty verification", operation: OperationManual, sourceVerification: ""},
	}
	for _, tc := range rejected {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			directory := t.TempDir()
			_, err := Create(source, CreateOptions{
				Directory:          directory,
				Operation:          tc.operation,
				SourceVerification: tc.sourceVerification,
				Now:                fixedNow,
			})
			if err == nil {
				t.Fatalf("ожидалась ошибка для %q", tc.name)
			}
			entries, readErr := os.ReadDir(directory)
			if readErr != nil {
				t.Fatalf("прочитать directory: %v", readErr)
			}
			if len(entries) != 0 {
				t.Errorf("файлы созданы до отклонения опций: %v", entries)
			}
		})
	}
}

// TestCreateRejectsUnsafePrefix проверяет, что prefix с разделителями пути
// отклоняется до создания файлов: иначе имя файла вышло бы за backup directory.
func TestCreateRejectsUnsafePrefix(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	for _, prefix := range []string{"../escape", "a/b", `a\b`, ".", "..", "bad\x00prefix"} {
		directory := t.TempDir()
		_, err := Create(source, CreateOptions{
			Directory:          directory,
			Prefix:             prefix,
			Operation:          OperationManual,
			SourceVerification: SourceNotChecked,
			Now:                fixedNow,
		})
		if err == nil {
			t.Errorf("prefix %q должен отклоняться", prefix)
		}
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			t.Fatalf("прочитать directory: %v", readErr)
		}
		if len(entries) != 0 {
			t.Errorf("файлы созданы при unsafe prefix %q: %v", prefix, entries)
		}
	}
}

// TestCreateDirectory проверяет обработку backup directory: отсутствующий каталог
// создаётся как real directory 0700, а существующий финальный symlink отклоняется.
func TestCreateDirectory(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	options := CreateOptions{
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	}

	// Отсутствующий custom directory создаётся с mode 0700.
	missingDir := filepath.Join(t.TempDir(), "nested", "backups")
	backup, err := Create(source, CreateOptions{
		Directory:          missingDir,
		Operation:          options.Operation,
		SourceVerification: options.SourceVerification,
		Now:                options.Now,
	})
	if err != nil {
		t.Fatalf("Create в отсутствующий directory: %v", err)
	}
	if filepath.Dir(backup.Path) != missingDir {
		t.Errorf("backup размещён не в custom directory: %q", backup.Path)
	}
	info, err := os.Lstat(missingDir)
	if err != nil {
		t.Fatalf("прочитать созданный directory: %v", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("directory не является real directory: %v", info.Mode())
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode directory: получено %04o, ожидалось 0700", info.Mode().Perm())
	}

	// Финальный symlink каталога отклоняется: небезопасная подмена места записи.
	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "linkdir")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("создать symlink directory: %v", err)
	}
	if _, err := Create(source, CreateOptions{
		Directory:          linkDir,
		Operation:          options.Operation,
		SourceVerification: options.SourceVerification,
		Now:                options.Now,
	}); err == nil {
		t.Fatal("существующий symlink directory должен быть отклонён")
	} else {
		entries, readErr := os.ReadDir(realDir)
		if readErr != nil {
			t.Fatalf("прочитать realDir: %v", readErr)
		}
		if len(entries) != 0 {
			t.Errorf("файлы попали через symlink directory: %v", entries)
		}
	}
}

// TestCreateSidecarSymlinkRejected проверяет, что sidecar-symlink для свободного
// имени является небезопасной подменой и отклоняется немедленно, а созданный
// data-файл текущей попытки очищается.
func TestCreateSidecarSymlinkRejected(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	directory := t.TempDir()
	dataPath := filepath.Join(directory, "fstab.bak."+expectedFixedID)
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), dataPath+".json"); err != nil {
		t.Fatalf("создать sidecar symlink: %v", err)
	}

	_, err := Create(source, CreateOptions{
		Directory:          directory,
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	})
	if err == nil {
		t.Fatal("sidecar symlink должен быть отклонён")
	}
	// Data текущей попытки очищен, чужой symlink не тронут, retry на .1 не выполнялся.
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatalf("прочитать directory: %v", readErr)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(dataPath)+".json" {
			t.Errorf("неожиданный файл после отклонения sidecar symlink: %s", entry.Name())
		}
	}
}

// TestCreateZeroNow проверяет, что zero Now допустимо и подставляет текущее UTC-время.
func TestCreateZeroNow(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	before := time.Now().UTC()

	backup, err := Create(source, CreateOptions{
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
	})
	if err != nil {
		t.Fatalf("Create с zero Now: %v", err)
	}
	parsed, err := time.Parse(backupIDLayout, backup.ID)
	if err != nil {
		t.Fatalf("backup ID %q не парсится layout: %v", backup.ID, err)
	}
	if parsed.Location() != time.UTC {
		t.Errorf("backup ID не в UTC: %v", parsed.Location())
	}
	if parsed.Before(before.Add(-time.Minute)) {
		t.Errorf("backup ID %q старше вызова Create", backup.ID)
	}
}

// TestCreateBinarySource проверяет побайтовое копирование invalid UTF-8/binary
// данных и совпадение source/backup SHA-256 в metadata.
func TestCreateBinarySource(t *testing.T) {
	binary := []byte{0xff, 0xfe, 0x00, 'a', 0x80, 0x00, 0x0d, 0x0a}
	source := newTargetSnapshot(t, binary)

	backup, err := Create(source, CreateOptions{
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Create binary source: %v", err)
	}
	data, err := os.ReadFile(backup.Path)
	if err != nil {
		t.Fatalf("прочитать data backup: %v", err)
	}
	if !bytes.Equal(data, binary) {
		t.Errorf("binary data искажена: получено %v, ожидалось %v", data, binary)
	}
	binaryHash := sha256.Sum256(binary)
	expectedHex := hex.EncodeToString(binaryHash[:])
	if backup.Metadata.SourceSHA256 != expectedHex || backup.Metadata.BackupSHA256 != expectedHex {
		t.Errorf("hashes metadata: source %q backup %q, ожидалось %q",
			backup.Metadata.SourceSHA256, backup.Metadata.BackupSHA256, expectedHex)
	}
}

// TestCreateUsesSnapshotNotDisk проверяет, что Create копирует переданный Snapshot
// и не перечитывает target с диска: изменение source после snapshot не влияет
// ни на data, ни на source hash в metadata.
func TestCreateUsesSnapshotNotDisk(t *testing.T) {
	source := newTargetSnapshot(t, []byte("оригинал\n"))
	changed := []byte("изменено на диске после snapshot\n")
	if err := os.WriteFile(source.Path, changed, 0o640); err != nil {
		t.Fatalf("изменить source на диске: %v", err)
	}

	backup, err := Create(source, CreateOptions{
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Create после изменения source: %v", err)
	}
	data, err := os.ReadFile(backup.Path)
	if err != nil {
		t.Fatalf("прочитать data backup: %v", err)
	}
	if !bytes.Equal(data, source.Data) {
		t.Errorf("backup data взята с диска, а не из snapshot: %q", data)
	}
	expectedSourceHex := hex.EncodeToString(source.SHA256[:])
	if backup.Metadata.SourceSHA256 != expectedSourceHex {
		t.Errorf("source_sha256 пересчитан с диска: получено %q, ожидалось %q",
			backup.Metadata.SourceSHA256, expectedSourceHex)
	}
}

// TestFormatBackupID проверяет форматирование backup ID: exact layout, дополнение
// nanoseconds нулями до девяти цифр и нормализацию не-UTC времени в UTC.
func TestFormatBackupID(t *testing.T) {
	cases := []struct {
		name     string
		time     time.Time
		expected string
	}{
		{
			name:     "фиксированное UTC время",
			time:     fixedNow,
			expected: expectedFixedID,
		},
		{
			name:     "nanoseconds дополняются нулями",
			time:     time.Date(2026, 11, 18, 15, 30, 45, 5, time.UTC),
			expected: "2026-11-18_15-30-45.000000005Z",
		},
		{
			name:     "сто миллионов nanoseconds без дополнения",
			time:     time.Date(2026, 1, 2, 3, 4, 5, 100000000, time.UTC),
			expected: "2026-01-02_03-04-05.100000000Z",
		},
		{
			name:     "не-UTC время нормализуется в UTC",
			time:     time.Date(2026, 11, 18, 18, 30, 45, 123456789, time.FixedZone("MSK", 3*3600)),
			expected: expectedFixedID,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatBackupID(tc.time); got != tc.expected {
				t.Errorf("formatBackupID(%v): получено %q, ожидалось %q", tc.time, got, tc.expected)
			}
		})
	}
}

// validMetadataMap строит минимальную валидную metadata schema v1 как map,
// чтобы тесты могли точечно ломать отдельные поля.
func validMetadataMap() map[string]any {
	return map[string]any{
		"schema_version":      1,
		"backup_id":           expectedFixedID,
		"created_at":          "2026-11-18T15:30:45.123456789Z",
		"source_path":         "/etc/fstab",
		"source_sha256":       strings.Repeat("a", 64),
		"backup_sha256":       strings.Repeat("b", 64),
		"uid":                 0,
		"gid":                 0,
		"mode":                384, // 0600
		"operation":           "manual",
		"program_version":     "dev",
		"source_verification": "not_checked",
	}
}

// marshalMetadataMap сериализует map metadata, завершая тест при ошибке.
func marshalMetadataMap(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("сериализовать metadata: %v", err)
	}
	return data
}

// TestParseMetadata проверяет strict валидацию schema v1: валидный документ
// принимается целиком, а каждое нарушение отдельно отклоняется с ErrInvalidBackup.
func TestParseMetadata(t *testing.T) {
	t.Run("valid принимается со всеми полями", func(t *testing.T) {
		metadata, err := parseMetadata(marshalMetadataMap(t, validMetadataMap()))
		if err != nil {
			t.Fatalf("parseMetadata валидного документа: %v", err)
		}
		if metadata.BackupID != expectedFixedID || metadata.Operation != OperationManual {
			t.Errorf("распарсенные поля: %+v", metadata)
		}
		if metadata.UID != 0 || metadata.Mode != 0o600 {
			t.Errorf("uid/mode: %+v", metadata)
		}
	})

	// Каждый случай ломает ровно один аспект документа.
	cases := []struct {
		name   string
		mutate func(map[string]any) []byte
	}{
		{
			name: "unknown field отклоняется",
			mutate: func(m map[string]any) []byte {
				m["extra"] = true
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "trailing JSON value отклоняется",
			mutate: func(m map[string]any) []byte {
				return append(marshalMetadataMap(t, m), []byte(` {"more":1}`)...)
			},
		},
		{
			name: "невалидный JSON отклоняется",
			mutate: func(m map[string]any) []byte {
				return []byte(`{"schema_version":`)
			},
		},
		{
			name: "неверный schema_version отклоняется",
			mutate: func(m map[string]any) []byte {
				m["schema_version"] = 2
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "backup_id не по regexp отклоняется",
			mutate: func(m map[string]any) []byte {
				m["backup_id"] = "2026-11-18_15-30-45.123456789" // нет Z
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "backup_id с несуществующей календарной датой отклоняется",
			mutate: func(m map[string]any) []byte {
				m["backup_id"] = "2026-13-40_25-61-61.123456789Z" // regexp проходит, календарь нет
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "created_at не UTC отклоняется",
			mutate: func(m map[string]any) []byte {
				m["created_at"] = "2026-11-18T15:30:45.123456789+00:00"
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "created_at не RFC3339Nano отклоняется",
			mutate: func(m map[string]any) []byte {
				m["created_at"] = "18.11.2026 15:30:45"
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "created_at не совпадает с backup_id отклоняется",
			mutate: func(m map[string]any) []byte {
				m["created_at"] = "2026-11-18T15:30:46.123456789Z" // секунда иная
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "created_at с иными nanoseconds отклоняется",
			mutate: func(m map[string]any) []byte {
				m["created_at"] = "2026-11-18T15:30:45.123456788Z"
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "относительный source_path отклоняется",
			mutate: func(m map[string]any) []byte {
				m["source_path"] = "etc/fstab"
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "неочищенный source_path отклоняется",
			mutate: func(m map[string]any) []byte {
				m["source_path"] = "/etc/../etc/fstab"
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "uppercase hex hash отклоняется",
			mutate: func(m map[string]any) []byte {
				m["source_sha256"] = strings.Repeat("A", 64)
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "короткий hex hash отклоняется",
			mutate: func(m map[string]any) []byte {
				m["backup_sha256"] = strings.Repeat("b", 63)
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "не-hex hash отклоняется",
			mutate: func(m map[string]any) []byte {
				m["source_sha256"] = strings.Repeat("g", 64)
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "uid выше MaxUint32 отклоняется",
			mutate: func(m map[string]any) []byte {
				m["uid"] = float64(math.MaxUint32) + 1
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "mode выше 07777 отклоняется",
			mutate: func(m map[string]any) []byte {
				m["mode"] = 0o10000
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "неизвестная operation отклоняется",
			mutate: func(m map[string]any) []byte {
				m["operation"] = "before-hack"
				return marshalMetadataMap(t, m)
			},
		},
		{
			name: "неизвестная source verification отклоняется",
			mutate: func(m map[string]any) []byte {
				m["source_verification"] = "maybe"
				return marshalMetadataMap(t, m)
			},
		},
	}
	for _, tc := range cases {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			_, err := parseMetadata(tc.mutate(validMetadataMap()))
			if err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
			if !errors.Is(err, ErrInvalidBackup) {
				t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
			}
		})
	}
}

// TestParseBackupFilename проверяет строгий разбор имён backup-файлов: принимаются
// только ровно "<prefix>.<id>" и "<prefix>.<id>.<suffix>", произвольные похожие
// имена отклоняются, чтобы чужие файлы не считались backup.
func TestParseBackupFilename(t *testing.T) {
	const prefix = "fstab.bak"
	validID := expectedFixedID

	accept := []struct {
		name     string
		expected int
	}{
		{name: prefix + "." + validID, expected: 0},
		{name: prefix + "." + validID + ".1", expected: 1},
		{name: prefix + "." + validID + ".999", expected: 999},
	}
	for _, tc := range accept {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			id, suffix, ok := parseBackupFilename(prefix, tc.name)
			if !ok {
				t.Fatalf("имя %q должно приниматься", tc.name)
			}
			if id != validID || suffix != tc.expected {
				t.Errorf("получено id=%q suffix=%d, ожидалось id=%q suffix=%d", id, suffix, validID, tc.expected)
			}
		})
	}

	reject := []string{
		prefix + "." + validID + ".json",               // sidecar не является data именем
		prefix + validID,                               // нет разделителя после prefix
		prefix + "X." + validID,                        // другой prefix
		"other." + validID,                             // чужой prefix
		prefix + "." + validID + ".",                   // пустой суффикс
		prefix + "." + validID + ".x",                  // нечисловой суффикс
		prefix + "." + validID + ".1.2",                // двойной суффикс
		prefix + "." + "2026-11-18_15-30-45.123456789", // нет Z
		prefix + "." + "2026-11-18_15-30-45.12345678Z", // восемь цифр nanoseconds
		"." + validID,                                  // пустой prefix у парсера
	}
	for _, name := range reject {
		t.Run("reject/"+name, func(t *testing.T) {
			if id, suffix, ok := parseBackupFilename(prefix, name); ok {
				t.Errorf("имя %q должно отклоняться, получено id=%q suffix=%d", name, id, suffix)
			}
		})
	}
}

// injectedError — sentinel-ошибки fault injection: проверяемые через errors.Is.
var errInjected = errors.New("injected fault")

// faultyBackupFile моделирует backupFile с ошибкой на выбранном этапе записи.
// Фиктивный writer не хранит данные: fault-сценариям важна точка отказа и cleanup.
type faultyBackupFile struct {
	name     string
	writeErr error
	syncErr  error
	closeErr error
}

func (f *faultyBackupFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *faultyBackupFile) Sync() error  { return f.syncErr }
func (f *faultyBackupFile) Close() error { return f.closeErr }
func (f *faultyBackupFile) Name() string { return f.name }

// TestCreateFaultInjection моделирует отказы каждого seam создания: ни data, ни
// sidecar не должны остаться на диске, target не изменён, первичная ошибка
// сохраняется. Тест копирует полный production config и заменяет один seam.
func TestCreateFaultInjection(t *testing.T) {
	// dataSeamFault заменяет openExclusive для data-файла (без .json) на faulty файл.
	dataSeamFault := func(operations createOperations, fault *faultyBackupFile) createOperations {
		baseOpen := operations.openExclusive
		operations.openExclusive = func(path string) (backupFile, error) {
			if !strings.HasSuffix(path, ".json") {
				fault.name = path
				return fault, nil
			}
			return baseOpen(path)
		}
		return operations
	}
	// sidecarSeamFault заменяет openExclusive для sidecar-файла на faulty файл,
	// пропуская data-открытие через production openExclusive.
	sidecarSeamFault := func(operations createOperations, fault *faultyBackupFile) createOperations {
		baseOpen := operations.openExclusive
		operations.openExclusive = func(path string) (backupFile, error) {
			if strings.HasSuffix(path, ".json") {
				fault.name = path
				return fault, nil
			}
			return baseOpen(path)
		}
		return operations
	}

	cases := []struct {
		name string
		// inject модифицирует production operations для сценария; directory — куда
		// должен попасть backup, если сбой происходит после выбора каталога.
		inject func(createOperations) createOperations
	}{
		{
			name: "mkdirAll ошибка",
			inject: func(operations createOperations) createOperations {
				operations.mkdirAll = func(string, os.FileMode) error { return errInjected }
				return operations
			},
		},
		{
			name: "lstat source ошибка",
			inject: func(operations createOperations) createOperations {
				baseLstat := operations.lstat
				operations.lstat = func(path string) (os.FileInfo, error) {
					if strings.HasSuffix(path, "fstab") && !strings.Contains(path, "fstab.bak") {
						return nil, errInjected
					}
					return baseLstat(path)
				}
				return operations
			},
		},
		{
			name: "openExclusive data ошибка",
			inject: func(operations createOperations) createOperations {
				operations.openExclusive = func(string) (backupFile, error) { return nil, errInjected }
				return operations
			},
		},
		{
			name: "data Write ошибка",
			inject: func(operations createOperations) createOperations {
				return dataSeamFault(operations, &faultyBackupFile{writeErr: errInjected})
			},
		},
		{
			name: "data Sync ошибка",
			inject: func(operations createOperations) createOperations {
				return dataSeamFault(operations, &faultyBackupFile{syncErr: errInjected})
			},
		},
		{
			name: "data Close ошибка",
			inject: func(operations createOperations) createOperations {
				return dataSeamFault(operations, &faultyBackupFile{closeErr: errInjected})
			},
		},
		{
			name: "sidecar open ошибка",
			inject: func(operations createOperations) createOperations {
				operations.openExclusive = func(path string) (backupFile, error) {
					if strings.HasSuffix(path, ".json") {
						return nil, errInjected
					}
					return productionCreateOperations().openExclusive(path)
				}
				return operations
			},
		},
		{
			name: "sidecar Write ошибка",
			inject: func(operations createOperations) createOperations {
				return sidecarSeamFault(operations, &faultyBackupFile{writeErr: errInjected})
			},
		},
		{
			name: "sidecar Sync ошибка",
			inject: func(operations createOperations) createOperations {
				return sidecarSeamFault(operations, &faultyBackupFile{syncErr: errInjected})
			},
		},
		{
			name: "sidecar Close ошибка",
			inject: func(operations createOperations) createOperations {
				return sidecarSeamFault(operations, &faultyBackupFile{closeErr: errInjected})
			},
		},
		{
			name: "read-back readSnapshot ошибка",
			inject: func(operations createOperations) createOperations {
				operations.readSnapshot = func(string) (writer.Snapshot, error) {
					return writer.Snapshot{}, errInjected
				}
				return operations
			},
		},
		{
			name: "read-back hash mismatch",
			inject: func(operations createOperations) createOperations {
				baseRead := operations.readSnapshot
				operations.readSnapshot = func(path string) (writer.Snapshot, error) {
					snapshot, err := baseRead(path)
					if err != nil {
						return snapshot, err
					}
					// Портим один байт data: metadata hash перестаёт совпадать.
					if !strings.HasSuffix(path, ".json") && len(snapshot.Data) > 0 {
						snapshot.Data[0] ^= 0xff
						snapshot.SHA256 = sha256.Sum256(snapshot.Data)
					}
					return snapshot, nil
				}
				return operations
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
			original, err := os.ReadFile(source.Path)
			if err != nil {
				t.Fatalf("прочитать target: %v", err)
			}
			directory := t.TempDir()
			if tc.name == "mkdirAll ошибка" {
				// MkdirAll вызывается только для отсутствующего каталога:
				// создаём сценарий, в котором seam реально понадобится.
				directory = filepath.Join(directory, "missing")
			}
			operations := tc.inject(productionCreateOperations())

			_, err = createWithOperations(source, CreateOptions{
				Directory:          directory,
				Operation:          OperationManual,
				SourceVerification: SourceNotChecked,
				Now:                fixedNow,
			}, operations)
			if err == nil {
				t.Fatal("ожидалась ошибка fault injection")
			}

			// Первичная причина сохраняется и распознаётся, кроме mismatch-сценария,
			// где первичной причиной по контракту является ErrInvalidBackup.
			if tc.name != "read-back hash mismatch" && !errors.Is(err, errInjected) {
				t.Errorf("первичная ошибка потеряна: %v", err)
			}
			if tc.name == "read-back hash mismatch" && !errors.Is(err, ErrInvalidBackup) {
				t.Errorf("ожидался ErrInvalidBackup: %v", err)
			}

			// Ни data, ни sidecar не остаются после любого сбоя создания. Каталог
			// может отсутствовать, если сбой случился на mkdirAll.
			entries, readErr := os.ReadDir(directory)
			if readErr != nil {
				if !os.IsNotExist(readErr) {
					t.Fatalf("прочитать directory: %v", readErr)
				}
			} else {
				for _, entry := range entries {
					t.Errorf("оставшийся файл после сбоя: %s", entry.Name())
				}
			}

			// Target не изменён.
			after, err := os.ReadFile(source.Path)
			if err != nil {
				t.Fatalf("прочитать target после сбоя: %v", err)
			}
			if !bytes.Equal(after, original) {
				t.Errorf("target изменён после сбоя создания backup")
			}
		})
	}
}

// TestCreateCleanupJoinsErrors проверяет, что ошибка cleanup присоединяется
// к первичной и обе причины доступны через errors.Is: первичная ошибка никогда
// не заменяется ошибкой cleanup.
func TestCreateCleanupJoinsErrors(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	directory := t.TempDir()
	operations := productionCreateOperations()

	injectedWrite := errors.New("injected sidecar write")
	injectedRemove := errors.New("injected remove")
	baseOpen := operations.openExclusive
	operations.openExclusive = func(path string) (backupFile, error) {
		if strings.HasSuffix(path, ".json") {
			return &faultyBackupFile{name: path, writeErr: injectedWrite}, nil
		}
		return baseOpen(path)
	}
	operations.remove = func(path string) error {
		if !strings.HasSuffix(path, ".json") {
			return injectedRemove // удалить data не удалось: cleanup частично failed
		}
		return os.Remove(path)
	}

	_, err := createWithOperations(source, CreateOptions{
		Directory:          directory,
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	}, operations)
	if err == nil {
		t.Fatal("ожидалась ошибка sidecar write")
	}
	if !errors.Is(err, injectedWrite) {
		t.Errorf("первичная ошибка write недоступна: %v", err)
	}
	if !errors.Is(err, injectedRemove) {
		t.Errorf("ошибка cleanup недоступна: %v", err)
	}
}

// TestCreateCollisionRetry проверяет переходы на следующий суффикс: EEXIST data и
// EEXIST sidecar действительно приводят к ".1", а ELOOP от data open возвращает
// ошибку немедленно, без перехода и без чистки чужих файлов.
func TestCreateCollisionRetry(t *testing.T) {
	const baseName = "fstab.bak." + expectedFixedID

	t.Run("EEXIST data переходит на .1", func(t *testing.T) {
		source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		directory := t.TempDir()
		userData := filepath.Join(directory, baseName)
		if err := os.WriteFile(userData, []byte("чужой data"), 0o600); err != nil {
			t.Fatalf("создать чужой data: %v", err)
		}

		backup, err := createWithOperations(source, CreateOptions{
			Directory:          directory,
			Operation:          OperationManual,
			SourceVerification: SourceNotChecked,
			Now:                fixedNow,
		}, productionCreateOperations())
		if err != nil {
			t.Fatalf("Create при EEXIST data: %v", err)
		}
		if backup.Path != filepath.Join(directory, baseName+".1") {
			t.Errorf("path: получено %q, ожидалось %q.1", backup.Path, baseName)
		}
		// Чужой data остался нетронут.
		content, err := os.ReadFile(userData)
		if err != nil {
			t.Fatalf("прочитать чужой data: %v", err)
		}
		if string(content) != "чужой data" {
			t.Errorf("чужой data изменён: %q", content)
		}
	})

	t.Run("EEXIST sidecar очищает data и переходит на .1", func(t *testing.T) {
		source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		directory := t.TempDir()
		userSidecar := filepath.Join(directory, baseName+".json")
		if err := os.WriteFile(userSidecar, []byte("чужой sidecar"), 0o600); err != nil {
			t.Fatalf("создать чужой sidecar: %v", err)
		}

		backup, err := createWithOperations(source, CreateOptions{
			Directory:          directory,
			Operation:          OperationManual,
			SourceVerification: SourceNotChecked,
			Now:                fixedNow,
		}, productionCreateOperations())
		if err != nil {
			t.Fatalf("Create при EEXIST sidecar: %v", err)
		}
		if backup.Path != filepath.Join(directory, baseName+".1") {
			t.Errorf("path: получено %q, ожидалось %q.1", backup.Path, baseName)
		}
		// Data base имени, созданный текущей попыткой, очищен; чужой sidecar цел.
		if _, err := os.Lstat(filepath.Join(directory, baseName)); !os.IsNotExist(err) {
			t.Errorf("data base имени должен быть удалён, err=%v", err)
		}
		content, err := os.ReadFile(userSidecar)
		if err != nil {
			t.Fatalf("прочитать чужой sidecar: %v", err)
		}
		if string(content) != "чужой sidecar" {
			t.Errorf("чужой sidecar изменён: %q", content)
		}
	})

	t.Run("ELOOP от data open немедленная ошибка", func(t *testing.T) {
		source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		directory := t.TempDir()
		// ELOOP моделируется через seam: реальный symlink с O_EXCL даёт EEXIST,
		// поэтому небезопасная подмена проверяется именно инъекцией unix.ELOOP.
		operations := productionCreateOperations()
		operations.openExclusive = func(path string) (backupFile, error) {
			if !strings.HasSuffix(path, ".json") {
				return nil, fmt.Errorf("создать backup файл %s: %w", path, unix.ELOOP)
			}
			return productionCreateOperations().openExclusive(path)
		}

		_, err := createWithOperations(source, CreateOptions{
			Directory:          directory,
			Operation:          OperationManual,
			SourceVerification: SourceNotChecked,
			Now:                fixedNow,
		}, operations)
		if err == nil {
			t.Fatal("ELOOP от data open должен немедленно вернуть ошибку")
		}
		if !errors.Is(err, unix.ELOOP) {
			t.Errorf("первичная ошибка ELOOP потеряна: %v", err)
		}
		// Немедленный возврат: retry на .1 не выполнялся.
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			t.Fatalf("прочитать directory: %v", readErr)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".1") {
				t.Errorf("retry-файл создан при ELOOP: %s", entry.Name())
			}
		}
	})
}

// TestCreateNilOperationField проверяет, что incomplete operations config
// отклоняется до создания любых файлов.
func TestCreateNilOperationField(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	directory := t.TempDir()
	operations := productionCreateOperations()
	operations.remove = nil

	called := false
	baseOpen := operations.openExclusive
	operations.openExclusive = func(path string) (backupFile, error) {
		called = true
		return baseOpen(path)
	}

	_, err := createWithOperations(source, CreateOptions{
		Directory:          directory,
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	}, operations)
	if err == nil {
		t.Fatal("incomplete config должен отклоняться")
	}
	if called {
		t.Error("openExclusive вызван при incomplete config")
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatalf("прочитать directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("файлы созданы при incomplete config: %v", entries)
	}
}

// TestCreateCleanupKeepsUserFiles проверяет, что cleanup ограничен путями,
// созданными текущим вызовом: заранее созданные user-файлы с похожими именами
// (base пара) не удаляются при сбое на суффиксе ".1".
func TestCreateCleanupKeepsUserFiles(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	directory := t.TempDir()
	const baseName = "fstab.bak." + expectedFixedID
	userData := filepath.Join(directory, baseName)
	userSidecar := userData + ".json"
	if err := os.WriteFile(userData, []byte("user data"), 0o600); err != nil {
		t.Fatalf("создать user data: %v", err)
	}
	if err := os.WriteFile(userSidecar, []byte("user sidecar"), 0o600); err != nil {
		t.Fatalf("создать user sidecar: %v", err)
	}

	// Сбой записи sidecar: сначала base занят (user files), попытка пойдёт на ".1".
	injectedWrite := errors.New("injected sidecar write")
	operations := productionCreateOperations()
	baseOpen := operations.openExclusive
	operations.openExclusive = func(path string) (backupFile, error) {
		if strings.HasSuffix(path, ".json") {
			return &faultyBackupFile{name: path, writeErr: injectedWrite}, nil
		}
		return baseOpen(path)
	}

	_, err := createWithOperations(source, CreateOptions{
		Directory:          directory,
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	}, operations)
	if !errors.Is(err, injectedWrite) {
		t.Fatalf("ожидалась ошибка sidecar write: %v", err)
	}

	// User-файлы на месте с исходным содержимым.
	for path, want := range map[string]string{userData: "user data", userSidecar: "user sidecar"} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("user file %s потерян: %v", path, readErr)
		}
		if string(content) != want {
			t.Errorf("user file %s изменён: %q", path, content)
		}
	}
	// Файл ".1", созданный текущим вызовом, очищен.
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatalf("прочитать directory: %v", readErr)
	}
	if len(entries) != 2 {
		t.Errorf("ожидались ровно user-файлы, получено: %d entries", len(entries))
	}
}
