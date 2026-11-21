package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/diff"
	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/validator"
	"fstab-edit/pkg/models"
)

// formMode — добавление или редактирование записи.
type formMode int

const (
	// formAdd — форма добавления новой записи в конец документа.
	formAdd formMode = iota
	// formEdit — форма редактирования существующей записи.
	formEdit
)

// formModel — состояние формы записи: 6 полей + комментарий + фокус.
// Предпросмотр и текст ошибки кэшируются в полях модели и пересчитываются
// после каждого изменения поля (updateForm).
type formModel struct {
	mode    formMode
	line    int               // физическая строка записи (formEdit)
	inputs  []textinput.Model // device, mount, type, options, dump, pass
	comment textinput.Model
	focus   int    // 0..6; 0-5 поля, 6 комментарий
	preview string // кэш предпросмотра raw-строки
	errText string // текстовая ошибка поля (не только цвет!)
	// orig и origComment — значения на входе в форму (formEdit); сравнение
	// с ними определяет Changes для editor.Edit и no-op при выходе.
	orig        []string
	origComment string
	showDiff    bool // true — панель показывает diff original→candidate
}

// newAddForm собирает пустую форму добавления с фокусом на первом поле.
func newAddForm() formModel {
	f := formModel{mode: formAdd, inputs: make([]textinput.Model, 6)}
	for i := range f.inputs {
		f.inputs[i] = newFormInput()
	}
	f.comment = newFormInput()
	f.inputs[0].Focus()
	return f
}

// newFormInput собирает textinput без prompt: метки полей рисует viewForm,
// лишний префикс ввода только сдвигал бы колонку значений.
func newFormInput() textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	return input
}

// newEditForm собирает форму редактирования, предзаполненную decoded-значениями
// записи узла nodeIndex (индекс в document.Nodes, физическая строка — nodeIndex+1).
// Связанный comment block подставляется по conservative-правилу, как в cliapp:
// только непрерывные NodeComment непосредственно над записью; блок, доходящий
// до первой строки документа, считается заголовком и пропускается.
func newEditForm(doc *models.Document, nodeIndex int) formModel {
	f := newAddForm()
	f.mode = formEdit
	f.orig = make([]string, len(f.inputs))
	if doc == nil || nodeIndex < 0 || nodeIndex >= len(doc.Nodes) {
		return f
	}
	f.line = nodeIndex + 1
	node := doc.Nodes[nodeIndex]
	if node.Entry != nil {
		entry := node.Entry
		f.inputs[0].SetValue(entry.Source.Value)
		f.inputs[1].SetValue(entry.MountPoint.Value)
		f.inputs[2].SetValue(entry.FSType.Value)
		f.inputs[3].SetValue(entry.Options.Value)
		if entry.Dump != nil {
			f.inputs[4].SetValue(entry.Dump.Value)
		}
		if entry.Pass != nil {
			f.inputs[5].SetValue(entry.Pass.Value)
		}
	}
	for i := range f.inputs {
		f.orig[i] = f.inputs[i].Value()
	}
	f.origComment = formAssociatedComment(doc, nodeIndex)
	f.comment.SetValue(f.origComment)
	f.refreshPreview()
	return f
}

// validate собирает кандидата через editor.Add в пустой документ и возвращает
// "" или текст первой диагностики severity=error. Сборка через editor, а не
// через ручную tab-строку: пустое обязательное поле отклоняется ErrEmptyField
// до парсинга (tab-separated сборка схлопнула бы его и потеряла ошибку), а
// пустые dump/pass подменяются "0" — это штатное поведение добавления.
func (f *formModel) validate(_ *models.Document) string {
	candidate := parser.Parse(nil)
	if _, err := editor.Add(candidate, f.newEntry()); err != nil {
		return err.Error()
	}
	report := validator.Validate(candidate)
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Severity == models.SeverityError {
			return diagnostic.Message
		}
	}
	return ""
}

// newEntry собирает editor.NewEntry из текущих значений формы. Пустой
// комментарий не передаётся: Comment != nil создал бы блок в записи.
func (f *formModel) newEntry() editor.NewEntry {
	entry := editor.NewEntry{
		Source:     f.inputs[0].Value(),
		MountPoint: f.inputs[1].Value(),
		FSType:     f.inputs[2].Value(),
		Options:    f.inputs[3].Value(),
		Dump:       f.inputs[4].Value(),
		Pass:       f.inputs[5].Value(),
	}
	if value := f.comment.Value(); value != "" {
		entry.Comment = &value
	}
	return entry
}

// buildChanges собирает editor.Changes из различий с исходными значениями:
// изменённое поле — новое значение, очистка непустого dump/pass — Unset-флаг,
// пустой комментарий при непустом исходном — ClearComment.
func (f *formModel) buildChanges() editor.Changes {
	changes := editor.Changes{}
	set := func(i int, dst **string) {
		if f.inputs[i].Value() != f.orig[i] {
			value := f.inputs[i].Value()
			*dst = &value
		}
	}
	set(0, &changes.Source)
	set(1, &changes.MountPoint)
	set(2, &changes.FSType)
	set(3, &changes.Options)
	if f.inputs[4].Value() != f.orig[4] {
		if f.inputs[4].Value() == "" {
			changes.UnsetDump = true
		} else {
			value := f.inputs[4].Value()
			changes.Dump = &value
		}
	}
	if f.inputs[5].Value() != f.orig[5] {
		if f.inputs[5].Value() == "" {
			changes.UnsetPass = true
		} else {
			value := f.inputs[5].Value()
			changes.Pass = &value
		}
	}
	if f.comment.Value() != f.origComment {
		if f.comment.Value() == "" {
			changes.ClearComment = true
		} else {
			value := f.comment.Value()
			changes.Comment = &value
		}
	}
	return changes
}

// hasChanges сообщает, отличается ли форма редактирования от исходных
// значений; форма добавления всегда считается изменённой.
func (f *formModel) hasChanges() bool {
	if f.mode == formAdd {
		return true
	}
	for i := range f.inputs {
		if f.inputs[i].Value() != f.orig[i] {
			return true
		}
	}
	return f.comment.Value() != f.origComment
}

// buildOp собирает операцию документа для режима формы: Add для formAdd и
// Edit по физической строке для formEdit. Один helper для apply и diff-панели,
// чтобы кандидат и применённый результат не расходились.
func (f *formModel) buildOp() func(*models.Document) error {
	if f.mode == formAdd {
		entry := f.newEntry()
		return func(d *models.Document) error {
			_, err := editor.Add(d, entry)
			return err
		}
	}
	changes := f.buildChanges()
	line := f.line
	return func(d *models.Document) error {
		_, err := editor.Edit(d, editor.ByLine(line), changes)
		return err
	}
}

// apply выполняет операцию формы через session.applyOp: снапшот для undo,
// dirty и no-op-семантика остаются внутри сессии.
func (f formModel) apply(m *model) error {
	if m.session == nil {
		return editor.ErrNilDocument
	}
	return m.session.applyOp(m.document, (&f).buildOp())
}

// refreshPreview пересчитывает кэш предпросмотра: сериализованная raw-строка
// записи из временного документа. При некодируемых значениях preview пустой.
func (f *formModel) refreshPreview() {
	f.preview = ""
	candidate := parser.Parse(nil)
	if _, err := editor.Add(candidate, f.newEntry()); err != nil {
		return
	}
	// Последний узел — сама запись; перед ней могут лежать узлы comment block.
	node := candidate.Nodes[len(candidate.Nodes)-1]
	if node.Kind == models.NodeEntry {
		f.preview = node.Raw
	}
}

// diffText строит unified diff original→candidate, применяя операцию формы к
// копии документа (parser.Parse(document.Serialize())), а не к живому.
func (f *formModel) diffText(doc *models.Document) string {
	if doc == nil {
		return ""
	}
	original := doc.Serialize()
	candidate := parser.Parse(append([]byte(nil), original...))
	if err := f.buildOp()(candidate); err != nil {
		return ""
	}
	return diff.Unified("original", "candidate", original, candidate.Serialize())
}

// moveFocus сдвигает фокус по кругу по семи полям (6 полей + комментарий).
// textinput принимает ввод только в состоянии Focus, поэтому blur прежнего
// поля и focus нового обязательны.
func (f *formModel) moveFocus(delta int) {
	total := len(f.inputs) + 1
	f.focus = ((f.focus+delta)%total + total) % total
	for i := range f.inputs {
		f.inputs[i].Blur()
	}
	f.comment.Blur()
	if f.focus < len(f.inputs) {
		f.inputs[f.focus].Focus()
	} else {
		f.comment.Focus()
	}
}

// formAssociatedComment — локальная копия conservative-правила cliapp (helper
// неэкспортирован): к записи привязывается только непрерывный блок NodeComment
// непосредственно над ней; блок до первой строки документа — заголовок.
func formAssociatedComment(document *models.Document, entryIndex int) string {
	lines := make([]string, 0)
	index := entryIndex - 1
	for index >= 0 && document.Nodes[index].Kind == models.NodeComment {
		lines = append([]string{formCommentText(document.Nodes[index].Raw)}, lines...)
		index--
	}
	if len(lines) == 0 || index < 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// formCommentText удаляет маркер комментария и ровно один разделитель после
// него, сохраняя остальное содержимое строки без изменений.
func formCommentText(raw string) string {
	text := strings.TrimLeft(raw, "#")
	return strings.TrimPrefix(text, " ")
}

// updateForm обрабатывает клавиши формы: Esc — отмена, Enter — подтверждение
// (валидация → apply → таблица), d — переключение diff-панели, Tab/Shift+Tab
// и стрелки вверх/вниз — фокус по кругу, остальное — в сфокусированное поле.
func (m model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.screen = ScreenTable
		return m, nil
	case "d":
		m.form.showDiff = !m.form.showDiff
		return m, nil
	case "enter":
		if text := m.form.validate(m.document); text != "" {
			m.form.errText = text
			return m, nil
		}
		if !m.form.hasChanges() {
			m.status = Tr(m.opts.Lang, "form_no_changes")
			m.screen = ScreenTable
			return m, nil
		}
		if err := m.form.apply(&m); err != nil {
			m.form.errText = err.Error()
			return m, nil
		}
		m.status = Tr(m.opts.Lang, "form_applied")
		m.screen = ScreenTable
		return m, nil
	case "tab", "down":
		m.form.moveFocus(1)
	case "shift+tab", "up":
		m.form.moveFocus(-1)
	}
	// Остальные клавиши уходят в сфокусированное поле; после правки ошибка
	// устарела и сбрасывается, предпросмотр пересчитывается.
	var cmd tea.Cmd
	if m.form.focus < len(m.form.inputs) {
		m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
	} else {
		m.form.comment, cmd = m.form.comment.Update(msg)
	}
	m.form.errText = ""
	m.form.refreshPreview()
	return m, cmd
}

// viewForm рендерит форму записи: заголовок режима, метки полей с маркером
// фокуса, строка ошибки с маркером "!", предпросмотр (raw-строка или diff)
// и подсказка клавиш. Маркер фокуса текстовый (">") — тема monochrome не
// обязана давать цвет (ТЗ §9.6).
func (m model) viewForm() string {
	var b strings.Builder
	title := Tr(m.opts.Lang, "form_title_add")
	if m.form.mode == formEdit {
		title = Tr(m.opts.Lang, "form_title_edit")
	}
	b.WriteString(m.theme.Title.Render(title) + "\n\n")
	labels := []string{
		Tr(m.opts.Lang, "form_label_source"),
		Tr(m.opts.Lang, "form_label_mount"),
		Tr(m.opts.Lang, "form_label_type"),
		Tr(m.opts.Lang, "form_label_options"),
		Tr(m.opts.Lang, "form_label_dump"),
		Tr(m.opts.Lang, "form_label_pass"),
	}
	for i := 0; i < len(m.form.inputs); i++ {
		b.WriteString(m.formFieldLine(labels[i], m.form.inputs[i].View(), m.form.focus == i) + "\n")
	}
	commentLabel := Tr(m.opts.Lang, "form_label_comment")
	b.WriteString(m.formFieldLine(commentLabel, m.form.comment.View(), m.form.focus == len(m.form.inputs)) + "\n")
	if m.form.errText != "" {
		b.WriteString(m.theme.MarkError(m.form.errText) + "\n")
	}
	b.WriteString("\n")
	if m.form.showDiff {
		b.WriteString(m.form.diffText(m.document))
	} else {
		b.WriteString(m.theme.Muted.Render(m.form.preview) + "\n")
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_form")) + "\n")
	return b.String()
}

// formFieldLine собирает одну строку поля: два пробела отступа или текстовый
// маркер "> " для фокуса, затем метка и значение.
func (m model) formFieldLine(label, value string, focused bool) string {
	prefix := "  "
	if focused {
		prefix = m.theme.Selected.Render("> ")
	}
	return prefix + label + " " + value
}
