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
	case "m", "M":
		if m.session == nil {
			break
		}
		m.mounts = loadMounts(context.Background(), &m)
		m.screen = ScreenMounts
	case "b", "B":
		// Блочные устройства (ТЗ §5.4): обрабатываются оба регистра — Shift+B
		// отдаёт rune 'B', обычная 'b' — "b"; read-only экран не должен требовать
		// layout-переключения. Загрузка — синхронно при входе: повторный вход
		// перечитывает список устройств.
		m.blockdev = loadBlockDevices(context.Background(), &m)
		m.screen = ScreenBlockDevices
	case "h", "H":
		m.history = loadHistory(&m)
		m.screen = ScreenHistory
	case "l", "L":
		m.logEvents = loadLog(&m, screenPageSize)
		m.screen = ScreenLog
	case "f1", "?":
		m.screen = ScreenHelp
	case "enter":
		// Редактирование записи под курсором: newEditForm получает индекс
		// узла в document.Nodes, а не номер строки таблицы. Устройства для
		// подсказок грузятся при открытии формы; их отсутствие (ошибка/пусто)
		// форму не ломает — панель просто не показывается.
		if idxs := m.entryIndexes(); m.cursor < len(idxs) {
			m.form = newEditForm(m.document, idxs[m.cursor])
			m.form.devices, m.form.devicesErr = loadFormDevices(context.Background(), &m)
			m.form.refreshHints()
			m.screen = ScreenForm
		}
	case "insert", "i":
		m.form = newAddForm()
		m.form.devices, m.form.devicesErr = loadFormDevices(context.Background(), &m)
		m.form.refreshHints()
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
			} else {
				// Документ изменился: пересчитать кэш диагностик, чтобы
				// маркеры "!"/"?" строк состояния соответствовали новому
				// содержимому (удалённая запись больше не подсвечивается).
				m.rebuildDiagnostics()
			}
			// Зажать курсор: после удаления последней записи индекс уходил
			// за пределы списка, и навигация/статус работали по пустоте.
			if n := len(m.entryIndexes()); m.cursor >= n {
				m.cursor = max(0, n-1)
			}
		}
	case "c":
		// Переключение комментария записи под курсором: активная запись
		// комментируется (Comment), закомментированная — включается (Uncomment).
		// Селектор — физическая строка записи; applyOp даёт undo и dirty бесплатно.
		if m.session == nil {
			break
		}
		if m.cursor < len(entries) {
			idx := entries[m.cursor]
			line := idx + 1
			commented := m.document.Nodes[idx].Kind == models.NodeCommentedEntry
			err := m.session.applyOp(m.document, func(d *models.Document) error {
				var err error
				if commented {
					_, err = editor.Uncomment(d, editor.ByLine(line))
				} else {
					_, err = editor.Comment(d, editor.ByLine(line))
				}
				return err
			})
			if err != nil {
				m.status = m.theme.MarkError(Trf(m.opts.Lang, "comment_failed", err.Error()))
			} else {
				m.status = m.theme.Info.Render(Tr(m.opts.Lang, "comment_done"))
				// Комментирование/включение меняет набор активных записей, а
				// валидатор проверяет только их: кэш диагностик пересчитан.
				m.rebuildDiagnostics()
			}
		}
	case "ctrl+z":
		if m.session == nil {
			break
		}
		if m.session.undo(m.document) {
			m.status = Tr(m.opts.Lang, "undo_done")
			// Undo вернул документ к прежнему снапшоту: пересчитать кэш
			// диагностик, чтобы маркеры строк вернулись к прежнему виду.
			m.rebuildDiagnostics()
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
		// Перезагрузка target: работает и на loadErr-экране (session == nil) —
		// reloadFromDisk сам восстанавливает document/session или обновляет
		// ошибку. При несохранённых изменениях — модальное подтверждение
		// (default No, ТЗ §15), иначе немедленно.
		if m.session != nil && m.session.dirty {
			m.screen = ScreenConfirmReload
		} else if err := m.reloadFromDisk(); err == nil {
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

// Ширины колонок таблицы записей. Совпадают с форматом заголовка (ТЗ §9.1)
// и фиксированы: ячейки рендерятся Field-стилями темы через fitCell, поэтому
// ширина задаётся здесь одним местом, а не в форматной строке.
const (
	widthColNum     = 3  // № строки
	widthColDevice  = 24 // source (device)
	widthColMount   = 12 // mount point
	widthColFSType  = 8  // fs type
	widthColOptions = 20 // options
)

// fitCell подгоняет значение ячейки под фиксированную ширину колонки: урезает
// по рунам (fstab-значения могут содержать UTF-8, а форматное усечение fmt
// %-24.24s режет по байтам и ломало бы многобайтовые символы) и дополняет
// пробелами справа. Работа с plain-текстом до стилизации: lipgloss добавляет
// ANSI-коды вокруг целой строки, значение внутри не разрывается, поэтому
// contains-проверки по выводу таблицы остаются рабочими.
func fitCell(value string, width int) string {
	runes := []rune(value)
	if len(runes) > width {
		runes = runes[:width]
	}
	return string(runes) + strings.Repeat(" ", width-len(runes))
}

// viewTable рендерит таблицу записей по образцу ТЗ §9.1: колонки
// # │ Device │ Mount │ Type │ Options │ D │ P │ активность. Каждая колонка
// записи рендерится своим Field-стилем темы (подсветка синтаксиса, ТЗ §5.3):
// source — cyan, mount — зелёный, fs type — оранжевый, options — серый.
// Колонка активности показывает текстовый маркер: "!" красным для строки с
// ошибкой валидатора, "?" жёлтым для предупреждения, иначе "+" (активная)
// или "-" (закомментированная) как раньше.
// Курсорная строка — особый случай: она рендерится БЕЗ внутренних стилей
// (plain-ячейки, маркер-символ без цвета) и целиком оборачивается
// theme.Selected. Причина: lipgloss НЕ умеет вложенность — каждый
// Style.Render заканчивается сбросом \x1b[0m, который гасит SGR внешнего
// Selected, и выделение получалось прерывистым (регрессия против старого
// plain-рендера). Проблемность записи под курсором читается маркером-символом
// ("!"/"?") без цвета — текстовые маркеры обязательны во всех темах (ТЗ §9.4).
func (m model) viewTable() string {
	var b strings.Builder
	title := m.theme.Title.Render(Tr(m.opts.Lang, "app_title"))
	if m.session != nil && m.session.dirty {
		// Dirty-индикатор ТЗ §9.1: "*" в заголовке при несохранённых изменениях.
		title += m.theme.Dirty.Render(" *")
	}
	b.WriteString(title + "\n\n")
	header := fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s %s %s %s",
		widthColNum, Tr(m.opts.Lang, "col_num"),
		widthColDevice, Tr(m.opts.Lang, "col_device"),
		widthColMount, Tr(m.opts.Lang, "col_mount"),
		widthColFSType, Tr(m.opts.Lang, "col_type"),
		widthColOptions, Tr(m.opts.Lang, "col_options"),
		Tr(m.opts.Lang, "col_dump"), Tr(m.opts.Lang, "col_pass"), Tr(m.opts.Lang, "col_state"))
	b.WriteString(m.theme.Header.Render(header) + "\n")
	for row, idx := range m.entryIndexes() {
		node := m.document.Nodes[idx]
		line := nodePositionLine(m.document, idx)
		// Маркер активности: текстовый, читается без цвета (ТЗ §9.4/§9.6).
		// Диагностика валидатора сильнее признака активности: проблемная
		// строка помечается "!" или "?" даже если запись активна. stateMarker —
		// plain-символ (для курсорной строки), stateCell — цветной маркер
		// (для остальных строк).
		stateMarker := "+"
		if node.Kind == models.NodeCommentedEntry {
			stateMarker = "-"
		}
		stateCell := stateMarker
		switch m.severityByLine[line] {
		case models.SeverityError:
			stateMarker, stateCell = "!", m.theme.SevError.Render("!")
		case models.SeverityWarning:
			stateMarker, stateCell = "?", m.theme.SevWarning.Render("?")
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
		// Строка собирается в двух вариантах из одних значений. Подсвеченный:
		// каждая ячейка подогнана под ширину колонки и стилизована своим
		// Field-стилем, маркер — цветной, разделители — одиночные пробелы
		// (как в формате заголовка). Plain: те же ячейки без стилей и
		// маркер-символ — им рендерится курсорная строка перед Selected,
		// чтобы вложенные сбросы SGR не гасили выделение. Dump/pass как раньше
		// без подсветки в обоих вариантах.
		plainRow := fmt.Sprintf("%-*d %s %s %s %s %s %s %s",
			widthColNum, line,
			fitCell(device, widthColDevice),
			fitCell(mount, widthColMount),
			fitCell(fsType, widthColFSType),
			fitCell(options, widthColOptions),
			dump, pass, stateMarker)
		styledRow := fmt.Sprintf("%-*d %s %s %s %s %s %s %s",
			widthColNum, line,
			m.theme.FieldSource.Render(fitCell(device, widthColDevice)),
			m.theme.FieldMount.Render(fitCell(mount, widthColMount)),
			m.theme.FieldFSType.Render(fitCell(fsType, widthColFSType)),
			m.theme.FieldOptions.Render(fitCell(options, widthColOptions)),
			dump, pass, stateCell)
		rowText := styledRow
		if row == m.cursor {
			rowText = m.theme.Selected.Render(plainRow)
		}
		b.WriteString(rowText + "\n")
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_table")) + "\n")
	if m.status != "" {
		b.WriteString(m.theme.Info.Render(m.status) + "\n")
	}
	return b.String()
}
