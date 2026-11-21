// Файл views_test.go покрывает рендеринг read-only экранов (монтирования,
// журнал, diff), итога сохранения и подтверждения перезагрузки через прямые
// вызовы View/Update на модели вне псевдо-TTY: сценарии сосредоточены на
// текстовых маркерах (MarkError/MarkWarning/заголовки), потому что цвет
// недостаточен (ТЗ §9.4).
package tui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/transaction"
	"fstab-edit/pkg/models"
)

// TestViewMountsRenders проверяет рендеринг экрана монтирований: заголовок,
// диагностики с текстовым маркером и таблицу записей с пометкой смонтированности.
func TestViewMountsRenders(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
	m.mounts = mountsModel{
		Verification: sysutil.Verification{Report: models.Report{Diagnostics: []models.Diagnostic{
			{Severity: models.SeverityError, Message: "bad option", Line: 1},
			{Severity: models.SeverityWarning, Message: "suspicious type"},
		}}},
		Mounted: map[int]bool{1: true},
	}
	m.screen = ScreenMounts
	view := m.View()
	for _, want := range []string{"Статус монтирований", "bad option (строка 1)", "!", "suspicious type", "UUID=aaa", "+"} {
		if !strings.Contains(view, want) {
			t.Fatalf("эран монтирований не содержит %q: %q", want, view)
		}
	}

	// Сбой проверки — MarkError с причиной, без таблицы.
	m.mounts = mountsModel{Err: errors.New("findmnt boom")}
	view = m.View()
	if !strings.Contains(view, "!") || !strings.Contains(view, "findmnt boom") {
		t.Fatalf("сбой findmnt должен рендериться через MarkError: %q", view)
	}
}

// TestViewLogRenders проверяет рендеринг экрана журнала: события, пустой
// журнал и сбой чтения различаются текстом.
func TestViewLogRenders(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))

	m.logEvents = logModel{Events: []loggerEvent{{Severity: "info", Operation: "add", Target: "/etc/fstab", Result: "success", BackupID: "fstab.bak.x"}}}
	m.screen = ScreenLog
	if view := m.View(); !strings.Contains(view, "Журнал операций") || !strings.Contains(view, "add") {
		t.Fatalf("журнал не показывает события: %q", view)
	}

	m.logEvents = logModel{}
	if view := m.View(); !strings.Contains(view, "Журнал пуст или недоступен") {
		t.Fatalf("пустой журнал не подписан: %q", view)
	}

	m.logEvents = logModel{Err: errors.New("read failed")}
	if view := m.View(); !strings.Contains(view, "!") || !strings.Contains(view, "read failed") {
		t.Fatalf("сбой чтения журнала должен рендериться через MarkError: %q", view)
	}
}

// TestViewDiffRenders проверяет рендеринг diff-экрана: непустой diff и
// подпись пустого состояния.
func TestViewDiffRenders(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))

	m.diff = newDiffModel("+UUID=new /data ext4 defaults 0 2")
	m.screen = ScreenDiff
	if view := m.View(); !strings.Contains(view, "Diff изменений") || !strings.Contains(view, "+UUID=new") {
		t.Fatalf("diff-экран не показывает изменения: %q", view)
	}

	m.diff = newDiffModel("")
	if view := m.View(); !strings.Contains(view, "нет несохранённых изменений") {
		t.Fatalf("пустой diff не подписан: %q", view)
	}
}

// TestUpdateScrollableScrollsAndReturns проверяет прокрутку diff-экрана и
// возврат Esc на таблицу через общий updateScrollable.
func TestUpdateScrollableScrollsAndReturns(t *testing.T) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = strings.Repeat("x", i+1)
	}
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
	m.diff = diffModel{lines: lines}
	m.screen = ScreenDiff

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(model)
	if m.diff.offset != 1 {
		t.Fatalf("offset после 'j' = %d, want 1", m.diff.offset)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(model)
	if m.diff.offset != 1+screenPageSize {
		t.Fatalf("offset после pgdown = %d, want %d", m.diff.offset, 1+screenPageSize)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = updated.(model)
	if m.diff.offset != 1 {
		t.Fatalf("offset после pgup = %d, want 1", m.diff.offset)
	}
	// Прокрутка зажата на верхней границе.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.diff.offset != 0 {
		t.Fatalf("offset после up = %d, want 0", m.diff.offset)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.ScreenNow() != ScreenTable {
		t.Fatalf("Esc не вернул на таблицу: %v", m.ScreenNow())
	}
}

// TestViewSaveResultRendersOutcomes проверяет итог сохранения: успех с backup
// ID, no-op без изменений и ошибку с состоянием rollback/recovery/конфликта.
func TestViewSaveResultRendersOutcomes(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))

	m.lastSave = saveOutcome{Result: transaction.TransactionResult{
		Changed: true,
		Backup:  backup.Backup{ID: "fstab.bak.2026-01-02_10-00-00.000000000Z"},
	}}
	m.screen = ScreenSaveResult
	if view := m.View(); !strings.Contains(view, "Результат сохранения") ||
		!strings.Contains(view, "fstab.bak.2026-01-02_10-00-00.000000000Z") ||
		!strings.Contains(view, "проверка после записи пройдена") {
		t.Fatalf("успех сохранения не показан: %q", view)
	}

	m.lastSave = saveOutcome{Result: transaction.TransactionResult{Changed: false}}
	if view := m.View(); !strings.Contains(view, "изменений нет, файл не перезаписан") {
		t.Fatalf("no-op сохранения не подписан: %q", view)
	}

	m.lastSave = saveOutcome{
		Err: &transaction.TransactionError{Kind: transaction.FailureIO, Err: errors.New("write failed")},
		Result: transaction.TransactionResult{
			RolledBack:    true,
			RollbackError: errors.New("rollback boom"),
			Recovery:      transaction.RecoveryPaths{Target: "/t", Backup: "/b", Metadata: "/m"},
		},
		Conflict: true,
	}
	view := m.View()
	for _, want := range []string{"write failed", "rollback не выполнен: rollback boom",
		"пути восстановления: target=/t backup=/b metadata=/m", "файл изменён извне"} {
		if !strings.Contains(view, want) {
			t.Fatalf("экран ошибки сохранения не содержит %q: %q", want, view)
		}
	}
}

// TestUpdateConfirmReloadReloadsOrCancels проверяет модальное подтверждение
// перезагрузки (F5 при dirty): «y» перечитывает target, иное возвращает на
// таблицу без изменений (default No, ТЗ §15).
func TestUpdateConfirmReloadReloadsOrCancels(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")
	m := testModel(t, testOptions(t, target))
	m.screen = ScreenConfirmReload

	// Отказ: таблица, сессия не перечитана.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(model)
	if m.ScreenNow() != ScreenTable {
		t.Fatalf("отказ должен вернуть на таблицу: %v", m.ScreenNow())
	}

	// Подтверждение: target перечитан, статус фиксирует перезагрузку.
	if err := os.WriteFile(target, []byte("UUID=bbb / ext4 defaults 0 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.screen = ScreenConfirmReload
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(model)
	if m.ScreenNow() != ScreenTable {
		t.Fatalf("подтверждение должно вернуть на таблицу: %v", m.ScreenNow())
	}
	if !strings.Contains(m.status, "файл перезагружен") {
		t.Fatalf("статус после перезагрузки: %q", m.status)
	}
	if m.document == nil || !strings.Contains(string(m.document.Serialize()), "UUID=bbb") {
		t.Fatalf("документ не перечитан из target")
	}
}

// TestFormDiffText покрывает предпросмотр diff формы: операция применяется к
// копии документа, живой документ не меняется.
func TestFormDiffText(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
	f := newAddForm()
	values := []string{"UUID=zzz", "/data", "ext4", "rw", "0", "2"}
	for i, v := range values {
		f.inputs[i].SetValue(v)
	}
	got := f.diffText(m.document)
	if !strings.Contains(got, "+UUID=zzz") {
		t.Fatalf("diff формы не содержит добавляемую запись: %q", got)
	}
	if strings.Contains(string(m.document.Serialize()), "UUID=zzz") {
		t.Fatal("diffText изменил живой документ")
	}
	if got := f.diffText(nil); got != "" {
		t.Fatalf("diffText при nil-документе: %q", got)
	}
}

// TestConfirmQuitBranching покрывает ветвление подтверждения выхода (ТЗ §9.2):
// чистая сессия завершается немедленно, dirty — через модальное подтверждение
// (default No: любая клавиша кроме подтверждения возвращает на таблицу).
func TestConfirmQuitBranching(t *testing.T) {
	// Чистая сессия: 'q' завершает программу без подтверждения.
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(model)
	if cmd == nil || m.ScreenNow() != ScreenTable {
		t.Fatalf("чистая сессия: ожидался немедленный quit, screen=%v cmd=%v", m.ScreenNow(), cmd)
	}

	// Dirty-сессия: 'q'/ctrl+q/f10/ctrl+c открывают подтверждение, quit не выдан.
	dirty := func() model {
		mm := testModel(t, testOptions(t, writeTestFstab(t,
			"UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")))
		updated, _ := mm.Update(tea.KeyMsg{Type: tea.KeyDelete})
		mm = updated.(model)
		if !mm.session.dirty {
			t.Fatal("после Del сессия должна стать dirty")
		}
		return mm
	}
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlQ},
		{Type: tea.KeyF10},
		{Type: tea.KeyCtrlC},
	} {
		m := dirty()
		updated, cmd := m.Update(key)
		m = updated.(model)
		if cmd != nil || m.ScreenNow() != ScreenConfirmQuit {
			t.Fatalf("%v: ожидался ScreenConfirmQuit без quit, screen=%v cmd=%v",
				key.String(), m.ScreenNow(), cmd)
		}
	}

	// Отказ ('n') возвращает на таблицу без выхода, dirty-флаг сохраняется.
	m = dirty()
	m.screen = ScreenConfirmQuit
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(model)
	if cmd != nil || m.ScreenNow() != ScreenTable || !m.session.dirty {
		t.Fatalf("отказ: screen=%v cmd=%v dirty=%v", m.ScreenNow(), cmd, m.session.dirty)
	}

	// Подтверждение ('y') выдаёт quit; модальный экран рендерит предупреждение.
	m = dirty()
	m.screen = ScreenConfirmQuit
	if view := m.View(); !strings.Contains(view, "есть несохранённые изменения") {
		t.Fatalf("модальный экран выхода: %q", view)
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("подтверждение выхода не выдало quit")
	}
}

// TestQuitWithUnsavedChangesConfirm — сквозной сценарий: мутация, выход по 'q'
// запрашивает подтверждение, отказ сохраняет несохранённое изменение в сессии,
// повторный выход с подтверждением завершает программу; target на диске не
// изменился.
func TestQuitWithUnsavedChangesConfirm(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	m := NewModel(testOptions(t, target))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "/mnt/data")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Del — удаление первой записи, dirty-индикатор в заголовке.
	tm.Send(tea.KeyMsg{Type: tea.KeyDelete})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return !strings.Contains(s, "UUID=aaa") && strings.Contains(s, "*")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// 'q' — модальное подтверждение выхода (default No).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "есть несохранённые изменения")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Отказ: сессия вернулась на таблицу, удаление осталось несохранённым.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return !strings.Contains(s, "UUID=aaa") && strings.Contains(s, "*") &&
			!strings.Contains(s, "есть несохранённые изменения")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Повторный выход с подтверждением завершает программу.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "есть несохранённые изменения")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !strings.Contains(string(data), "UUID=aaa") {
		t.Fatalf("target на диске изменился несохранённой сессией: %q", data)
	}
}
