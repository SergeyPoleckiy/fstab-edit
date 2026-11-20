// Этот файл реализует обнаружение и выбор backups: List безопасно сканирует
// backup directory, отфильтровывая чужие, повреждённые и подменённые файлы,
// и возвращает копии текущего canonical target в детерминированном newest-first
// порядке; Select выбирает копию по ID поверх List. Имя файла здесь — только
// подсказка для scan: принадлежность target подтверждает metadata.

package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fstab-edit/internal/writer"
)

// Selection — результат выбора backup по ID. RequestedID сохраняет исходный
// запрос, включая пустой (newest). Candidates содержит все eligible backups
// в том же newest-first порядке, что и List; Selected валиден только при
// err == nil.
type Selection struct {
	// RequestedID — переданный запрос без изменений, включая пустую строку.
	RequestedID string
	// Selected — выбранный backup; нулевое значение означает отсутствие выбора.
	Selected Backup
	// Candidates — все eligible backups, доступные для запрошенного target.
	Candidates []Backup
}

// List возвращает все валидные backups заданного canonical target в directory,
// отсортированные newest first. Пустой directory означает каталог target, пустой
// prefix — DefaultBackupPrefix. Scan читает только entry names каталога; каждый
// sidecar-кандидат верифицируется через Open, а неподходящие записи пропускаются:
//   - symlink и не-regular записи (Open не следует по ссылке и не удаляет её);
//   - metadata с source_path, отличным от canonical target (чужой backup);
//   - имена вне формата "<prefix>.<backup ID>[.<suffix>]" или с metadata ID,
//     не совпадающим с ядром имени файла.
//
// Ошибка чтения отдельной записи не прерывает весь scan; недоступный сам
// каталог — ошибка вызова, потому что отличить пустой результат от сбоя нельзя.
func List(targetPath, prefix, directory string) ([]Backup, error) {
	canonicalTarget, err := writer.CanonicalTargetPath(targetPath)
	if err != nil {
		return nil, err
	}
	resolvedDirectory, err := resolveListDirectory(directory, canonicalTarget)
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		prefix = DefaultBackupPrefix
	}

	entries, err := os.ReadDir(resolvedDirectory)
	if err != nil {
		return nil, fmt.Errorf("прочитать backup directory %s: %w", resolvedDirectory, err)
	}
	backups := make([]Backup, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		// Symlink-записи (включая ссылки на каталоги) пропускаются до Open:
		// scan никогда не следует по ссылке и никогда ничего не удаляет.
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		sidecarPath := filepath.Join(resolvedDirectory, name)
		// Ошибка верификации отдельной записи — повод пропустить её, а не весь
		// scan: среди чужих и повреждённых файлов свои копии обязаны находиться.
		backup, err := Open(sidecarPath)
		if err != nil {
			continue
		}
		if backup.Metadata.SourcePath != canonicalTarget {
			continue
		}
		dataName := strings.TrimSuffix(name, ".json")
		id, _, ok := parseBackupFilename(prefix, dataName)
		if !ok || id != backup.Metadata.BackupID {
			continue
		}
		backups = append(backups, backup)
	}
	sortBackupsNewestFirst(backups)
	return backups, nil
}

// Select выбирает backup по ID поверх List. Пустой ID выбирает самую новую
// eligible копию; непустой — точное совпадение ID. Отсутствие совпадений
// возвращает ErrBackupNotFound с непустым Candidates; несколько файлов одного
// ID возвращают ErrBackupAmbiguous с обоими кандидатами в детерминированном
// порядке. Selection возвращается при любом исходе, включая ошибку List.
func Select(targetPath, prefix, directory, requestedID string) (Selection, error) {
	selection := Selection{RequestedID: requestedID}
	listed, err := List(targetPath, prefix, directory)
	if err != nil {
		return selection, err
	}
	selection.Candidates = listed

	if requestedID == "" {
		if len(listed) == 0 {
			return selection, fmt.Errorf("%w: нет доступных backups", ErrBackupNotFound)
		}
		selection.Selected = listed[0]
		return selection, nil
	}

	matches := make([]Backup, 0, 1)
	for _, backup := range listed {
		if backup.ID == requestedID {
			matches = append(matches, backup)
		}
	}
	switch len(matches) {
	case 0:
		return selection, fmt.Errorf("%w: %s", ErrBackupNotFound, requestedID)
	case 1:
		selection.Selected = matches[0]
		return selection, nil
	default:
		return selection, fmt.Errorf("%w: %s (%d файлов)", ErrBackupAmbiguous, requestedID, len(matches))
	}
}

// resolveListDirectory приводит backup directory к абсолютному очищенному пути.
// Пустое значение означает каталог canonical target — то же правило, что и у Create.
func resolveListDirectory(directory, canonicalTarget string) (string, error) {
	if directory == "" {
		directory = filepath.Dir(canonicalTarget)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("получить абсолютный путь backup directory: %w", err)
	}
	return filepath.Clean(absolute), nil
}

// sortBackupsNewestFirst упорядочивает backups по убыванию времени создания;
// равные timestamps детерминированно разрешаются по backup ID, затем по path.
// Это один порядок для List и Candidates: независимо от порядка чтения каталога
// результат стабилен, а collision-копии одного ID идут base-имя раньше суффикса.
func sortBackupsNewestFirst(backups []Backup) {
	sort.SliceStable(backups, func(i, j int) bool {
		left, right := backups[i], backups[j]
		leftTime := backupCreatedTime(left)
		rightTime := backupCreatedTime(right)
		if !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		return left.Path < right.Path
	})
}

// backupCreatedTime извлекает UTC-время из backup ID. ID к моменту сортировки
// уже проверен parseBackupIDStrict внутри verify seam, поэтому ошибка здесь
// недостижима; нулевое время безопасно уходит в конец порядка.
func backupCreatedTime(backup Backup) time.Time {
	moment, err := parseBackupIDStrict(backup.ID)
	if err != nil {
		return time.Time{}
	}
	return moment
}
