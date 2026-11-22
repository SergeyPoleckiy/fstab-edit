// Файл history.go реализует экран истории backup: список копий newest-first,
// diff выбранной копии против текущего target и восстановление target из
// выбранной копии через доменный сервис cliapp.Service.Rollback (safety backup
// → replace → post-verify) — TUI не дублирует логику записи самостоятельно.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/diff"
	"fstab-edit/internal/i18n"
)

// historyModel — список backups newest-first и выбранный элемент. Пустой
// список — штатное состояние (не сбой): Err остаётся nil. Поле confirm
// обозначает модальное подтверждение restore (y/n, default No, ТЗ §15).
type historyModel struct {
	Backups []backup.Backup
	Cursor  int
	Err     error
	confirm bool
}

// loadHistory читает список backups через seam; сортировку newest-first
// выполняет backup.List, поэтому здесь только проброс результата.
func loadHistory(m *model) historyModel {
	out := historyModel{}
	backups, err := m.deps().ListBackups(m.opts.Target, m.opts.Backup.Prefix, m.opts.Backup.Dir)
	if err != nil {
		out.Err = err
		return out
	}
	out.Backups = backups
	return out
}

// formatCreated приводит RFC3339Nano timestamp метаданных к читаемому виду
// «2006-01-02 15:04:05» в UTC (так же, как и в метаданных); неразбираемое
// значение показывается как есть — fallback безопаснее пустой ячейки.
func formatCreated(raw string) string {
	created, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return created.UTC().Format("2006-01-02 15:04:05")
}

// Selected возвращает копию под курсором. Вызывается только при непустом
// списке: Update не допускает действий 'r'/'d' на пустой истории.
func (h historyModel) Selected() backup.Backup { return h.Backups[h.Cursor] }

// restoreResult — итог восстановления выбранной копии для показа в TUI.
// Err nil означает успех; текст ошибки — из каталога по ExitError.MessageKey
// (русский fallback Message используется, только если ключа нет).
type restoreResult struct {
	Result cliapp.RollbackResult
	Err    *cliapp.ExitError
}

// restoreSelected восстанавливает target из выбранной копии через доменный
// сервис: selection уже сделана списком, поэтому RestoreID — точный ID копии;
// safety backup, replace и post-verify выполняет cliapp.Service.Rollback без
// отдельной небезопасной реализации записи. После успеха target на диске
// содержит другие байты — сессия перечитывается через reloadFromDisk.
func restoreSelected(ctx context.Context, m *model, b backup.Backup) restoreResult {
	service := cliapp.NewService(cliappDepsFromTui(m), m.opts.Runner)
	result, exitErr := service.Rollback(ctx, m.opts.Target, cliapp.RollbackRequest{
		RestoreID: b.ID,
		Directory: m.opts.Backup.Dir,
		Prefix:    m.opts.Backup.Prefix,
	})
	out := restoreResult{Result: result, Err: exitErr}
	if exitErr == nil {
		// После restore на диске другие байты: перечитываем сессию из target.
		// Ошибка перечитывания уже зафиксирована в статусе reloadFromDisk.
		_ = m.reloadFromDisk()
	}
	return out
}

// cliappDepsFromTui адаптирует tui.Deps к cliapp.Dependencies. Создание и
// выбор backup идут напрямую через backup.Create/backup.Select: у TUI нет
// этих seams в Deps (он их не подменяет), а остальные границы совпадают 1:1.
func cliappDepsFromTui(m *model) cliapp.Dependencies {
	d := m.deps()
	return cliapp.Dependencies{
		ReadSnapshot:       d.ReadSnapshot,
		ExecuteTransaction: d.ExecuteTransaction,
		CreateBackup:       backup.Create,
		SelectBackup:       backup.Select,
		OpenBackup:         d.OpenBackup,
		Replace:            d.Replace,
		VerifyTarget:       d.VerifyTarget,
	}
}

// updateHistory обрабатывает клавиши экрана истории. Навигация зажата в
// границах списка; 'd' открывает diff target против выбранной копии; 'r'
// включает модальное подтверждение restore (default No: любая клавиша кроме
// подтверждения отменяет); Esc возвращает на таблицу (глобальное правило).
func (m model) updateHistory(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.history.Err != nil {
		if msg.String() == "esc" {
			m.screen = ScreenTable
		}
		return m, nil
	}
	if m.history.confirm {
		if i18n.AcceptYes(m.opts.Lang, msg.String()) {
			m.history.confirm = false
			result := restoreSelected(context.Background(), &m, m.history.Selected())
			if result.Err != nil {
				// Перевод по MessageKey; без ключа — русский fallback Error().
				if result.Err.MessageKey != "" {
					m.status = m.theme.MarkError(
						i18n.Tf(m.opts.Lang, result.Err.MessageKey, result.Err.MessageArgs...))
				} else {
					m.status = m.theme.MarkError(result.Err.Error())
				}
			} else {
				m.status = m.theme.Info.Render(Tr(m.opts.Lang, "history_restore_ok"))
			}
			m.screen = ScreenTable
			return m, nil
		}
		// Default No: любая другая клавиша (включая Esc) отменяет restore.
		m.history.confirm = false
		return m, nil
	}
	if len(m.history.Backups) == 0 {
		if msg.String() == "esc" {
			m.screen = ScreenTable
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.screen = ScreenTable
	case "up", "k":
		if m.history.Cursor > 0 {
			m.history.Cursor--
		}
	case "down", "j":
		if m.history.Cursor < len(m.history.Backups)-1 {
			m.history.Cursor++
		}
	case "d":
		// Diff текущего target против выбранной копии: оба снапшота читаются
		// через seam; сбой чтения уходит в статус, экран не меняется.
		current, err := m.deps().ReadSnapshot(m.opts.Target)
		if err != nil {
			m.status = m.theme.MarkError(Trf(m.opts.Lang, "history_read_failed", err.Error()))
			break
		}
		selected, err := m.deps().ReadSnapshot(m.history.Selected().Path)
		if err != nil {
			m.status = m.theme.MarkError(Trf(m.opts.Lang, "history_read_failed", err.Error()))
			break
		}
		m.diff = newDiffModel(diff.Unified("<current>", "<backup>", current.Data, selected.Data))
		m.screen = ScreenDiff
	case "r":
		m.history.confirm = true
	}
	return m, nil
}

// viewHistory рендерит экран истории: таблица ID │ operation │ created (коды
// операций не локализуются, как и в экране журнала), курсорная строка
// подсвечена через theme.Selected; ошибка загрузки — MarkError, пустой список
// — muted-подпись. Подтверждение restore рендерится отдельной строкой.
func (m model) viewHistory() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(Tr(m.opts.Lang, "history_title")) + "\n\n")
	switch {
	case m.history.Err != nil:
		b.WriteString(m.theme.MarkError(Trf(m.opts.Lang, "history_error", m.history.Err.Error())) + "\n")
	case len(m.history.Backups) == 0:
		b.WriteString(m.theme.Muted.Render(Tr(m.opts.Lang, "history_empty")) + "\n")
	default:
		b.WriteString(m.theme.Header.Render(fmt.Sprintf("%-34s %-18s %s",
			"ID", "operation", "created")) + "\n")
		for i, item := range m.history.Backups {
			row := fmt.Sprintf("%-34s %-18s %s",
				item.ID, item.Metadata.Operation, formatCreated(item.Metadata.CreatedAt))
			if i == m.history.Cursor {
				row = m.theme.Selected.Render(row)
			}
			b.WriteString(row + "\n")
		}
	}
	if m.history.confirm {
		b.WriteString("\n" + m.theme.Warning.Render(
			Trf(m.opts.Lang, "history_confirm_restore", m.history.Selected().ID)) + "\n")
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_history")) + "\n")
	return b.String()
}
