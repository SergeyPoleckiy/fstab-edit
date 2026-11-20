// Package backup реализует создание резервных копий fstab с sidecar-метаданными.
//
// Пакет отвечает только за backup: он никогда не изменяет target, не выполняет
// lock, atomic replace, rollback и не выбирает копии для восстановления. Вход —
// готовый writer.Snapshot, выход — созданная на диске пара data + sidecar JSON,
// проверенная чтением и SHA-256 до возврата.
//
// Основные инварианты:
//   - backup ID всегда имеет ядро YYYY-MM-DD_HH-MM-SS.nnnnnnnnnZ в UTC;
//   - data и sidecar создаются только через O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC
//     с начальным mode 0600, symlink не принимается ни на одном уровне;
//   - backup считается готовым только после записи, fsync обоих файлов,
//     повторного чтения и совпадения SHA-256 data с metadata;
//   - backup гарантирует восстановление байтов, но metadata не заявляет
//     работоспособность повреждённого исходника (source_verification).
package backup

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MetadataSchemaVersion фиксирует текущую стабильную версию sidecar-спецификации.
// Обратно несовместимые изменения обязаны увеличить версию; parser принимает
// ровно это значение.
const MetadataSchemaVersion = 1

// DefaultBackupPrefix — имя backup-файлов по умолчанию рядом с target.
const DefaultBackupPrefix = "fstab.bak"

// DefaultKeepBackups — сколько newest-копий сохраняет ротация по умолчанию.
// keep <= 0 нормализуется в эту константу перед удалением кандидатов.
const DefaultKeepBackups = 10

// ProgramVersion — версия программы, записываемая в metadata. Значение "dev"
// используется до подключения версионирования сборки; release-сборка заменит его.
const ProgramVersion = "dev"

// Operation описывает доменную операцию, перед которой создан backup.
// Значения не локализуются и используются как стабильные machine-readable коды.
type Operation string

// Допустимые значения Operation. Список исчерпывающий: parseMetadata и Create
// отклоняют любое другое значение, чтобы metadata нельзя было подделать чужим кодом.
const (
	// OperationBeforeAdd — backup перед добавлением новой записи.
	OperationBeforeAdd Operation = "before-add"
	// OperationBeforeEdit — backup перед изменением существующей записи.
	OperationBeforeEdit Operation = "before-edit"
	// OperationBeforeDelete — backup перед удалением записи.
	OperationBeforeDelete Operation = "before-delete"
	// OperationBeforeComment — backup перед комментированием активной записи.
	OperationBeforeComment Operation = "before-comment"
	// OperationBeforeUncomment — backup перед раскомментированием записи.
	OperationBeforeUncomment Operation = "before-uncomment"
	// OperationBeforeRollback — safety copy текущего target перед восстановлением.
	OperationBeforeRollback Operation = "before-rollback"
	// OperationManual — копия, созданная явной командой backup без изменения target.
	OperationManual Operation = "manual"
)

// validOperations — whitelist всех допустимых operation для строгой проверки.
var validOperations = map[Operation]struct{}{
	OperationBeforeAdd:       {},
	OperationBeforeEdit:      {},
	OperationBeforeDelete:    {},
	OperationBeforeComment:   {},
	OperationBeforeUncomment: {},
	OperationBeforeRollback:  {},
	OperationManual:          {},
}

// validOperation сообщает, принадлежит ли значение точному whitelist operation.
func validOperation(operation Operation) bool {
	_, ok := validOperations[operation]
	return ok
}

// SourceVerification фиксирует результат проверки исходного target до backup.
// Backup гарантирует восстановление байтов, но этот признак не должен молча
// объявлять повреждённый исходник работоспособным.
type SourceVerification string

// Допустимые значения SourceVerification. Список исчерпывающий, как у Operation.
const (
	// SourceNotChecked — исходная проверка не выполнялась (ручной backup).
	SourceNotChecked SourceVerification = "not_checked"
	// SourceValid — исходная проверка пройдена успешно.
	SourceValid SourceVerification = "valid"
	// SourceInvalid — исходная проверка выявила ошибки в документе.
	SourceInvalid SourceVerification = "invalid"
	// SourceUnavailable — исходная проверка невозможна в текущей среде.
	SourceUnavailable SourceVerification = "unavailable"
)

// validSourceVerifications — whitelist всех допустимых значений проверки исходника.
var validSourceVerifications = map[SourceVerification]struct{}{
	SourceNotChecked:  {},
	SourceValid:       {},
	SourceInvalid:     {},
	SourceUnavailable: {},
}

// validSourceVerification сообщает, принадлежит ли значение точному whitelist.
func validSourceVerification(verification SourceVerification) bool {
	_, ok := validSourceVerifications[verification]
	return ok
}

// Metadata — стабильная sidecar-спецификация v1. JSON keys не локализуются
// и не меняются без новой schema_version.
type Metadata struct {
	// SchemaVersion обязан равняться MetadataSchemaVersion.
	SchemaVersion int `json:"schema_version"`
	// BackupID — ядро имени файла в UTC, без collision-суффикса.
	BackupID string `json:"backup_id"`
	// CreatedAt — RFC3339Nano timestamp в UTC, обозначающий то же время, что BackupID.
	CreatedAt string `json:"created_at"`
	// SourcePath — canonical absolute clean путь target на момент backup.
	SourcePath string `json:"source_path"`
	// SourceSHA256 — lowercase hex SHA-256 source data из переданного snapshot.
	SourceSHA256 string `json:"source_sha256"`
	// BackupSHA256 — lowercase hex SHA-256 записанных backup data.
	BackupSHA256 string `json:"backup_sha256"`
	// UID — владелец source на момент backup.
	UID uint32 `json:"uid"`
	// GID — группа source на момент backup.
	GID uint32 `json:"gid"`
	// Mode — permission bits source (не более 0o7777).
	Mode uint32 `json:"mode"`
	// Operation — доменная операция из whitelist.
	Operation Operation `json:"operation"`
	// ProgramVersion — версия программы, создавшей backup.
	ProgramVersion string `json:"program_version"`
	// SourceVerification — результат исходной проверки target.
	SourceVerification SourceVerification `json:"source_verification"`
}

// Backup — проверенная резервная копия, готовая к использованию. Backup
// возвращается только после write, fsync, повторного чтения и сверки хешей.
type Backup struct {
	// ID — backup ID из metadata, ядро имени без collision-суффикса.
	ID string
	// Path — абсолютный путь data-файла backup.
	Path string
	// MetadataPath — абсолютный путь sidecar JSON, всегда Path + ".json".
	MetadataPath string
	// Metadata — распарсенные и проверенные sidecar-метаданные.
	Metadata Metadata
	// SHA256 — фактический SHA-256 data backup после read-back.
	SHA256 [32]byte
}

// CreateOptions задаёт параметры одного вызова Create.
type CreateOptions struct {
	// Directory переопределяет каталог backup; пустое значение — каталог target.
	Directory string
	// Prefix имени файлов; пустое значение — DefaultBackupPrefix.
	Prefix string
	// Operation — обязательная доменная операция из whitelist.
	Operation Operation
	// SourceVerification — обязательный результат исходной проверки из whitelist.
	SourceVerification SourceVerification
	// Now задаёт детерминированное время; zero time означает time.Now().UTC().
	Now time.Time
}

var (
	// ErrInvalidBackup обозначает недопустимый или повреждённый backup:
	// нарушение schema, несовпадение хешей, symlink-подмена или неверные пути.
	ErrInvalidBackup = errors.New("недопустимый или повреждённый backup")
	// ErrBackupNotFound обозначает отсутствие backup с запрошенным ID.
	// Потребители появятся в Task 2 вместе с Select.
	ErrBackupNotFound = errors.New("backup не найден")
	// ErrBackupAmbiguous обозначает несколько файлов одного backup ID.
	ErrBackupAmbiguous = errors.New("backup ID соответствует нескольким файлам")
)

// backupIDLayout — Go layout, точно воспроизводящий ядро backup ID.
// Девять нулей после точки фиксируют nanoseconds с дополнением нулями.
const backupIDLayout = "2006-01-02_15-04-05.000000000Z"

// backupIDRegexp строго описывает ядро backup ID: календарная дата, время и
// ровно девять цифр nanoseconds с литеральным Z. Regexp сам по себе допускает
// несуществующую календарную дату, поэтому parseMetadata повторно парсит ID
// layout-ом и сверяет round-trip.
var backupIDRegexp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}\.\d{9}Z$`)

// backupNameRegexp выделяет backup ID и опциональный числовой collision-суффикс
// из имени data-файла. Первый сегмент (prefix) намеренно не ограничен: строгость
// обеспечивается вызовом через parseBackupFilename с известным prefix.
var backupNameRegexp = regexp.MustCompile(`^(.+)\.(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}\.\d{9}Z)(?:\.([0-9]+))?$`)

// formatBackupID форматирует момент времени в backup ID по контракту. Время
// нормализуется в UTC, поэтому не-UTC time.Time допустим и не искажает ID;
// литеральный Z в layout гарантирует UTC-формат независимо от входной локации.
func formatBackupID(moment time.Time) string {
	return moment.UTC().Format(backupIDLayout)
}

// parseBackupIDStrict проверяет backup ID по regexp и повторно парсит его тем же
// layout, что использует formatBackupID. Round-trip через Format отсекает
// несуществующие календарные даты, которые пропускает regexp (например, месяц 13),
// и возвращает UTC-время, обозначенное ID.
func parseBackupIDStrict(id string) (time.Time, error) {
	if !backupIDRegexp.MatchString(id) {
		return time.Time{}, fmt.Errorf("%w: backup_id %q не соответствует формату", ErrInvalidBackup, id)
	}
	parsed, err := time.Parse(backupIDLayout, id)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: backup_id %q не является календарной датой: %v", ErrInvalidBackup, id, err)
	}
	if parsed.Format(backupIDLayout) != id {
		return time.Time{}, fmt.Errorf("%w: backup_id %q не воспроизводится форматом", ErrInvalidBackup, id)
	}
	return parsed, nil
}

// parseBackupFilename разбирает имя data-файла строго как "<prefix>.<id>" или
// "<prefix>.<id>.<suffix>". Произвольные похожие имена отклоняются, чтобы чужие
// файлы не принимались за backup; sidecar-имена (с ".json") тоже отклоняются.
// suffix 0 означает base-имя без суффикса.
func parseBackupFilename(prefix, name string) (id string, suffix int, ok bool) {
	if prefix == "" || !strings.HasPrefix(name, prefix+".") {
		return "", 0, false
	}
	matches := backupNameRegexp.FindStringSubmatch(name)
	if matches == nil || matches[1] != prefix {
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

// parseMetadata выполняет strict-декодирование и полную валидацию sidecar v1.
// Decoder запрещает unknown fields и trailing JSON value, затем каждое поле
// проверяется по контракту. Ошибки оборачивают ErrInvalidBackup и не включают
// содержимое backup, чтобы повреждённые данные не утекали в диагностику.
func parseMetadata(data []byte) (Metadata, error) {
	var metadata Metadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	// DisallowUnknownFields защищает schema v1 от молчаливого расширения:
	// опечатка ключа должна быть ошибкой, а не пропущенным полем.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, fmt.Errorf("%w: декодировать metadata: %v", ErrInvalidBackup, err)
	}
	// Trailing JSON value запрещён: sidecar — ровно один документ.
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == nil {
		return Metadata{}, fmt.Errorf("%w: лишний JSON value после metadata", ErrInvalidBackup)
	} else if !errors.Is(err, io.EOF) {
		return Metadata{}, fmt.Errorf("%w: декодировать metadata: %v", ErrInvalidBackup, err)
	}
	return validateMetadata(metadata)
}

// validateMetadata проверяет распарсенные поля metadata по всем инвариантам
// schema v1. Сообщения намеренно содержат только имя поля, не его значение.
func validateMetadata(metadata Metadata) (Metadata, error) {
	if metadata.SchemaVersion != MetadataSchemaVersion {
		return Metadata{}, fmt.Errorf("%w: неподдерживаемая schema_version %d", ErrInvalidBackup, metadata.SchemaVersion)
	}

	// backup_id должен быть валидным UTC-временем в формате контракта.
	idTime, err := parseBackupIDStrict(metadata.BackupID)
	if err != nil {
		return Metadata{}, err
	}

	// created_at парсится RFC3339Nano и обязан быть в UTC; "+00:00" отклоняется,
	// потому что Location() FixedZone не является time.UTC по контракту.
	createdAt, err := time.Parse(time.RFC3339Nano, metadata.CreatedAt)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: created_at не является RFC3339Nano: %v", ErrInvalidBackup, err)
	}
	if createdAt.Location() != time.UTC {
		return Metadata{}, fmt.Errorf("%w: created_at не в UTC", ErrInvalidBackup)
	}
	// created_at и backup_id обязаны обозначать одно и то же UTC-время,
	// включая nanoseconds: Equal сравнивает инстанты с полной точностью.
	if !createdAt.Equal(idTime) {
		return Metadata{}, fmt.Errorf("%w: created_at не совпадает с backup_id", ErrInvalidBackup)
	}

	if !filepath.IsAbs(metadata.SourcePath) || filepath.Clean(metadata.SourcePath) != metadata.SourcePath {
		return Metadata{}, fmt.Errorf("%w: source_path не является абсолютным очищенным путём", ErrInvalidBackup)
	}
	if err := validateHexHash("source_sha256", metadata.SourceSHA256); err != nil {
		return Metadata{}, err
	}
	if err := validateHexHash("backup_sha256", metadata.BackupSHA256); err != nil {
		return Metadata{}, err
	}

	// UID/GID уже ограничены uint32 декодером; mode ограничен явно по контракту.
	if metadata.Mode > 0o7777 {
		return Metadata{}, fmt.Errorf("%w: mode превышает 07777", ErrInvalidBackup)
	}
	if !validOperation(metadata.Operation) {
		return Metadata{}, fmt.Errorf("%w: неизвестная operation", ErrInvalidBackup)
	}
	if !validSourceVerification(metadata.SourceVerification) {
		return Metadata{}, fmt.Errorf("%w: неизвестная source verification", ErrInvalidBackup)
	}
	return metadata, nil
}

// validateHexHash требует ровно 64 lowercase hex-символов, декодируемых в 32 байта.
func validateHexHash(field, value string) error {
	if len(value) != 64 {
		return fmt.Errorf("%w: %s должен содержать 64 hex-символа", ErrInvalidBackup, field)
	}
	for _, char := range value {
		if !isLowerHex(char) {
			return fmt.Errorf("%w: %s должен быть lowercase hex", ErrInvalidBackup, field)
		}
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%w: %s не декодируется как hex: %v", ErrInvalidBackup, field, err)
	}
	return nil
}

// isLowerHex сообщает, является ли руна символом 0-9 или a-f.
func isLowerHex(char rune) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')
}
