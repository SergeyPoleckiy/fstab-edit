package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// TestNewAddFormEmptyFields проверяет начальное состояние формы добавления:
// режим formAdd и все шесть полей пустые.
func TestNewAddFormEmptyFields(t *testing.T) {
	f := newAddForm()
	if f.mode != formAdd {
		t.Fatalf("mode: %v, ожидался formAdd", f.mode)
	}
	for i, in := range f.inputs {
		if in.Value() != "" {
			t.Fatalf("поле %d не пустое: %q", i, in.Value())
		}
	}
	if f.comment.Value() != "" {
		t.Fatalf("комментарий не пустой: %q", f.comment.Value())
	}
}

// TestNewEditFormPrefilled проверяет предзаполнение формы редактирования:
// шесть decoded-значений записи и текст связанного comment block над ней.
// Блок комментария намеренно не доходит до первой строки документа:
// conservative-правило отбрасывает заголовок документа.
func TestNewEditFormPrefilled(t *testing.T) {
	doc := testDocument(t, "UUID=root / ext4 defaults 0 1\n# foo\nUUID=a /data ext4 rw 0 2\n")
	f := newEditForm(doc, 2)
	if f.mode != formEdit {
		t.Fatalf("mode: %v, ожидался formEdit", f.mode)
	}
	if f.line != 3 {
		t.Fatalf("line: %d, ожидалась физическая строка 3", f.line)
	}
	want := []string{"UUID=a", "/data", "ext4", "rw", "0", "2"}
	for i, value := range want {
		if f.inputs[i].Value() != value {
			t.Fatalf("поле %d: %q, ожидалось %q", i, f.inputs[i].Value(), value)
		}
	}
	if f.comment.Value() != "foo" {
		t.Fatalf("комментарий: %q, ожидался %q", f.comment.Value(), "foo")
	}
}

// TestValidateEmptyMount проверяет live-валидацию: пустой mount отклоняется
// непустым текстом ошибки даже при корректных остальных полях.
func TestValidateEmptyMount(t *testing.T) {
	f := newAddForm()
	f.inputs[0].SetValue("UUID=a")
	f.inputs[2].SetValue("ext4")
	f.inputs[3].SetValue("rw")
	if msg := f.validate(testDocument(t, "")); msg == "" {
		t.Fatal("пустой mount должен отклоняться")
	}
}

// TestApplyAddThroughSession проверяет, что apply режима add проходит через
// session.applyOp: запись добавлена, сессия dirty, undo-стек вырос на один.
func TestApplyAddThroughSession(t *testing.T) {
	doc := testDocument(t, "UUID=root / ext4 defaults 0 1\n")
	m := model{document: doc, session: &sessionState{original: doc.Serialize()}}
	f := newAddForm()
	f.inputs[0].SetValue("UUID=data")
	f.inputs[1].SetValue("/data")
	f.inputs[2].SetValue("ext4")
	f.inputs[3].SetValue("rw")
	if err := f.apply(&m); err != nil {
		t.Fatal(err)
	}
	if got := len(m.entryIndexes()); got != 2 {
		t.Fatalf("записей: %d, ожидалось 2", got)
	}
	if !m.session.dirty {
		t.Fatal("сессия не стала dirty")
	}
	if got := len(m.session.undoStack); got != 1 {
		t.Fatalf("undo-стек: %d, ожидался 1", got)
	}
}

// TestApplyEditChangesOnlySelected проверяет, что edit-форма меняет только
// выбранную запись: правка options первой записи не затрагивает вторую.
func TestApplyEditChangesOnlySelected(t *testing.T) {
	doc := testDocument(t, "UUID=a /data ext4 rw 0 2\nUUID=b /home ext4 defaults 0 2\n")
	m := model{document: doc, session: &sessionState{original: doc.Serialize()}}
	f := newEditForm(doc, 0)
	f.inputs[3].SetValue("noatime")
	if err := f.apply(&m); err != nil {
		t.Fatal(err)
	}
	if got := m.document.Nodes[0].Raw; !strings.Contains(got, "noatime") {
		t.Fatalf("первая запись: %q, ожидалось noatime", got)
	}
	if got := m.document.Nodes[1].Raw; got != "UUID=b /home ext4 defaults 0 2" {
		t.Fatalf("вторая запись изменена: %q", got)
	}
	if got := len(m.session.undoStack); got != 1 {
		t.Fatalf("undo-стек: %d, ожидался 1", got)
	}
}

// TestPreviewShowsRawLine проверяет предпросмотр: заполненная форма add
// показывает сериализованную raw-строку записи с tab-разделителями.
func TestPreviewShowsRawLine(t *testing.T) {
	f := newAddForm()
	f.inputs[0].SetValue("UUID=zz")
	f.inputs[1].SetValue("/data")
	f.inputs[2].SetValue("btrfs")
	f.inputs[3].SetValue("rw")
	f.inputs[4].SetValue("1")
	f.inputs[5].SetValue("2")
	f.refreshPreview()
	want := "UUID=zz\t/data\tbtrfs\trw\t1\t2"
	if f.preview != want {
		t.Fatalf("preview: %q, ожидалось %q", f.preview, want)
	}
}

// TestFormFocusCyclesThroughFields проверяет циклическую навигацию фокуса:
// Tab двигает вперёд по семи полям (6 полей + комментарий) и заворачивает
// с последнего на первое, Shift+Tab — назад.
func TestFormFocusCyclesThroughFields(t *testing.T) {
	m := model{opts: options{Lang: "ru"}, screen: ScreenForm, form: newAddForm()}
	step := func(key tea.KeyMsg) model {
		next, _ := m.Update(key)
		return next.(model)
	}
	m = step(tea.KeyMsg{Type: tea.KeyTab})
	if m.form.focus != 1 {
		t.Fatalf("focus после Tab: %d, ожидался 1", m.form.focus)
	}
	for i := 0; i < 5; i++ {
		m = step(tea.KeyMsg{Type: tea.KeyTab})
	}
	if m.form.focus != 6 {
		t.Fatalf("focus после 6 Tab: %d, ожидался 6 (комментарий)", m.form.focus)
	}
	m = step(tea.KeyMsg{Type: tea.KeyTab})
	if m.form.focus != 0 {
		t.Fatalf("focus после 7 Tab: %d, ожидалась обёртка на 0", m.form.focus)
	}
	m = step(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.form.focus != 6 {
		t.Fatalf("focus после Shift+Tab: %d, ожидался 6", m.form.focus)
	}
}

// TestFormAddFlow — сквозной teatest-сценарий: 'i' открывает форму, ввод полей
// через Tab-навигацию, Enter применяет запись; запись видна в таблице, в
// заголовке появился dirty-индикатор "*".
func TestFormAddFlow(t *testing.T) {
	target := writeTestFstab(t, "UUID=root / ext4 defaults 0 1\n")
	m := NewModel(testOptions(t, target))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "UUID=root")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "form_title_add")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	fields := []string{"UUID=zz", "/data", "ext4", "rw", "0", "2"}
	for i, value := range fields {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)})
		if i < len(fields)-1 {
			tm.Send(tea.KeyMsg{Type: tea.KeyTab})
		}
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "UUID=zz") && strings.Contains(s, "*")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}
