// Пакет backup реализует создание резервных копий fstab с sidecar-метаданными.
//
// Этот тестовый файл покрывает Task 2 (часть Open): верификация существующих
// backup-пар, обнаружение подмены и повреждений, обработка sidecar-имён.
// Тесты используют только t.TempDir и никогда не трогают реальный /etc/fstab;
// root не требуется.
package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fstab-edit/internal/writer"
)

// createValidBackup создаёт один валидный backup через production Create
// с фиксированным временем. Хелпер устраняет повторение стандартных опций.
func createValidBackup(t *testing.T, content []byte) Backup {
	t.Helper()
	source := newTargetSnapshot(t, content)
	backup, err := Create(source, CreateOptions{
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("создать валидный backup: %v", err)
	}
	return backup
}

// rewriteSidecar перезаписывает sidecar новым содержимым, сохраняя mode 0600.
// Тесты chmod/пишут только собственные temp-файлы.
func rewriteSidecar(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("перезаписать sidecar %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("восстановить mode 0600 sidecar %s: %v", path, err)
	}
}

// sidecarAsMap читает sidecar в map, чтобы тесты могли точечно менять поля.
func sidecarAsMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("прочитать sidecar %s: %v", path, err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("распарсить sidecar %s: %v", path, err)
	}
	return fields
}

// TestOpenByDataAndSidecarPath проверяет, что Open принимает и data, и sidecar
// путь, возвращая один и тот же проверенный Backup: равные ID, canonical paths,
// metadata и SHA-256. Data побайтово совпадает с исходным содержимым, а sidecar
// остаётся strict JSON без unknown keys.
func TestOpenByDataAndSidecarPath(t *testing.T) {
	content := []byte("# test fstab\nUUID=abc / ext4 defaults 0 1\n")
	created := createValidBackup(t, content)

	byData, err := Open(created.Path)
	if err != nil {
		t.Fatalf("Open по data path: %v", err)
	}
	bySidecar, err := Open(created.MetadataPath)
	if err != nil {
		t.Fatalf("Open по sidecar path: %v", err)
	}

	for name, opened := range map[string]Backup{"по data": byData, "по sidecar": bySidecar} {
		if opened.ID != created.ID {
			t.Errorf("%s: ID: получено %q, ожидалось %q", name, opened.ID, created.ID)
		}
		if opened.Path != created.Path {
			t.Errorf("%s: Path: получено %q, ожидалось %q", name, opened.Path, created.Path)
		}
		if opened.MetadataPath != created.MetadataPath {
			t.Errorf("%s: MetadataPath: получено %q, ожидалось %q", name, opened.MetadataPath, created.MetadataPath)
		}
		if opened.Metadata != created.Metadata {
			t.Errorf("%s: metadata: получено %+v, ожидалось %+v", name, opened.Metadata, created.Metadata)
		}
		if opened.SHA256 != created.SHA256 {
			t.Errorf("%s: SHA256: получено %x, ожидалось %x", name, opened.SHA256, created.SHA256)
		}
	}

	// Data побайтово равна исходному содержимому target.
	data, err := os.ReadFile(byData.Path)
	if err != nil {
		t.Fatalf("прочитать data backup: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("data backup не равна исходному содержимому: получено %q, ожидалось %q", data, content)
	}

	// Sidecar остаётся strict JSON: DisallowUnknownFields decoder принимает его.
	sidecar, err := os.ReadFile(byData.MetadataPath)
	if err != nil {
		t.Fatalf("прочитать sidecar: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(sidecar))
	decoder.DisallowUnknownFields()
	var decoded Metadata
	if err := decoder.Decode(&decoded); err != nil {
		t.Errorf("sidecar отклонён strict decoder: %v", err)
	}
}

// TestOpenCanonicalizesPath проверяет, что Open нормализует путь: избыточный
// разделитель и компонент "." не мешают открытию backup.
func TestOpenCanonicalizesPath(t *testing.T) {
	created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))

	unclean := filepath.Dir(created.Path) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(created.Path)
	opened, err := Open(unclean)
	if err != nil {
		t.Fatalf("Open по неочищенному пути: %v", err)
	}
	if opened.Path != created.Path {
		t.Errorf("Path: получено %q, ожидалось %q", opened.Path, created.Path)
	}
}

// TestOpenDetectsDataTampering проверяет, что повреждение одного байта data
// без обновления metadata обнаруживается сверкой SHA-256: ErrInvalidBackup.
func TestOpenDetectsDataTampering(t *testing.T) {
	created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))

	data, err := os.ReadFile(created.Path)
	if err != nil {
		t.Fatalf("прочитать data backup: %v", err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(created.Path, data, 0o600); err != nil {
		t.Fatalf("повредить data backup: %v", err)
	}

	_, err = Open(created.Path)
	if err == nil {
		t.Fatal("Open должен отклонить повреждённый data backup")
	}
	if !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
	}
}

// TestOpenDetectsMetadataHashTampering проверяет, что замена hex-символа в
// backup_sha256 metadata ломает сверку хеша: backup отклоняется.
func TestOpenDetectsMetadataHashTampering(t *testing.T) {
	created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))

	fields := sidecarAsMap(t, created.MetadataPath)
	hash, ok := fields["backup_sha256"].(string)
	if !ok || len(hash) != 64 {
		t.Fatalf("backup_sha256 в sidecar не является строкой из 64 символов: %v", fields["backup_sha256"])
	}
	tampered := hash[:60] + "0000"
	if tampered == hash {
		tampered = hash[:60] + "1111"
	}
	fields["backup_sha256"] = tampered
	rewriteSidecar(t, created.MetadataPath, marshalMetadataMap(t, fields))

	_, err := Open(created.Path)
	if err == nil {
		t.Fatal("Open должен отклонить backup с подделанным backup_sha256")
	}
	if !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
	}
}

// TestOpenForeignSourcePathStillOpens проверяет, что sidecar с source_path
// другого canonical target по-прежнему является валидным backup (Open успешен),
// но не участвует в List/Select чужого target. Фильтрация по владельцу — задача
// List, а Open верифицирует только целостность пары.
func TestOpenForeignSourcePathStillOpens(t *testing.T) {
	created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))

	fields := sidecarAsMap(t, created.MetadataPath)
	fields["source_path"] = filepath.Join(t.TempDir(), "other-fstab")
	rewriteSidecar(t, created.MetadataPath, marshalMetadataMap(t, fields))

	opened, err := Open(created.Path)
	if err != nil {
		t.Fatalf("Open с чужим source_path должен успешно верифицировать пару: %v", err)
	}
	if opened.ID != created.ID {
		t.Errorf("ID: получено %q, ожидалось %q", opened.ID, created.ID)
	}

	// Чужая копия не видна List оригинального target.
	listed, err := List(created.Metadata.SourcePath, "", filepath.Dir(created.Path))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, backup := range listed {
		if backup.Path == created.Path {
			t.Errorf("backup с чужим source_path попал в List target: %s", backup.Path)
		}
	}
}

// TestOpenRejectsInvalidMetadata проверяет через Open целиком, что каждое
// нарушение schema v1 в sidecar отклоняется: неверная schema_version, неизвестная
// operation, неизвестная source verification, mode выше 07777, created_at не в
// UTC, backup_id с лишним суффиксом и backup_id неправильной длины.
func TestOpenRejectsInvalidMetadata(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "неверная schema_version",
			mutate: func(m map[string]any) {
				m["schema_version"] = 2
			},
		},
		{
			name: "неизвестная operation",
			mutate: func(m map[string]any) {
				m["operation"] = "before-hack"
			},
		},
		{
			name: "неизвестная source verification",
			mutate: func(m map[string]any) {
				m["source_verification"] = "maybe"
			},
		},
		{
			name: "mode выше 07777",
			mutate: func(m map[string]any) {
				m["mode"] = 0o10000
			},
		},
		{
			name: "created_at не в UTC",
			mutate: func(m map[string]any) {
				m["created_at"] = "2026-11-18T15:30:45.123456789+00:00"
			},
		},
		{
			name: "backup_id с суффиксом коллизии",
			mutate: func(m map[string]any) {
				m["backup_id"] = expectedFixedID + ".1"
			},
		},
		{
			name: "backup_id неправильной длины",
			mutate: func(m map[string]any) {
				m["backup_id"] = "2026-11-18_15-30-45.12345678Z" // 8 цифр nanoseconds
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
			fields := sidecarAsMap(t, created.MetadataPath)
			tc.mutate(fields)
			rewriteSidecar(t, created.MetadataPath, marshalMetadataMap(t, fields))

			_, err := Open(created.Path)
			if err == nil {
				t.Fatal("Open должен отклонить backup с невалидной metadata")
			}
			if !errors.Is(err, ErrInvalidBackup) {
				t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
			}
		})
	}
}

// TestOpenRejectsSymlinkSubstitution проверяет, что замена data или sidecar
// на symlink является небезопасной подменой: Open отклоняет её через ErrInvalidBackup,
// не следуя по ссылке.
func TestOpenRejectsSymlinkSubstitution(t *testing.T) {
	t.Run("data symlink", func(t *testing.T) {
		created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		if err := os.Remove(created.Path); err != nil {
			t.Fatalf("удалить data перед подменой: %v", err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), created.Path); err != nil {
			t.Fatalf("создать data symlink: %v", err)
		}

		_, err := Open(created.Path)
		if err == nil {
			t.Fatal("Open должен отклонить data symlink")
		}
		if !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
		}
	})

	t.Run("sidecar symlink", func(t *testing.T) {
		created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		if err := os.Remove(created.MetadataPath); err != nil {
			t.Fatalf("удалить sidecar перед подменой: %v", err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), created.MetadataPath); err != nil {
			t.Fatalf("создать sidecar symlink: %v", err)
		}

		_, err := Open(created.MetadataPath)
		if err == nil {
			t.Fatal("Open должен отклонить sidecar symlink")
		}
		if !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
		}
	})
}

// TestOpenRejectsRelaxedMode проверяет, что data или sidecar с mode отличным
// от 0600 отклоняются: ослабленные права делают tampering незаметным. Тесты chmod
// только собственные temp-файлы.
func TestOpenRejectsRelaxedMode(t *testing.T) {
	t.Run("data mode 0644", func(t *testing.T) {
		created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		if err := os.Chmod(created.Path, 0o644); err != nil {
			t.Fatalf("chmod data: %v", err)
		}
		_, err := Open(created.Path)
		if err == nil {
			t.Fatal("Open должен отклонить data с mode 0644")
		}
		if !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
		}
	})

	t.Run("sidecar mode 0640", func(t *testing.T) {
		created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		if err := os.Chmod(created.MetadataPath, 0o640); err != nil {
			t.Fatalf("chmod sidecar: %v", err)
		}
		_, err := Open(created.Path)
		if err == nil {
			t.Fatal("Open должен отклонить sidecar с mode 0640")
		}
		if !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
		}
	})

	t.Run("data mode 0400", func(t *testing.T) {
		created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		if err := os.Chmod(created.Path, 0o400); err != nil {
			t.Fatalf("chmod data: %v", err)
		}
		_, err := Open(created.Path)
		if err == nil {
			t.Fatal("Open должен отклонить data с mode 0400")
		}
		if !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
		}
	})
}

// TestOpenRejectsCorruptSidecar проверяет, что пустой, испорченный JSON и sidecar
// с trailing JSON value отклоняются без panic: sidecar — ровно один JSON-документ.
func TestOpenRejectsCorruptSidecar(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
	}{
		{name: "пустой sidecar", content: nil},
		{name: "испорченный JSON", content: []byte(`{"schema_version":`)},
		{name: "trailing JSON value", content: marshalMetadataMap(t, validMetadataMap())},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			// Сначала создаём валидную пару, затем портим sidecar: так data
			// гарантированно валиден и проверяется именно parser sidecar.
			source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
			backup, err := Create(source, CreateOptions{
				Directory:          directory,
				Operation:          OperationManual,
				SourceVerification: SourceNotChecked,
				Now:                fixedNow.Add(time.Duration(i) * time.Second),
			})
			if err != nil {
				t.Fatalf("создать backup: %v", err)
			}
			content := tc.content
			if tc.name == "trailing JSON value" {
				content = append(content, []byte(` {"more":1}`)...)
			}
			rewriteSidecar(t, backup.MetadataPath, content)

			_, err = Open(backup.Path)
			if err == nil {
				t.Fatal("Open должен отклонить повреждённый sidecar")
			}
			if !errors.Is(err, ErrInvalidBackup) {
				t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
			}
		})
	}
}

// TestOpenOrphanSidecar проверяет, что sidecar без data-файла не образует backup:
// Open по orphan sidecar отклоняется, а foreign sidecar произвольного имени
// (prefix.9999-...) без data не создаёт Backup через Open.
func TestOpenOrphanSidecar(t *testing.T) {
	t.Run("sidecar без data", func(t *testing.T) {
		created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
		if err := os.Remove(created.Path); err != nil {
			t.Fatalf("удалить data: %v", err)
		}
		_, err := Open(created.MetadataPath)
		if err == nil {
			t.Fatal("Open должен отклонить sidecar без data")
		}
		if !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
		}
	})

	t.Run("foreign sidecar без data", func(t *testing.T) {
		directory := t.TempDir()
		orphanSidecar := filepath.Join(directory, DefaultBackupPrefix+".9999-12-31_23-59-59.999999999Z.json")
		rewriteSidecar(t, orphanSidecar, marshalMetadataMap(t, validMetadataMap()))

		_, err := Open(orphanSidecar)
		if err == nil {
			t.Fatal("Open должен отклонить foreign sidecar без data")
		}
		if !errors.Is(err, ErrInvalidBackup) {
			t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
		}
	})
}

// TestOpenRejectsMissingFile проверяет, что Open по несуществующему пути
// возвращает ошибку, сохраняющую причину os.ErrNotExist.
func TestOpenRejectsMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "fstab.bak."+expectedFixedID)
	_, err := Open(missing)
	if err == nil {
		t.Fatal("Open по несуществующему пути должен возвращать ошибку")
	}
	if !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("причина os.ErrNotExist потеряна: %v", err)
	}
}

// TestReadVerifiedBackupRejectsRelaxedMode проверяет на уровне единого verify
// seam, что readVerifiedBackup отклоняет пару с mode отличным от 0600: этот же
// путь используют Create, Open, List и Select, поэтому ослабленные права не
// проходят ни в одном из них.
func TestReadVerifiedBackupRejectsRelaxedMode(t *testing.T) {
	created := createValidBackup(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	if err := os.Chmod(created.MetadataPath, 0o644); err != nil {
		t.Fatalf("chmod sidecar: %v", err)
	}

	_, err := readVerifiedBackup(writer.ReadSnapshot, writer.Snapshot{}, created.Path, created.MetadataPath)
	if err == nil {
		t.Fatal("readVerifiedBackup должен отклонить sidecar с mode 0644")
	}
	if !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("ошибка не оборачивает ErrInvalidBackup: %v", err)
	}
}

// TestOpenDataMatchesSourceHash проверяет, что Open возвращает backup, чей
// SHA-256 metadata совпадает с фактическим хешем data: целостность восстанавливаемых
// байтов подтверждена заново при открытии.
func TestOpenDataMatchesSourceHash(t *testing.T) {
	content := []byte("# complex\nUUID=def /home ext4 noatime 0 2\n")
	created := createValidBackup(t, content)

	opened, err := Open(created.Path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	actual := sha256.Sum256(content)
	if opened.SHA256 != actual {
		t.Errorf("SHA256 backup: получено %x, ожидалось %x", opened.SHA256, actual)
	}
	if opened.Metadata.BackupSHA256 != hex.EncodeToString(actual[:]) {
		t.Errorf("backup_sha256 metadata: получено %q, ожидалось %q",
			opened.Metadata.BackupSHA256, hex.EncodeToString(actual[:]))
	}
	if strings.TrimSpace(opened.Metadata.SourcePath) == "" {
		t.Error("source_path в metadata пуст")
	}
}
