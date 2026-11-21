package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/editor"
	"fstab-edit/pkg/models"
)

// updateTable обрабатывает клавиши главной таблицы (скелет Task 1):
// навигация, выход, переключение экранов-заглушек.
func (m model) updateTable(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	entries := m.entryIndexes()
	switch msg.String() {
	case "ctrl+c", "q", "ctrl+q", "f10":
		// ТЗ §9.2: выход при несохранённых изменениях требует подтверждения
		// (default No); чистая сессия завершается немедленно.
		if m.session != nil && m.session.dirty {
			m.screen = ScreenConfirmQuit
			return m, nil
		}
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(entries)-1 {
			m.cursor++
		}
	case "m":
		if m.session == nil {
			break
		}
		m.mounts = loadMounts(context.Background(), &m)
		m.screen = ScreenMounts
	case "h":
		m.history = loadHistory(&m)
		m.screen = ScreenHistory
	case "l":
		m.logEvents = loadLog(&m, screenPageSize)
		m.screen = ScreenLog
	case "f1", "?":
		m.screen = ScreenHelp
	case "enter":
		// Редактирование записи под курсором: newEditForm получает индекс
		// узла в document.Nodes, а не номер строки таблицы.
		if idxs := m.entryIndexes(); m.cursor < len(idxs) {
			m.form = newEditForm(m.document, idxs[m.cursor])
			m.screen = ScreenForm
		}
	case "insert", "i":
		m.form = newAddForm()
		m.screen = ScreenForm
	case "d":
		// Diff несохранённых изменений сессии (Task 5). Удаление записи —
		// клавишей Del: 'd' занят diff-экраном, как и внутри формы (Task 4).
		if m.session == nil {
			break
		}
		m.diff = newDiffModel(m.diffSession())
		m.screen = ScreenDiff
	case "delete":
		// Удаление записи под курсором: селектор — физическая строка записи.
		// session nil только на loadErr-экране, который сюда не доходит,
		// но защита обязательна: паника недопустима.
		if m.session == nil {
			break
		}
		if m.cursor < len(entries) {
			line := entries[m.cursor] + 1
			if err := m.session.applyOp(m.document, func(d *models.Document) error {
				_, err := editor.Delete(d, editor.ByLine(line))
				return err
			}); err != nil {
				m.status = m.theme.MarkError(Trf(m.opts.Lang, "op_failed", err.Error()))
			}
			// Зажать курсор: после удаления последней записи индекс уходил
			// за пределы списка, и навигация/статус работали по пустоте.
			if n := len(m.entryIndexes()); m.cursor >= n {
				m.cursor = max(0, n-1)
			}
		}
	case "ctrl+z":
		if m.session == nil {
			break
		}
		if m.session.undo(m.document) {
			m.status = Tr(m.opts.Lang, "undo_done")
		} else {
			m.status = m.theme.Muted.Render(Tr(m.opts.Lang, "undo_empty"))
		}
	case "ctrl+s", "f2":
		// Сохранение через transaction: журнал → ExecuteTransaction →
		// событие журнала → сброс сессии. Итог показывает ScreenSaveResult.
		if m.session == nil {
			break
		}
		m.lastSave = save(context.Background(), &m)
		m.screen = ScreenSaveResult
	case "f5":
		// Перезагрузка target: при несохранённых изменениях — модальное
		// подтверждение (default No, ТЗ §15), иначе немедленно.
		if m.session == nil {
			break
		}
		if m.session.dirty {
			m.screen = ScreenConfirmReload
		} else {
			m.reloadFromDisk()
			m.status = Tr(m.opts.Lang, "reload_done")
		}
	}
	// Enter (редактирование формы), Ins/i (добавление формы) подключены;
	// остальные экраны — Task 5–6.
	return m, nil
}

// entryIndexes возвращает индексы узлов-записей документа в порядке следования.
// Закомментированные записи входят в список: курсор перемещается по всем
// записям, а признак активности показывается в колонке состояния.
func (m model) entryIndexes() []int {
	idx := make([]int, 0)
	if m.document == nil {
		return idx
	}
	for i, node := range m.document.Nodes {
		if node.Kind == models.NodeEntry || node.Kind == models.NodeCommentedEntry {
			idx = append(idx, i)
		}
	}
	return idx
}

// nodePositionLine возвращает 1-based номер физической строки документа для
// узла по его индексу: парсер не сохраняет порядковый номер узла отдельно,
// поэтому позиция совпадает с индексом + 1.
func nodePositionLine(_ *models.Document, idx int) int { return idx + 1 }

// viewTable рендерит таблицу записей по образцу ТЗ §9.1: колонки
// # │ Device │ Mount │ Type │ Options │ D │ P │ активность. Закомментированные
// записи помечаются текстовым маркером "-" в колонке активности.
func (m model) viewTable() string {
	var b strings.Builder
	title := m.theme.Title.Render(Tr(m.opts.Lang, "app_title"))
	if m.session != nil && m.session.dirty {
		// Dirty-индикатор ТЗ §9.1: "*" в заголовке при несохранённых изменениях.
		title += m.theme.Dirty.Render(" *")
	}
	b.WriteString(title + "\n\n")
	header := fmt.Sprintf("%-3s %-24s %-12s %-8s %-20s %s %s %s",
		Tr(m.opts.Lang, "col_num"), Tr(m.opts.Lang, "col_device"), Tr(m.opts.Lang, "col_mount"),
		Tr(m.opts.Lang, "col_type"), Tr(m.opts.Lang, "col_options"),
		Tr(m.opts.Lang, "col_dump"), Tr(m.opts.Lang, "col_pass"), Tr(m.opts.Lang, "col_state"))
	b.WriteString(m.theme.Header.Render(header) + "\n")
	for row, idx := range m.entryIndexes() {
		node := m.document.Nodes[idx]
		line := nodePositionLine(m.document, idx)
		state := "+"
		if node.Kind == models.NodeCommentedEntry {
			state = "-"
		}
		entry := node.Entry
		var device, mount, fsType, options, dump, pass string
		if entry != nil {
			device = entry.Source.Value
			mount = entry.MountPoint.Value
			fsType = entry.FSType.Value
			options = entry.Options.Value
			if entry.Dump != nil {
				dump = entry.Dump.Value
			}
			if entry.Pass != nil {
				pass = entry.Pass.Value
			}
		}
		rowText := fmt.Sprintf("%-3d %-24.24s %-12.12s %-8.8s %-20.20s %s %s %s",
			line, device, mount, fsType, options, dump, pass, state)
		if row == m.cursor {
			rowText = m.theme.Selected.Render(rowText)
		}
		b.WriteString(rowText + "\n")
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_table")) + "\n")
	if m.status != "" {
		b.WriteString(m.theme.Info.Render(m.status) + "\n")
	}
	return b.String()
}
