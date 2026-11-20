// Этот файл реализует жизненный цикл создания backup: безопасные пути, именование
// при коллизии, эксклюзивное создание data и sidecar с fsync, а затем обязательный
// read-back с проверкой SHA-256 до возврата Backup. Все файловые операции скрыты
// за immutable per-call operations struct для fault injection без mutable globals.

package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fstab-edit/internal/writer"
	"golang.org/x/sys/unix"
)

// maxCollisionAttempts ограничивает перебор collision-суффиксов для одного base ID:
// 0..999. Исчерпание лимита означает, что каталог заполнен чужими файлами,
// и продолжать подбор unsafe.
const maxCollisionAttempts = 1000

// backupFile — минимальный контракт создаваемого backup-файла для fault injection.
// *os.File удовлетворяет ему; тесты подменяют реализацию с ошибками на этапах.
type backupFile interface {
	io.Writer
	// Sync сбрасывает данные на диск перед close.
	Sync() error
	// Close завершает работу с descriptor; ошибка close сохраняется первичной.
	Close() error
	// Name возвращает путь файла.
	Name() string
}

// createOperations — immutable per-call seam создания backup. Тесты копируют
// полностью заполненный production config и заменяют отдельные function fields;
// полей снаружи пакета нет, mutable global state не используется.
type createOperations struct {
	// mkdirAll создаёт отсутствующий backup directory с заданными правами.
	mkdirAll func(path string, perm os.FileMode) error
	// lstat читает metadata без следования финальному symlink.
	lstat func(path string) (os.FileInfo, error)
	// openExclusive создаёт файл только эксклюзивно без следования symlink.
	openExclusive func(path string) (backupFile, error)
	// readSnapshot повторно читает созданные файлы для read-back проверки.
	readSnapshot func(path string) (writer.Snapshot, error)
	// remove удаляет файл; os.ErrNotExist при cleanup не является ошибкой.
	remove func(path string) error
}

// productionCreateOperations заполняет каждый seam прямой os/unix операцией.
func productionCreateOperations() createOperations {
	return createOperations{
		mkdirAll: os.MkdirAll,
		lstat:    os.Lstat,
		openExclusive: func(path string) (backupFile, error) {
			// O_EXCL гарантирует, что чужой файл не будет перезаписан; O_NOFOLLOW
			// отклоняет symlink-подмену; O_CLOEXEC не наследует descriptor детьми.
			fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if err != nil {
				return nil, fmt.Errorf("создать backup файл %s: %w", path, err)
			}
			return os.NewFile(uintptr(fd), path), nil
		},
		readSnapshot: writer.ReadSnapshot,
		remove:       os.Remove,
	}
}

// validateCreateOperations отклоняет incomplete config до создания любых файлов,
// превращая программную ошибку сборки seam-ов в обычную ошибку вызова.
func validateCreateOperations(operations createOperations) error {
	fields := []struct {
		name    string
		missing bool
	}{
		{name: "mkdirAll", missing: operations.mkdirAll == nil},
		{name: "lstat", missing: operations.lstat == nil},
		{name: "openExclusive", missing: operations.openExclusive == nil},
		{name: "readSnapshot", missing: operations.readSnapshot == nil},
		{name: "remove", missing: operations.remove == nil},
	}
	for _, field := range fields {
		if field.missing {
			return fmt.Errorf("create operations: nil field %s", field.name)
		}
	}
	return nil
}

// Create создаёт data backup и sidecar metadata рядом с target или в options.Directory.
// Функция копирует переданный Snapshot и не перечитывает target с диска; backup
// возвращается только после записи, fsync обоих файлов, повторного чтения и
// совпадения SHA-256. Target не изменяется. При любой ошибке файлы текущего
// вызова удаляются, а первичная ошибка сохраняется через errors.Join.
func Create(source writer.Snapshot, options CreateOptions) (Backup, error) {
	return createWithOperations(source, options, productionCreateOperations())
}

// createWithOperations реализует один вызов создания с подменяемыми операциями.
// createdPaths накапливает только пути, созданные текущим вызовом: cleanup никогда
// не трогает чужие файлы, даже с похожими именами.
func createWithOperations(source writer.Snapshot, options CreateOptions, operations createOperations) (backup Backup, err error) {
	if err := validateCreateOperations(operations); err != nil {
		return Backup{}, err
	}
	// Недопустимые enum-значения отклоняются до создания файлов: metadata обязана
	// описывать реальную операцию, а не молча принимать опечатку ключа.
	if !validOperation(options.Operation) {
		return Backup{}, fmt.Errorf("недопустимая operation %q", options.Operation)
	}
	if !validSourceVerification(options.SourceVerification) {
		return Backup{}, fmt.Errorf("недопустимая source verification %q", options.SourceVerification)
	}

	// Определяем backup directory: absolute clean path; по умолчанию — каталог target.
	directory, err := resolveDirectory(options.Directory, source.Path, operations)
	if err != nil {
		return Backup{}, err
	}

	// Metadata владельца и mode читаются с source через Lstat, проверяя regular file.
	// Snapshot уже содержит data, поэтому source не перечитывается.
	sourceInfo, err := operations.lstat(source.Path)
	if err != nil {
		return Backup{}, fmt.Errorf("прочитать metadata source %s: %w", source.Path, err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return Backup{}, fmt.Errorf("%w: source %s не является regular file", ErrInvalidBackup, source.Path)
	}
	stat, ok := sourceInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return Backup{}, fmt.Errorf("прочитать Stat_t source %s", source.Path)
	}

	prefix := options.Prefix
	if prefix == "" {
		prefix = DefaultBackupPrefix
	}
	// Prefix становится частью имени файла внутри backup directory: разделители
	// пути и специальные имена привели бы к записи за пределы каталога, поэтому
	// небезопасный prefix отклоняется до создания файлов.
	if strings.ContainsAny(prefix, `/\`) || prefix == "." || prefix == ".." || strings.Contains(prefix, "\x00") {
		return Backup{}, fmt.Errorf("недопустимый backup prefix %q", prefix)
	}
	moment := options.Now
	if moment.IsZero() {
		moment = time.Now().UTC()
	}
	backupID := formatBackupID(moment)
	createdAt := moment.UTC()

	backupHash := sha256.Sum256(source.Data)
	metadata := Metadata{
		SchemaVersion:      MetadataSchemaVersion,
		BackupID:           backupID,
		CreatedAt:          createdAt.Format(time.RFC3339Nano),
		SourcePath:         source.Path,
		SourceSHA256:       hex.EncodeToString(source.SHA256[:]),
		BackupSHA256:       hex.EncodeToString(backupHash[:]),
		UID:                stat.Uid,
		GID:                stat.Gid,
		Mode:               uint32(sourceInfo.Mode().Perm()),
		Operation:          options.Operation,
		ProgramVersion:     ProgramVersion,
		SourceVerification: options.SourceVerification,
	}

	// createdPaths ограничивает cleanup путями текущего вызова.
	createdPaths := make([]string, 0, 2)
	defer func() {
		if err == nil {
			return
		}
		// Ошибка cleanup присоединяется через errors.Join: первичная причина
		// остаётся доступной через errors.Is и никогда не заменяется.
		if cleanupErr := removeCreated(operations.remove, createdPaths); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()

	dataPath, sidecarPath, dataFile, sidecarFile, err := openBackupPair(directory, prefix, backupID, operations)
	if err != nil {
		return Backup{}, err
	}
	createdPaths = append(createdPaths, dataPath, sidecarPath)
	// После выбора пары оба файла закрываются при любом исходе: при ошибке
	// запись прекращается, а cleanup удаляет пути независимо от состояния fd.
	defer func() {
		_ = dataFile.Close()
		_ = sidecarFile.Close()
	}()

	// Порядок data → Sync → Close обязателен: backup не считается записанным без
	// fsync, иначе сбой питания после rename-less создания оставит пустой файл.
	if err := writeAll(dataFile, source.Data); err != nil {
		return Backup{}, fmt.Errorf("записать data backup %s: %w", dataPath, err)
	}
	if err := dataFile.Sync(); err != nil {
		return Backup{}, fmt.Errorf("синхронизировать data backup %s: %w", dataPath, err)
	}
	if err := dataFile.Close(); err != nil {
		return Backup{}, fmt.Errorf("закрыть data backup %s: %w", dataPath, err)
	}

	sidecar, err := json.Marshal(metadata)
	if err != nil {
		return Backup{}, fmt.Errorf("сериализовать metadata backup %s: %w", backupID, err)
	}
	// Trailing newline делает sidecar текстовым документом по Unix-конвенции и
	// упрощает append-only анализ журналами без нарушения strict parser.
	sidecar = append(sidecar, '\n')
	if err := writeAll(sidecarFile, sidecar); err != nil {
		return Backup{}, fmt.Errorf("записать sidecar %s: %w", sidecarPath, err)
	}
	if err := sidecarFile.Sync(); err != nil {
		return Backup{}, fmt.Errorf("синхронизировать sidecar %s: %w", sidecarPath, err)
	}
	if err := sidecarFile.Close(); err != nil {
		return Backup{}, fmt.Errorf("закрыть sidecar %s: %w", sidecarPath, err)
	}

	// Read-back обязателен: любое несовпадение хешей, ID или путей означает
	// повреждение или подмену и приводит к ErrInvalidBackup с cleanup обоих файлов.
	verified, err := readVerifiedBackup(operations.readSnapshot, source, dataPath, sidecarPath)
	if err != nil {
		return Backup{}, err
	}
	return verified, nil
}

// resolveDirectory приводит backup directory к абсолютному очищенному пути.
// Отсутствующий каталог создаётся как real directory 0700; существующий финальный
// symlink отклоняется, чтобы backup не попал по небезопасному пути. Пустой
// directory означает каталог canonical target.
func resolveDirectory(directory, sourcePath string, operations createOperations) (string, error) {
	if directory == "" {
		directory = filepath.Dir(sourcePath)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("получить абсолютный путь backup directory: %w", err)
	}
	directory = filepath.Clean(absolute)

	info, err := operations.lstat(directory)
	switch {
	case err == nil:
		// Существующий каталог обязан быть real directory: Lstat не следует по
		// symlink, поэтому symlink на каталог виден здесь как ModeSymlink.
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: backup directory %s является symlink", ErrInvalidBackup, directory)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%w: backup directory %s не является каталогом", ErrInvalidBackup, directory)
		}
	case errors.Is(err, os.ErrNotExist):
		// 0700: backup-файлы не должны быть доступны посторонним через каталог.
		if err := operations.mkdirAll(directory, 0o700); err != nil {
			return "", fmt.Errorf("создать backup directory %s: %w", directory, err)
		}
		info, err = operations.lstat(directory)
		if err != nil {
			return "", fmt.Errorf("прочитать созданный backup directory %s: %w", directory, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("%w: backup directory %s не является real directory", ErrInvalidBackup, directory)
		}
	default:
		return "", fmt.Errorf("прочитать backup directory %s: %w", directory, err)
	}
	return directory, nil
}

// openBackupPair выбирает свободную пару имён для base ID и открывает оба файла
// эксклюзивно. Сначала data O_EXCL, затем sidecar O_EXCL: если sidecar занят,
// data текущей попытки закрывается и удаляется, а цикл переходит к следующему
// суффиксу. EEXIST разрешает retry; EISDIR/ELOOP означают небезопасную подмену
// и возвращаются немедленно без перехода.
func openBackupPair(directory, prefix, backupID string, operations createOperations) (dataPath, sidecarPath string, dataFile, sidecarFile backupFile, err error) {
	for suffix := 0; suffix < maxCollisionAttempts; suffix++ {
		dataPath, sidecarPath = backupPairPaths(directory, prefix, backupID, suffix)

		dataFile, err = operations.openExclusive(dataPath)
		if err != nil {
			if errors.Is(err, unix.EEXIST) {
				// EEXIST означает коллизию, но symlink с O_EXCL тоже даёт EEXIST:
				// Lstat различает занятый файл и небезопасную подмену, которая
				// обязана быть отклонена немедленно, без перехода на suffix.
				if symlinkErr := rejectSymlinkPath(operations, dataPath, err); symlinkErr != nil {
					return "", "", nil, nil, symlinkErr
				}
				continue
			}
			return "", "", nil, nil, fmt.Errorf("создать data backup %s: %w", dataPath, err)
		}

		sidecarFile, err = operations.openExclusive(sidecarPath)
		if err != nil {
			// Data текущей попытки уже создан: закрываем и удаляем его при любом
			// исходе, чтобы успешный retry не оставлял осиротевших файлов base
			// имени, а немедленный возврат не оставлял мусор от текущей попытки.
			if closeErr := dataFile.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("закрыть data backup %s: %w", dataPath, closeErr))
			}
			if removeErr := operations.remove(dataPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return "", "", nil, nil, fmt.Errorf("удалить data backup %s после сбоя sidecar: %w", dataPath, removeErr)
			}
			if errors.Is(err, unix.EEXIST) {
				// Как и для data: symlink занимает имя с той же ошибкой EEXIST,
				// но подмена небезопасна и отклоняется без перехода на suffix.
				if symlinkErr := rejectSymlinkPath(operations, sidecarPath, err); symlinkErr != nil {
					return "", "", nil, nil, symlinkErr
				}
				continue
			}
			return "", "", nil, nil, fmt.Errorf("создать sidecar %s: %w", sidecarPath, err)
		}
		return dataPath, sidecarPath, dataFile, sidecarFile, nil
	}
	return "", "", nil, nil, fmt.Errorf("не удалось выбрать свободное имя backup для %s за %d попыток", backupID, maxCollisionAttempts)
}

// rejectSymlinkPath проверяет путь, занятый с ошибкой EEXIST: если Lstat показывает
// symlink, занятое имя является небезопасной подменой, а не обычной коллизией, и
// вызов обязан завершиться ошибкой без retry. Обычный файл возвращает nil, и
// коллизия обрабатывается переходом на следующий suffix.
func rejectSymlinkPath(operations createOperations, path string, cause error) error {
	info, err := operations.lstat(path)
	if err != nil {
		// Занятое имя недоступно для проверки: безопаснее отклонить вызов.
		return fmt.Errorf("проверить занятый путь %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: путь %s является symlink", ErrInvalidBackup, path)
	}
	return nil
}

// backupPairPaths вычисляет имена пары для суффикса: base при suffix 0, иначе
// "<prefix>.<id>.<suffix>"; sidecar всегда "<data>.json", что фиксирует парность.
func backupPairPaths(directory, prefix, backupID string, suffix int) (dataPath, sidecarPath string) {
	dataName := prefix + "." + backupID
	if suffix > 0 {
		dataName += "." + strconv.Itoa(suffix)
	}
	dataPath = filepath.Join(directory, dataName)
	return dataPath, dataPath + ".json"
}

// writeAll записывает весь буфер, превращая короткую запись в io.ErrShortWrite.
func writeAll(file backupFile, data []byte) error {
	written, err := file.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return fmt.Errorf("%w: записано %d из %d bytes", io.ErrShortWrite, written, len(data))
	}
	return nil
}

// removeCreated удаляет только пути, созданные текущим вызовом, в обратном
// порядке: sidecar раньше data. Отсутствующий файл не ошибка — возможен
// частичный cleanup более раннего сбоя.
func removeCreated(remove func(string) error, paths []string) error {
	var cleanupErr error
	for i := len(paths) - 1; i >= 0; i-- {
		if err := remove(paths[i]); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("удалить %s: %w", paths[i], err))
		}
	}
	return cleanupErr
}

// readVerifiedBackup заново открывает data и sidecar через writer.ReadSnapshot
// (что повторно гарантирует regular file и O_NOFOLLOW), требует exact mode 0600
// обоих файлов, парсит metadata и требует совпадения хешей, source hash,
// backup ID и путей. Это единый verify seam создания и открытия: Backup
// возвращается только после его успеха. source с пустым Path пропускает
// source-зависимые проверки для потребителей вроде Open, где исходный snapshot
// отсутствует.
func readVerifiedBackup(readSnapshot func(string) (writer.Snapshot, error), source writer.Snapshot, dataPath, sidecarPath string) (Backup, error) {
	// Exact mode 0600 обоих файлов — часть целостности: ослабленные права
	// делают tampering незаметным, поэтому seam отклоняет их до чтения.
	for _, pairPath := range []string{dataPath, sidecarPath} {
		if err := verifyBackupFileMode(pairPath); err != nil {
			return Backup{}, err
		}
	}
	data, err := readSnapshot(dataPath)
	if err != nil {
		return Backup{}, fmt.Errorf("%w: повторно прочитать data backup %s: %w", ErrInvalidBackup, dataPath, err)
	}
	sidecar, err := readSnapshot(sidecarPath)
	if err != nil {
		return Backup{}, fmt.Errorf("%w: повторно прочитать sidecar %s: %w", ErrInvalidBackup, sidecarPath, err)
	}
	metadata, err := parseMetadata(sidecar.Data)
	if err != nil {
		return Backup{}, err
	}

	// Хеш data backup обязан точно совпадать с записанным в metadata.
	if metadata.BackupSHA256 != hex.EncodeToString(data.SHA256[:]) {
		return Backup{}, fmt.Errorf("%w: SHA-256 data backup %s не совпадает с metadata", ErrInvalidBackup, dataPath)
	}
	// Sidecar обязан описывать именно эту пару файлов: backup ID совпадает с
	// ядром имени data-файла, а не произвольным содержимым JSON.
	id, _, ok := parseBackupFilenameFromName(filepath.Base(data.Path))
	if !ok || id != metadata.BackupID {
		return Backup{}, fmt.Errorf("%w: backup_id не соответствует имени data %s", ErrInvalidBackup, data.Path)
	}
	if source.Path != "" {
		if metadata.SourcePath != source.Path {
			return Backup{}, fmt.Errorf("%w: source_path metadata не совпадает с %s", ErrInvalidBackup, source.Path)
		}
		if metadata.SourceSHA256 != hex.EncodeToString(source.SHA256[:]) {
			return Backup{}, fmt.Errorf("%w: source_sha256 metadata не совпадает с snapshot %s", ErrInvalidBackup, source.Path)
		}
	}
	return Backup{
		ID:           metadata.BackupID,
		Path:         data.Path,
		MetadataPath: sidecar.Path,
		Metadata:     metadata,
		SHA256:       data.SHA256,
	}, nil
}

// parseBackupFilenameFromName извлекает backup ID из имени data-файла с
// опциональным collision-суффиксом, не зная prefix: допускается любой непустой
// префикс без символов пути. Проверка с известным prefix выполняется
// отдельно через parseBackupFilename.
func parseBackupFilenameFromName(name string) (id string, suffix int, ok bool) {
	matches := backupNameRegexp.FindStringSubmatch(name)
	if matches == nil || strings.ContainsAny(matches[1], `/\`) {
		return "", 0, false
	}
	suffix = 0
	if matches[3] != "" {
		parsed, err := strconv.Atoi(matches[3])
		if err != nil {
			return "", 0, false
		}
		suffix = parsed
	}
	return matches[2], suffix, true
}
