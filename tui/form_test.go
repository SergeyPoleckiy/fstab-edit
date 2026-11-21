package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"fstab-edit/internal/sysutil"
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
// выбранную запись: правка Options первой записи не затрагивает вторую.
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
	m := model{opts: Options{Lang: "ru"}, screen: ScreenForm, form: newAddForm()}
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
		return strings.Contains(string(b), "Новая запись")
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
	// Dirty-сессия: выход запрашивает подтверждение (ТЗ §9.2, default No).
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "есть несохранённые изменения")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// testFormDeviceSeam собирает Deps с подменённым ListBlockDevices: устройства
// подаются готовым списком, реальный lsblk не выполняется. Остальные seams
// форме не нужны (она ничего не сохраняет и не читает с диска).
func testFormDeviceSeam(devices []sysutil.BlockDevice) *Deps {
	return &Deps{
		ListBlockDevices: func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error) {
			return devices, nil
		},
	}
}

// testFormModel собирает модель на главной таблице с подменённым seam
// устройств и открывает форму добавления клавишей 'i' — тем же путём, что и
// в TTY: устройства грузятся при открытии формы, подсказки уже наполнены.
func testFormModel(t *testing.T, devices []sysutil.BlockDevice) model {
	t.Helper()
	doc := testDocument(t, "UUID=root / ext4 defaults 0 1\n")
	m := model{
		opts:     Options{Lang: "ru", Deps: testFormDeviceSeam(devices)},
		document: doc,
		session:  &sessionState{original: doc.Serialize()},
		screen:   ScreenTable,
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	return updated.(model)
}

// testFormDevices — фиксированный список устройств для тестов формы.
func testFormDevices() []sysutil.BlockDevice {
	return []sysutil.BlockDevice{
		{Path: "/dev/sdb1", Type: "part", FSType: "vfat", Size: "16G",
			Label: "USBKEY", UUID: "BBBB-2222", MountPoint: "/mnt/usb"},
	}
}

// TestFormCtrlNSubstitutesDeviceUUID проверяет подстановку подсказки в поле
// device: форма, открытая с устройствами, по Ctrl+N подставляет первое
// значение — UUID=<uuid> из lsblk (приоритет ТЗ п.10).
func TestFormCtrlNSubstitutesDeviceUUID(t *testing.T) {
	m := testFormModel(t, testFormDevices())
	if m.screen != ScreenForm {
		t.Fatalf("экран: %v, ожидалась форма", m.screen)
	}
	if len(m.form.hints) != 1 {
		t.Fatalf("подсказок: %d, ожидалась 1", len(m.form.hints))
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	if got := m.form.inputs[0].Value(); got != "UUID=BBBB-2222" {
		t.Fatalf("поле device: %q, ожидалось %q", got, "UUID=BBBB-2222")
	}
	if m.form.hintIdx != 0 {
		t.Fatalf("hintIdx: %d, ожидался 0", m.form.hintIdx)
	}
}

// TestFormCtrlNNoDevicesNoop проверяет устойчивость формы без устройств:
// при пустой загрузке (ошибка lsblk или пустой список) Ctrl+N не портит поле
// и не паникует — форма работает как до автодополнения.
func TestFormCtrlNNoDevicesNoop(t *testing.T) {
	m := testFormModel(t, nil)
	if len(m.form.hints) != 0 {
		t.Fatalf("подсказок без устройств: %d, ожидалось 0", len(m.form.hints))
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	if got := m.form.inputs[0].Value(); got != "" {
		t.Fatalf("поле device: %q, ожидалась пустота (Ctrl+N — no-op)", got)
	}
	if m.form.hintIdx != -1 {
		t.Fatalf("hintIdx: %d, ожидался -1", m.form.hintIdx)
	}
}

// TestFormFsTypePrefixFilter проверяет префиксную фильтрацию типа ФС: ввод
// "ex" в поле fs_type сужает подсказки до ext2/ext3/ext4/exfat.
func TestFormFsTypePrefixFilter(t *testing.T) {
	m := testFormModel(t, testFormDevices())
	// Два Tab: фокус с device (0) на fs_type (2).
	for i := 0; i < 2; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(model)
	}
	if m.form.focus != 2 {
		t.Fatalf("focus: %d, ожидался 2 (fs_type)", m.form.focus)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ex")})
	m = updated.(model)
	// Порядок — порядок KnownFSTypes (отсортирован): exfat идёт раньше ext2.
	want := []string{"exfat", "ext2", "ext3", "ext4"}
	got := make([]string, 0, len(m.form.hints))
	for _, hint := range m.form.hints {
		got = append(got, hint.Value)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("подсказки fs_type: %v, ожидалось %v", got, want)
	}
	// Ctrl+N подставляет выбранный тип целиком.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	if got := m.form.inputs[2].Value(); got != "exfat" {
		t.Fatalf("поле fs_type: %q, ожидалось %q", got, "exfat")
	}
}

// TestFormOptionsTokenReplacement проверяет подстановку опции: последний токен
// заменяется полной подсказкой, предыдущие токены сохраняются с запятой
// ("defaults,comp" + Ctrl+N → "defaults,compress" для btrfs).
func TestFormOptionsTokenReplacement(t *testing.T) {
	m := testFormModel(t, testFormDevices())
	// Три Tab: фокус на options (3); тип ФС задаём напрямую — поле 2 сейчас
	// не в фокусе, ввод через Update ушёл бы в другое поле.
	for i := 0; i < 3; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(model)
	}
	m.form.inputs[2].SetValue("btrfs")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("defaults,comp")})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	if got := m.form.inputs[3].Value(); got != "defaults,compress" {
		t.Fatalf("поле options: %q, ожидалось %q", got, "defaults,compress")
	}
}

// TestFormHintResetOnFocusChange проверяет сброс автодополнения при смене
// фокуса: выбранный индекс и список пересчитываются под новое поле.
func TestFormHintResetOnFocusChange(t *testing.T) {
	m := testFormModel(t, testFormDevices())
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(model)
	if m.form.hintIdx != 0 {
		t.Fatalf("hintIdx: %d, ожидался 0", m.form.hintIdx)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(model)
	if m.form.focus != 1 {
		t.Fatalf("focus: %d, ожидался 1 (mount)", m.form.focus)
	}
	if len(m.form.hints) != 0 {
		t.Fatalf("подсказок для mount: %d, ожидалось 0", len(m.form.hints))
	}
	if m.form.hintIdx != -1 {
		t.Fatalf("hintIdx после смены фокуса: %d, ожидался -1", m.form.hintIdx)
	}
}

// TestFormHintsPanelRendered проверяет, что панель подсказок попадает во View:
// заголовок с клавишами листания и значение подсказки видны вместе с Info.
func TestFormHintsPanelRendered(t *testing.T) {
	m := testFormModel(t, testFormDevices())
	view := m.View()
	if !strings.Contains(view, "Ctrl+N/Ctrl+P") {
		t.Fatalf("в View нет заголовка подсказок:\n%s", view)
	}
	if !strings.Contains(view, "UUID=BBBB-2222") || !strings.Contains(view, "16G") {
		t.Fatalf("в View нет подсказки устройства:\n%s", view)
	}
	// Без устройств панель скрыта — View как до автодополнения.
	empty := testFormModel(t, nil)
	if strings.Contains(empty.View(), "Ctrl+N/Ctrl+P") {
		t.Fatalf("панель показана без устройств:\n%s", empty.View())
	}
}

// testFormManyDevices собирает n устройств с различимыми UUID — нужны
// для тестов циклического листания и скролла окна панели подсказок.
func testFormManyDevices(n int) []sysutil.BlockDevice {
	devices := make([]sysutil.BlockDevice, 0, n)
	for i := 0; i < n; i++ {
		devices = append(devices, sysutil.BlockDevice{
			Path:   fmt.Sprintf("/dev/sd%c1", 'a'+i),
			Type:   "part",
			FSType: "ext4",
			Size:   "100G",
			UUID:   fmt.Sprintf("UUID-%02d", i),
		})
	}
	return devices
}

// pressHintKey отправляет одну клавишу листания подсказок и возвращает модель.
func pressHintKey(m model, key tea.KeyType) model {
	updated, _ := m.Update(tea.KeyMsg{Type: key})
	return updated.(model)
}

// TestFormHintWrapAround проверяет циклическое листание по полному списку:
// Ctrl+N с последнего индекса оборачивается на первый, Ctrl+P с -1 и с 0
// уходит на последний вариант.
func TestFormHintWrapAround(t *testing.T) {
	devices := testFormManyDevices(3)

	// Ctrl+N: -1 → 0 → 1 → 2 → обёртка на 0.
	m := testFormModel(t, devices)
	for i := 0; i < 3; i++ {
		m = pressHintKey(m, tea.KeyCtrlN)
	}
	if m.form.hintIdx != 2 {
		t.Fatalf("hintIdx после 3×Ctrl+N: %d, ожидался 2 (последний)", m.form.hintIdx)
	}
	m = pressHintKey(m, tea.KeyCtrlN)
	if m.form.hintIdx != 0 {
		t.Fatalf("hintIdx после обёртки: %d, ожидался 0", m.form.hintIdx)
	}
	if got := m.form.inputs[0].Value(); got != "UUID=UUID-00" {
		t.Fatalf("поле device после обёртки: %q, ожидалось первое устройство", got)
	}

	// Ctrl+P с -1 (ничего не выбрано) — сразу последний вариант.
	m = testFormModel(t, devices)
	m = pressHintKey(m, tea.KeyCtrlP)
	if m.form.hintIdx != 2 {
		t.Fatalf("hintIdx после Ctrl+P с -1: %d, ожидался 2 (последний)", m.form.hintIdx)
	}
	if got := m.form.inputs[0].Value(); got != "UUID=UUID-02" {
		t.Fatalf("поле device: %q, ожидалось последнее устройство", got)
	}

	// Ctrl+P с 0 — тоже последний вариант (шаг назад по кругу).
	m = pressHintKey(m, tea.KeyCtrlP) // 2 → 1
	m = pressHintKey(m, tea.KeyCtrlP) // 1 → 0
	if m.form.hintIdx != 0 {
		t.Fatalf("hintIdx: %d, ожидался 0", m.form.hintIdx)
	}
	m = pressHintKey(m, tea.KeyCtrlP)
	if m.form.hintIdx != 2 {
		t.Fatalf("hintIdx после Ctrl+P с 0: %d, ожидался 2 (последний)", m.form.hintIdx)
	}
}

// TestFormHintsWindowScrollsWithCursor проверяет скролл окна панели: при
// листании за пределы maxFormHints строк выбранный вариант остаётся виден в
// View (окно догоняет курсор), а первый вариант уходит за верхнюю границу.
func TestFormHintsWindowScrollsWithCursor(t *testing.T) {
	devices := testFormManyDevices(15)
	m := testFormModel(t, devices)
	if len(m.form.hints) != 15 {
		t.Fatalf("подсказок: %d, ожидалось 15", len(m.form.hints))
	}
	// 13 раз Ctrl+N: hintIdx = 12 — ниже окна в 10 строк.
	for i := 0; i < 13; i++ {
		m = pressHintKey(m, tea.KeyCtrlN)
	}
	if m.form.hintIdx != 12 {
		t.Fatalf("hintIdx: %d, ожидался 12", m.form.hintIdx)
	}
	view := m.View()
	selected := m.form.hints[12].Value // "UUID=UUID-12"
	if !strings.Contains(view, selected) {
		t.Fatalf("выбранный вариант %q не виден в View (окно не скроллится):\n%s", selected, view)
	}
	if strings.Contains(view, m.form.hints[0].Value) {
		t.Fatalf("первый вариант %q всё ещё виден — окно не сдвинулось:\n%s",
			m.form.hints[0].Value, view)
	}
}
