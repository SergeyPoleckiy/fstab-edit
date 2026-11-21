package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/i18n"
	"fstab-edit/internal/logger"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/transaction"
)

// saveOutcome описывает итог одной попытки сохранения для экрана
// ScreenSaveResult. Err nil означает успех; Conflict выставляется отдельно,
// чтобы экран мог предложить перезагрузку документа (Task 6).
type saveOutcome struct {
	Result    transaction.TransactionResult
	Err       *transaction.TransactionError // nil при успехе
	Conflict  bool                          // base изменился: предложить перезагрузку
	LogOpened bool
}

// save выполняет одну транзакцию сохранения текущего документа. Порядок
// жёстко фиксирован ТЗ §9.2 и §14: журнал открывается до точки изменения и
// его недоступность отменяет сохранение целиком; конфликт версии возвращается
// без перезаписи target; сбой записи события журнала после replace игнорируется,
// потому что первичная ошибка и обязательный rollback важнее.
func save(ctx context.Context, m *model) saveOutcome {
	out := saveOutcome{}
	if m.session == nil {
		out.Err = &transaction.TransactionError{
			Kind: transaction.FailureValidation,
			Err:  errors.New("сессия недоступна: документ не загружен"),
		}
		return out
	}
	deps := m.deps()

	// Журнал открывается до любой записи: его недоступность блокирует
	// сохранение до точки изменения (ТЗ §14).
	logFile, err := deps.OpenLog()
	if err != nil {
		out.Err = &transaction.TransactionError{
			Kind: transaction.FailureIO,
			Err:  fmt.Errorf("открыть журнал перед сохранением: %w", err),
		}
		return out
	}
	out.LogOpened = true
	// Ошибка Close после записи события не влияет на итог сохранения.
	defer func() { _ = logFile.Close() }()

	candidate := m.session.candidate(m.document)
	plan := transaction.Plan{
		TargetPath:      m.opts.Target,
		Candidate:       candidate,
		Operation:       backup.OperationBeforeEdit,
		BackupDirectory: m.opts.Backup.Dir,
		BackupPrefix:    m.opts.Backup.Prefix,
		RotationKeep:    m.opts.Backup.Keep,
	}
	txOptions := transaction.Options{
		Runner:          m.opts.Runner,
		BackupDirectory: m.opts.Backup.Dir,
		BackupPrefix:    m.opts.Backup.Prefix,
		RotationKeep:    m.opts.Backup.Keep,
	}
	start := time.Now()
	result, txErr := deps.ExecuteTransaction(ctx, plan, txOptions)
	out.Result = result
	if txErr != nil {
		var failure *transaction.TransactionError
		if errors.As(txErr, &failure) {
			out.Err = failure
		} else {
			// Неизвестная (не transaction) ошибка: сохраняем как I/O-сбой,
			// чтобы экран результата работал с одним типом.
			out.Err = &transaction.TransactionError{Kind: transaction.FailureIO, Err: txErr}
		}
		out.Conflict = out.Err.Kind == transaction.FailureConflict
	}

	// Событие журнала: ошибка записи игнорируется — она не заменяет
	// первичную ошибку и не мешает обязательному rollback (ТЗ §14).
	severity := logger.SeverityInfo
	resultCode := logger.ResultSuccess
	errorCode := 0
	if out.Err != nil {
		severity = logger.SeverityError
		resultCode = logger.ResultError
		// Коды совпадают с будущими CLI exit codes: конфликт — 4, прочее — 6.
		errorCode = 6
		if out.Conflict {
			errorCode = 4
		}
	}
	_ = logFile.Log(logger.Event{
		Severity: severity, Operation: logger.OperationEdit, Target: m.opts.Target,
		BackupID: result.Backup.ID, Result: resultCode, ErrorCode: errorCode,
		DurationMs: time.Since(start).Milliseconds(),
		OldValue:   logger.Redact(string(m.session.original)),
		NewValue:   logger.Redact(string(candidate)),
	})

	if out.Err == nil {
		// Сохранение успешно: сессия сбрасывается, original обновляется
		// до записанного candidate; undo-стек больше не имеет смысла.
		m.session.original = append([]byte(nil), candidate...)
		m.session.undoStack = m.session.undoStack[:0]
		m.session.dirty = false
	}
	return out
}

// reloadFromDisk перечитывает target в модель, сбрасывая сессию и курсор.
// F5; подтверждение при dirty — на уровне Update (ScreenConfirmReload).
// Task 6 переиспользует этот метод для перезагрузки после конфликта.
func (m *model) reloadFromDisk() {
	deps := m.deps()
	snapshot, err := deps.ReadSnapshot(m.opts.Target)
	if err != nil {
		m.status = m.theme.MarkError(Trf(m.opts.Lang, "load_error", m.opts.Target, err.Error()))
		return
	}
	m.document = parser.Parse(snapshot.Data)
	m.session = &sessionState{original: append([]byte(nil), snapshot.Data...)}
	m.cursor = 0
}

// updateConfirmReload обрабатывает модальный экран подтверждения перезагрузки
// (F5 при dirty): подтверждение перечитывает target, всё остальное (default
// No, ТЗ §15) возвращает на таблицу без изменений.
func (m model) updateConfirmReload(msgKey string) (tea.Model, tea.Cmd) {
	if i18n.AcceptYes(m.opts.Lang, msgKey) {
		m.reloadFromDisk()
		m.status = Tr(m.opts.Lang, "reload_done")
	}
	m.screen = ScreenTable
	return m, nil
}

// updateConfirmQuit обрабатывает модальное подтверждение выхода при
// несохранённых изменениях (ТЗ §9.2): подтверждение завершает программу,
// всё остальное (default No, ТЗ §15) возвращает на таблицу без выхода.
func (m model) updateConfirmQuit(msgKey string) (tea.Model, tea.Cmd) {
	if i18n.AcceptYes(m.opts.Lang, msgKey) {
		return m, tea.Quit
	}
	m.screen = ScreenTable
	return m, nil
}

// viewSaveResult рендерит итог сохранения: успех — backup ID + строка
// проверки; ошибка — описание + состояние rollback. После неуспешной записи
// единственное действие — Esc: «оставить файл» не предлагается (ТЗ §9.2).
func (m model) viewSaveResult() string {
	out := m.lastSave
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(Tr(m.opts.Lang, "save_result_title")) + "\n\n")
	if out.Err == nil {
		if out.Result.Changed {
			b.WriteString(m.theme.Info.Render(
				Trf(m.opts.Lang, "save_success", out.Result.Backup.ID)) + "\n")
			b.WriteString(m.theme.Muted.Render(Tr(m.opts.Lang, "save_verified")) + "\n")
		} else {
			b.WriteString(m.theme.Muted.Render(Tr(m.opts.Lang, "save_noop")) + "\n")
		}
	} else {
		b.WriteString(m.theme.MarkError(out.Err.Error()) + "\n")
		if out.Result.RolledBack {
			b.WriteString(m.theme.Info.Render(Tr(m.opts.Lang, "rolled_back")) + "\n")
		}
		if out.Result.RollbackError != nil {
			b.WriteString(m.theme.MarkError(
				Trf(m.opts.Lang, "rollback_failed", out.Result.RollbackError.Error())) + "\n")
			r := out.Result.Recovery
			if r.Target != "" || r.Backup != "" || r.Metadata != "" {
				b.WriteString(m.theme.Muted.Render(
					Trf(m.opts.Lang, "recovery_paths", r.Target, r.Backup, r.Metadata)) + "\n")
			}
		}
		if out.Conflict {
			b.WriteString(m.theme.Muted.Render(Tr(m.opts.Lang, "save_conflict_hint")) + "\n")
		}
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_save_result")) + "\n")
	return b.String()
}
