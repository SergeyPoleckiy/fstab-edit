// Этот файл реализует верификацию существующих backup-пар: Open принимает
// data или sidecar путь, отклоняет symlink-подмену и mode отличный от 0600,
// а затем прогоняет пару через единый verify seam readVerifiedBackup. Никакой
// код здесь не доверяет имени файла: принадлежность target подтверждается
// только содержимым metadata.

package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"fstab-edit/internal/writer"
)

// Open открывает существующую backup-пару по пути к data-файлу или sidecar
// (с суффиксом ".json") и возвращает полностью проверенный Backup. Путь
// нормализуется в absolute clean, оба файла отдельно проверяются через Lstat
// (regular, не symlink) и прочитываются через readVerifiedBackup, который
// сверяет SHA-256 data, формат metadata и соответствие backup ID имени файла.
// Нарушение любого инварианта возвращает ErrInvalidBackup; владельца backup
// (source_path) Open не проверяет — это фильтрация List/Select.
func Open(path string) (Backup, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Backup{}, fmt.Errorf("получить абсолютный путь backup %s: %w", path, err)
	}
	clean := filepath.Clean(absolute)

	var dataPath, sidecarPath string
	if strings.HasSuffix(clean, ".json") {
		sidecarPath = clean
		dataPath = strings.TrimSuffix(clean, ".json")
	} else {
		dataPath = clean
		sidecarPath = clean + ".json"
	}

	// Оба пути проверяются отдельно и до чтения: Lstat не следует по финальному
	// symlink, поэтому подмена любого из файлов ссылкой отклоняется до open.
	for _, pairPath := range []string{dataPath, sidecarPath} {
		info, err := os.Lstat(pairPath)
		if err != nil {
			return Backup{}, fmt.Errorf("%w: прочитать metadata %s: %w", ErrInvalidBackup, pairPath, err)
		}
		if !info.Mode().IsRegular() {
			return Backup{}, fmt.Errorf("%w: %s не является regular file", ErrInvalidBackup, pairPath)
		}
	}

	// Source-пустой Snapshot отключает source-зависимые сверки: исходный target
	// на момент Open недоступен, а целостность пары подтверждается metadata.
	return readVerifiedBackup(writer.ReadSnapshot, writer.Snapshot{}, dataPath, sidecarPath)
}

// verifyBackupFileMode требует exact mode 0600 для backup-файла: backup не
// привилегированный, и любое ослабление прав (0644, 0400) делает tampering
// незаметным, поэтому такая пара отклоняется единым verify seam.
func verifyBackupFileMode(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: прочитать metadata %s: %w", ErrInvalidBackup, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s не является regular file", ErrInvalidBackup, path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("прочитать Stat_t backup файла %s", path)
	}
	if stat.Mode&0o7777 != 0o600 {
		return fmt.Errorf("%w: mode %s равен %04o, ожидалось 0600", ErrInvalidBackup, path, stat.Mode&0o7777)
	}
	return nil
}
