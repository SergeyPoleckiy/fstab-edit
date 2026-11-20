// Package logger пишет события fstab-edit в JSON Lines-журнал (ТЗ §14):
// одно событие — одна строка, timestamp ISO 8601 (RFC 3339), коды событий и
// operations не локализуются. Перед mutation CLI обязан открыть журнал:
// недоступность журнала до точки изменения блокирует мутацию. Сбой записи
// после replace не должен мешать обязательному rollback — поэтому Log
// возвращает ошибку, которую вызывающий код вправе проигнорировать после
// успешной записи target.
package logger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Severity — уровень события; значения фиксированы и не локализуются.
type Severity string

const (
	SeverityDebug Severity = "debug"
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Operation — стабильный код операции для журнала и backup metadata;
// не локализуется.
type Operation string

const (
	OperationAdd       Operation = "add"
	OperationEdit      Operation = "edit"
	OperationDelete    Operation = "delete"
	OperationComment   Operation = "comment"
	OperationUncomment Operation = "uncomment"
	OperationBackup    Operation = "backup"
	OperationRollback  Operation = "rollback"
)

// Result — исход операции: success | error | cancelled.
const (
	ResultSuccess   = "success"
	ResultError     = "error"
	ResultCancelled = "cancelled"
)

// Event — одно событие журнала. OldValue/NewValue уже прошли Redact:
// newlines экранированы, секреты замаскированы. JSON-теги фиксированы
// schema журнала и не локализуются.
type Event struct {
	Severity     Severity  `json:"severity"`
	Timestamp    time.Time `json:"timestamp"`
	RealUID      int       `json:"real_uid"`
	EffectiveUID int       `json:"effective_uid"`
	SudoUser     string    `json:"sudo_user,omitempty"`
	SudoUID      string    `json:"sudo_uid,omitempty"`
	Operation    Operation `json:"operation"`
	Target       string    `json:"target"`
	BackupID     string    `json:"backup_id,omitempty"`
	Result       string    `json:"result"`
	ErrorCode    int       `json:"error_code,omitempty"`
	DurationMs   int64     `json:"duration_ms,omitempty"`
	OldValue     string    `json:"old_value,omitempty"`
	NewValue     string    `json:"new_value,omitempty"`
}

// Options — параметры Open; Now подменяется тестами.
type Options struct {
	File         string
	Level        Severity
	MaxSizeMB    int
	Keep         int
	RealUID      int
	EffectiveUID int
	SudoUser     string
	SudoUID      string
	Now          func() time.Time
}

// Logger — append-only журнал с rotation по размеру. Методы безопасны для
// одного goroutine-писателя; CLI пишет последовательно из одной команды.
// Поля uid/sudo из Options — контекст процесса: Log подставляет их в событие,
// когда событие не несёт собственных значений.
type Logger struct {
	file         *os.File
	level        Severity
	max          int64
	keep         int
	now          func() time.Time
	realUID      int
	effectiveUID int
	sudoUser     string
	sudoUID      string
	closed       bool
	mu           sync.Mutex
}

// Open открывает (создаёт) журнал с правами 0600 и выполняет rotation,
// если текущий размер превышает MaxSizeMB. Ошибка открытия возвращается
// вызывающему: недоступный журнал до mutation блокирует изменение target.
func Open(opts Options) (*Logger, error) {
	if opts.File == "" {
		return nil, errors.New("путь журнала пуст")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	max := int64(opts.MaxSizeMB) * 1024 * 1024
	if err := rotateIfNeeded(opts.File, max, opts.Keep); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(opts.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("открыть журнал %s: %w", opts.File, err)
	}
	return &Logger{
		file: f, level: opts.Level, max: max, keep: opts.Keep, now: opts.Now,
		realUID: opts.RealUID, effectiveUID: opts.EffectiveUID,
		sudoUser: opts.SudoUser, sudoUID: opts.SudoUID,
	}, nil
}

// Log сериализует событие одной строкой JSON и дописывает в файл. События
// ниже уровня журнала отбрасываются без ошибки. Ошибка записи возвращается
// вызывающему — после успешного replace он вправе её проигнорировать,
// но до точки изменения она фатальна (журнал недоступен → target не менять).
func (l *Logger) Log(e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("журнал закрыт")
	}
	if !l.enabled(e.Severity) {
		return nil
	}
	e.Timestamp = l.now().UTC()
	// Контекст процесса (uid/sudo) подставляем, только если событие не
	// несёт собственных значений — конкретика события важнее Options.
	if e.RealUID == 0 && e.EffectiveUID == 0 {
		e.RealUID = l.realUID
		e.EffectiveUID = l.effectiveUID
	}
	if e.SudoUser == "" {
		e.SudoUser = l.sudoUser
	}
	if e.SudoUID == "" {
		e.SudoUID = l.sudoUID
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("сериализовать событие: %w", err)
	}
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("записать событие: %w", err)
	}
	return nil
}

// Close закрывает файл; повторный вызов безопасен (ТЗ: штатное завершение
// освобождает ресурсы).
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.file.Close()
}

// enabled сообщает, проходит ли severity установленный уровень журнала.
func (l *Logger) enabled(s Severity) bool {
	order := map[Severity]int{SeverityDebug: 0, SeverityInfo: 1, SeverityWarn: 2, SeverityError: 3}
	return order[s] >= order[l.level]
}

// rotateIfNeeded сдвигает архивы file.N → file.N+1 (старшие удаляются сверх
// Keep) и переименовывает текущий файл в file.1, если он достиг max байт.
// Rotation не выполняется, когда max <= 0 или файла нет.
func rotateIfNeeded(path string, max int64, keep int) error {
	if max <= 0 || keep <= 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("проверить журнал %s: %w", path, err)
	}
	if info.Size() < max {
		return nil
	}
	// Удаляем самый старый архив, освобождая номер keep.
	oldest := path + "." + fmt.Sprint(keep)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("удалить архив %s: %w", oldest, err)
	}
	for i := keep - 1; i >= 1; i-- {
		from := path + "." + fmt.Sprint(i)
		to := path + "." + fmt.Sprint(i+1)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, to); err != nil {
				return fmt.Errorf("rotate %s → %s: %w", from, to, err)
			}
		}
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("rotate %s → %s: %w", path, path+".1", err)
	}
	return nil
}
