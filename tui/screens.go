package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"fstab-edit/internal/diff"
	"fstab-edit/internal/sysutil"
	"fstab-edit/pkg/models"

	tea "github.com/charmbracelet/bubbletea"
)

// screenPageSize — сколько строк прокручивают pgup/pgdown и какое число
// последних событий читает экран журнала.
const screenPageSize = 20

// diffModel — постраничный просмотр unified diff (экран diff; панель формы
// Task 4 строит diff отдельно и этим типом не пользуется).
type diffModel struct {
	lines  []string
	offset int
}

// newDiffModel разбивает unified diff на строки для постраничного показа.
func newDiffModel(d string) diffModel { return diffModel{lines: strings.Split(d, "\n")} }

// scroll сдвигает окно просмотра; границы зажаты, отрицательный offset недопустим.
func (d *diffModel) scroll(delta int) {
	d.offset = clampOffset(d.offset+delta, len(d.lines))
}

// clampOffset зажимает offset в [0, max(0, total-1)]: прокрутка за пределы
// содержимого останавливается на границе без паники.
func clampOffset(offset, total int) int {
	max := total - 1
	if max < 0 {
		max = 0
	}
	if offset < 0 {
		return 0
	}
	if offset > max {
		return max
	}
	return offset
}

// mountsModel — результат findmnt по текущему документу: безопасный Report
// проверки candidate, смонтированность записей и сбой запуска, если был.
type mountsModel struct {
	Verification sysutil.Verification
	Mounted      map[int]bool // физическая строка записи → смонтировано
	Err          error
	offset       int
}

// loadMounts собирает статус монтирований: verify текущего candidate через
// findmnt и per-entry проверку смонтированности (ТЗ §9.5, экран M).
// Сбои findmnt не паникуют и не маскируются: попадают в Err отдельно от
// частично заполненного списка.
func loadMounts(ctx context.Context, m *model) mountsModel {
	out := mountsModel{Mounted: make(map[int]bool)}
	verification := sysutil.VerifyCandidate(ctx, m.opts.Runner, m.session.candidate(m.document))
	out.Verification = verification
	if verification.Execution.Err != nil {
		out.Err = verification.Execution.Err
		return out
	}
	for _, idx := range m.entryIndexes() {
		node := m.document.Nodes[idx]
		if node.Entry == nil || node.Kind != models.NodeEntry {
			continue // закомментированные записи физически не смонтированы
		}
		mount := node.Entry.MountPoint.Value
		res := m.opts.Runner.Run(ctx, "findmnt", "--noheadings", "--output=TARGET", mount)
		out.Mounted[idx+1] = res.ExitCode == 0
	}
	return out
}

// logModel — последние события журнала (newest last), максимум limit штук.
type logModel struct {
	Events []loggerEvent // logger.Event, декодированный из JSON Lines
	Err    error
	offset int
}

// loggerEvent — строка журнала; timestamp строкой: экран только показывает
// события и не обязан разбирать time.Parse-форматы.
type loggerEvent struct {
	Severity  string `json:"severity"`
	Timestamp string `json:"timestamp"`
	Operation string `json:"operation"`
	Target    string `json:"target"`
	BackupID  string `json:"backup_id,omitempty"`
	Result    string `json:"result"`
}

// loadLog читает последние limit событий журнала newest-last. Файл только
// читается: read-only экран не создаёт журнал (ТЗ §14). Битые строки
// пропускаются: одна повреждённая запись не должна ломать просмотр.
func loadLog(m *model, limit int) logModel {
	out := logModel{}
	f, err := os.Open(m.opts.Log.File)
	if err != nil {
		out.Err = err
		return out
	}
	defer func() { _ = f.Close() }()
	lines := make([]string, 0)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		out.Err = err
		return out
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	out.Events = make([]loggerEvent, 0, len(lines))
	for _, line := range lines {
		var e loggerEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // битая строка пропускается
		}
		out.Events = append(out.Events, e)
	}
	return out
}

// diffSession строит unified diff original→current candidate для экрана diff:
// несохранённые изменения сессии видны в проверке и в diff до сохранения.
func (m model) diffSession() string {
	if m.session == nil {
		return ""
	}
	return diff.Unified("<original>", "<current>", m.session.original, m.session.candidate(m.document))
}

// mountsContent собирает строки экрана монтирований: диагностики проверки
// candidate (severity=error — MarkError, warning — MarkWarning) и таблицу
// записей с текстовой пометкой смонтированности (+/−, не только цвет).
func (m model) mountsContent() []string {
	lines := make([]string, 0)
	for _, d := range m.mounts.Verification.Report.Diagnostics {
		message := d.Message
		if d.Line > 0 {
			message = Trf(m.opts.Lang, "diag_at_line", message, d.Line)
		}
		switch d.Severity {
		case models.SeverityError:
			lines = append(lines, m.theme.MarkError(message))
		case models.SeverityWarning:
			lines = append(lines, m.theme.MarkWarning(message))
		default:
			lines = append(lines, m.theme.Info.Render(message))
		}
	}
	lines = append(lines, m.theme.Header.Render(Tr(m.opts.Lang, "mounts_header")))
	for _, idx := range m.entryIndexes() {
		node := m.document.Nodes[idx]
		line := nodePositionLine(m.document, idx)
		state := "-"
		if m.mounts.Mounted[line] {
			state = "+"
		}
		var device, mount string
		if node.Entry != nil {
			device = node.Entry.Source.Value
			mount = node.Entry.MountPoint.Value
		}
		lines = append(lines, fmt.Sprintf("%-3d %-24.24s %-16.16s %s", line, device, mount, state))
	}
	return lines
}

// viewMounts рендерит экран статуса монтирований (клавиша M, ТЗ §9.5).
func (m model) viewMounts() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(Tr(m.opts.Lang, "mounts_title")) + "\n\n")
	if m.mounts.Err != nil {
		b.WriteString(m.theme.MarkError(Trf(m.opts.Lang, "mounts_error", m.mounts.Err.Error())) + "\n")
	} else {
		writeWindow(&b, m.mountsContent(), m.mounts.offset)
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_back")) + "\n")
	return b.String()
}

// logContent собирает строки экрана журнала: severity/operation/target/
// result/backup_id — коды не локализуются (ТЗ: enum values не переводятся).
func (m model) logContent() []string {
	lines := make([]string, 0, len(m.logEvents.Events))
	for _, e := range m.logEvents.Events {
		lines = append(lines, fmt.Sprintf("%-8.8s %-10.10s %-24.24s %-8.8s %s",
			e.Severity, e.Operation, e.Target, e.Result, e.BackupID))
	}
	return lines
}

// viewLog рендерит экран журнала (клавиша L): таблица последних событий;
// пустой журнал и сбой чтения различаются текстом (только muted, не цветом).
func (m model) viewLog() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(Tr(m.opts.Lang, "log_title")) + "\n\n")
	switch {
	case m.logEvents.Err != nil:
		b.WriteString(m.theme.MarkError(Trf(m.opts.Lang, "log_error", m.logEvents.Err.Error())) + "\n")
	case len(m.logEvents.Events) == 0:
		b.WriteString(m.theme.Muted.Render(Tr(m.opts.Lang, "log_empty")) + "\n")
	default:
		b.WriteString(m.theme.Header.Render(Tr(m.opts.Lang, "log_header")) + "\n")
		writeWindow(&b, m.logContent(), m.logEvents.offset)
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_back")) + "\n")
	return b.String()
}

// viewHelp рендерит статический экран помощи: таблица горячих клавиш и их
// действий. Клавиши — литералы (Ctrl+S, F10 и т.д. не переводятся), описания
// — через каталоги RU/EN.
func (m model) viewHelp() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(Tr(m.opts.Lang, "help_title")) + "\n\n")
	rows := [][2]string{
		{Tr(m.opts.Lang, "help_key_save"), Tr(m.opts.Lang, "help_desc_save")},
		{Tr(m.opts.Lang, "help_key_undo"), Tr(m.opts.Lang, "help_desc_undo")},
		{Tr(m.opts.Lang, "help_key_reload"), Tr(m.opts.Lang, "help_desc_reload")},
		{Tr(m.opts.Lang, "help_key_quit"), Tr(m.opts.Lang, "help_desc_quit")},
		{Tr(m.opts.Lang, "help_key_edit"), Tr(m.opts.Lang, "help_desc_edit")},
		{Tr(m.opts.Lang, "help_key_add"), Tr(m.opts.Lang, "help_desc_add")},
		{Tr(m.opts.Lang, "help_key_delete"), Tr(m.opts.Lang, "help_desc_delete")},
		{Tr(m.opts.Lang, "help_key_comment"), Tr(m.opts.Lang, "help_desc_comment")},
		{Tr(m.opts.Lang, "help_key_diff"), Tr(m.opts.Lang, "help_desc_diff")},
		{Tr(m.opts.Lang, "help_key_mounts"), Tr(m.opts.Lang, "help_desc_mounts")},
		{Tr(m.opts.Lang, "help_key_history"), Tr(m.opts.Lang, "help_desc_history")},
		{Tr(m.opts.Lang, "help_key_log"), Tr(m.opts.Lang, "help_desc_log")},
		{Tr(m.opts.Lang, "help_key_help"), Tr(m.opts.Lang, "help_desc_help")},
		{Tr(m.opts.Lang, "help_key_back"), Tr(m.opts.Lang, "help_desc_back")},
	}
	for _, row := range rows {
		b.WriteString(fmt.Sprintf("%-16s %s", row[0], row[1]) + "\n")
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_back")) + "\n")
	return b.String()
}

// viewDiff рендерит экран diff текущей сессии: заголовок фиксирует
// направление original→current; пустой diff (нет изменений) подписан текстом.
func (m model) viewDiff() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(
		Trf(m.opts.Lang, "diff_title", "<original>", "<current>")) + "\n\n")
	if len(m.diff.lines) == 0 || (len(m.diff.lines) == 1 && m.diff.lines[0] == "") {
		b.WriteString(m.theme.Muted.Render(Tr(m.opts.Lang, "diff_empty")) + "\n")
	} else {
		writeWindow(&b, m.diff.lines, m.diff.offset)
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_back")) + "\n")
	return b.String()
}

// writeWindow выводит content начиная с offset: прокрутка экрана — это
// сдвиг окна, а не пересборка содержимого.
func writeWindow(b *strings.Builder, content []string, offset int) {
	for i := offset; i < len(content); i++ {
		b.WriteString(content[i] + "\n")
	}
}

// updateScrollable обрабатывает клавиши прокручиваемых read-only экранов
// (diff/монтирования/журнал, Task 5): стрелки и k/j сдвигают offset на строку,
// pgup/pgdown — на страницу, Esc возвращает на главную таблицу.
func (m model) updateScrollable(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	delta := 0
	switch msg.String() {
	case "esc":
		m.screen = ScreenTable
		return m, nil
	case "up", "k":
		delta = -1
	case "down", "j":
		delta = 1
	case "pgup":
		delta = -screenPageSize
	case "pgdown":
		delta = screenPageSize
	default:
		return m, nil
	}
	switch m.screen {
	case ScreenDiff:
		m.diff.scroll(delta)
	case ScreenMounts:
		m.mounts.offset = clampOffset(m.mounts.offset+delta, len(m.mountsContent()))
	case ScreenLog:
		m.logEvents.offset = clampOffset(m.logEvents.offset+delta, len(m.logContent()))
	}
	return m, nil
}
