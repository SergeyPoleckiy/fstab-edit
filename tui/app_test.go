package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"fstab-edit/internal/logger"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/transaction"
	"fstab-edit/internal/writer"
	"fstab-edit/pkg/models"
)

// fakeRunner — заглушка sysutil.CommandRunner: любой вызов считается успешным
// с пустым выводом. Скелет Task 1 не запускает внешние утилиты, поэтому
// достаточно безусловного успеха.
type fakeRunner struct{}

// Run возвращает успешный пустой результат без запуска процесса.
func (fakeRunner) Run(context.Context, string, ...string) sysutil.RunResult {
	return sysutil.RunResult{ExitCode: 0}
}

// writeTestFstab пишет содержимое fstab во временный файл и возвращает его путь.
// Тесты никогда не читают реальный /etc/fstab: target всегда в t.TempDir().
func writeTestFstab(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeTestFstab: %v", err)
	}
	return path
}

// testDeps собирает production-границы отказа с подменами: реальный
// ReadSnapshot (нужен для загрузки документа), ExecuteTransaction-заглушка,
// потому что тесты ничего не сохраняют, и ListBlockDevices-заглушка с пустым
// списком: форма открывается без панели подсказок, и реальный lsblk в тестах
// не выполняется. Сценарии, проверяющие устройства, подменяют seam ещё раз.
func testDeps(t *testing.T) *Deps {
	t.Helper()
	deps := ProductionDeps(logger.Options{})
	deps.ReadSnapshot = writer.ReadSnapshot
	deps.ExecuteTransaction = func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error) {
		return transaction.TransactionResult{}, errors.New("сохранение недоступно в тестах")
	}
	deps.ListBlockDevices = func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error) {
		return nil, nil
	}
	return &deps
}

// testOptions собирает Options для тестов: tempdir target, fake deps.
func testOptions(t *testing.T, target string) Options {
	t.Helper()
	return Options{
		Target: target,
		Lang:   "ru",
		Theme:  "dark",
		Runner: fakeRunner{}, // fakeRunner — заглушка sysutil.CommandRunner
		Deps:   testDeps(t),
	}
}

// TestAppShowsTableAndQuits проверяет скелет сквозным тестом: таблица показывает
// записи документа, а "q" без dirty-изменений завершает программу без подтверждения.
func TestAppShowsTableAndQuits(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n/dev/sdb1 /mnt/usb vfat noauto,user 0 0\n")
	m := NewModel(testOptions(t, target))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "/mnt/usb")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))
	// q без dirty-изменений завершает программу без подтверждения.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestDeleteAndUndoFlow проверяет сквозной цикл сессии: 'd' удаляет запись под
// курсором (вторая строка исчезает из таблицы, в заголовке появляется "*"),
// ctrl+z откатывает мутацию (строка возвращается, "*" исчезает).
func TestDeleteAndUndoFlow(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	m := NewModel(testOptions(t, target))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "/mnt/data")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Del — delete: запись под курсором (первая) удалена, вторая осталась.
	// Клавиша 'd' с Task 5 открывает экран diff (Task 5), удаление — по ТЗ §5.2
	// на Del; сценарий удаления/undo сохранён без изменений.
	// Dirty-индикатор "*" появился в заголовке.
	tm.Send(tea.KeyMsg{Type: tea.KeyDelete})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return !strings.Contains(s, "UUID=aaa") && strings.Contains(s, "*")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// ctrl+z — undo: первая запись вернулась, dirty-индикатор исчез.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlZ})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "UUID=aaa") && !strings.Contains(s, "*")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestBlockDevicesFlow проверяет сквозной сценарий экрана устройств: клавиша B
// из таблицы открывает экран с данными подменённого seam (реальный lsblk в
// тестах не выполняется), Esc возвращает на таблицу, target на диске не тронут.
func TestBlockDevicesFlow(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	opts := testOptions(t, target)
	opts.Deps.ListBlockDevices = func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error) {
		return []sysutil.BlockDevice{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part", FSType: "vfat",
				Size: "16G", Label: "USBKEY", UUID: "BBBB-2222", MountPoint: "/mnt/usb"},
		}, nil
	}
	m := NewModel(opts)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "/mnt/data")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// B — экран блочных устройств: показывает устройство из fake seam.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "Блочные устройства") && strings.Contains(s, "/dev/sdb1") &&
			strings.Contains(s, "USBKEY")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Esc — назад в таблицу записей.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "/mnt/data") && !strings.Contains(s, "Блочные устройства")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Чистая сессия: выход без подтверждения; target не изменён.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !strings.Contains(string(data), "UUID=bbb") {
		t.Fatalf("target на диске изменился read-only экраном: %q", data)
	}
}

// TestScreenKeysBothCases проверяет, что экраны монтирований (M), истории (H),
// журнала (L) и устройств (B) открываются в обоих регистрах: Shift-вариант
// отдаёт rune 'M'/'H'/'L', обычная клавиша — "m"/"h"/"l"; read-only экраны не
// должны требовать layout-переключения. Esc возвращает на таблицу.
func TestScreenKeysBothCases(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	cases := []struct {
		lower, upper rune
		screen       Screen
	}{
		{'m', 'M', ScreenMounts},
		{'h', 'H', ScreenHistory},
		{'l', 'L', ScreenLog},
		{'b', 'B', ScreenBlockDevices},
	}
	press := func(m model, r rune) model {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		return updated.(model)
	}
	for _, tc := range cases {
		for _, r := range []rune{tc.lower, tc.upper} {
			m := testModel(t, testOptions(t, target))
			m = press(m, r)
			if m.ScreenNow() != tc.screen {
				t.Fatalf("клавиша %q: экран %v, ожидался %v", r, m.ScreenNow(), tc.screen)
			}
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m = updated.(model)
			if m.ScreenNow() != ScreenTable {
				t.Fatalf("клавиша %q: Esc не вернул таблицу: %v", r, m.ScreenNow())
			}
		}
	}
}

// активная запись комментируется (Kind → NodeCommentedEntry), закомментированная
// включается (Kind → NodeEntry); обе мутации проходят через applyOp, поэтому
// dirty выставлен, а ctrl+z откатывает изменения в исходном порядке.
func TestCommentToggleBranches(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n#UUID=bbb /mnt/data ext4 rw 0 2\n")
	m := testModel(t, testOptions(t, target))
	if m.document.Nodes[0].Kind != models.NodeEntry {
		t.Fatal("первая запись должна быть активной")
	}
	if m.document.Nodes[1].Kind != models.NodeCommentedEntry {
		t.Fatal("вторая запись должна быть закомментированной")
	}

	pressC := func(m model) model {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
		return updated.(model)
	}

	// Курсор на активной записи: 'c' комментирует её.
	m = pressC(m)
	if m.document.Nodes[0].Kind != models.NodeCommentedEntry {
		t.Fatal("'c' на активной записи должна давать NodeCommentedEntry")
	}
	if !m.session.dirty {
		t.Fatal("после комментария dirty обязан быть true")
	}

	// Курсор на закомментированной записи: 'c' включает её обратно.
	m.cursor = 1
	m = pressC(m)
	if m.document.Nodes[1].Kind != models.NodeEntry {
		t.Fatal("'c' на закомментированной записи должна давать NodeEntry")
	}

	// Два undo возвращают документ в исходную сериализацию.
	undo := func(m model) model {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
		return updated.(model)
	}
	m = undo(m)
	if m.document.Nodes[1].Kind != models.NodeCommentedEntry {
		t.Fatal("первый undo должен вернуть закомментированную запись")
	}
	m = undo(m)
	if m.document.Nodes[0].Kind != models.NodeEntry {
		t.Fatal("второй undo должен вернуть активную запись")
	}
	if m.session.dirty {
		t.Fatal("после полного undo dirty должен исчезнуть")
	}
}

// TestCommentToggleFlow проверяет сквозной сценарий клавиши 'c' в TTY: активная
// запись под курсором комментируется (появляется dirty-индикатор "*", в статусе
// — comment_done), затем 'j'+'c' на закомментированной записи включает её.
func TestCommentToggleFlow(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n#UUID=bbb /mnt/data ext4 rw 0 2\n")
	m := NewModel(testOptions(t, target))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "/mnt/data")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// 'c' на первой (активной) записи: dirty-индикатор и статус операции.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "*") && strings.Contains(s, "закомментирована/включена")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// 'j' — курсор на вторую (закомментированную) запись; 'c' включает её:
	// маркер активности второй строки меняется с "-" на "+".
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), " 0 2 +")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Выход с dirty-сессией запрашивает подтверждение (default No, ТЗ §9.2).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "[y/N]")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestCtrlCReturnsFromSubscreens фиксирует правило «ctrl+c на подэкране =
// Esc»: на каждом read-only экране-просмотрщике ctrl+c возвращает на главную
// таблицу, а не завершает программу посреди просмотра.
func TestCtrlCReturnsFromSubscreens(t *testing.T) {
	subscreens := []Screen{
		ScreenDiff, ScreenMounts, ScreenLog, ScreenHelp,
		ScreenBlockDevices, ScreenHistory, ScreenSaveResult,
	}
	for _, s := range subscreens {
		m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
		m.screen = s
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		got := updated.(model)
		if got.ScreenNow() != ScreenTable {
			t.Fatalf("экран %v: ctrl+c не вернул таблицу: %v", s, got.ScreenNow())
		}
		if cmd != nil {
			t.Fatalf("экран %v: ctrl+c не должен выдавать команду, получено %v", s, cmd)
		}
	}
}

// TestCtrlCOnTableKeepsQuitFlow: на главной таблице поведение ctrl+c прежнее —
// чистая сессия завершает программу (команда tea.Quit), а не «возврат на
// таблицу». Dirty-вариант (подтверждение ScreenConfirmQuit) покрыт отдельно в
// views_test.go.
func TestCtrlCOnTableKeepsQuitFlow(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := updated.(model)
	if got.ScreenNow() != ScreenTable {
		t.Fatalf("ctrl+c на таблице не должен менять экран: %v", got.ScreenNow())
	}
	if cmd == nil {
		t.Fatal("ctrl+c на чистой таблице должен завершать программу (tea.Quit)")
	}
}

// TestBlockDevicesCtrlCAndReloadFlow — сквозной teatest-сценарий экрана
// устройств: 'r' перечитывает список через seam (второе поколение данных
// заменяет первое без выхода с экрана), ctrl+c возвращает на таблицу как Esc.
func TestBlockDevicesCtrlCAndReloadFlow(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	calls := 0
	opts := testOptions(t, target)
	opts.Deps.ListBlockDevices = func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error) {
		calls++
		if calls == 1 {
			return []sysutil.BlockDevice{
				{Name: "sda", Path: "/dev/sda", Type: "disk", Size: "1G"},
			}, nil
		}
		return []sysutil.BlockDevice{
			{Name: "nvme0n1", Path: "/dev/nvme0n1", Type: "disk", Size: "2T"},
		}, nil
	}
	m := NewModel(opts)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "/mnt/data")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// B — экран устройств, первое поколение данных.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "Блочные устройства") && strings.Contains(s, "/dev/sda")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// 'r' — перечитывание списка без выхода с экрана: второе поколение на месте.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "/dev/nvme0n1") && !strings.Contains(s, "/dev/sda")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Ctrl+C — возврат на таблицу (как Esc): записи fstab снова видны.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "/mnt/data") && !strings.Contains(s, "Блочные устройства")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Чистая сессия: выход без подтверждения; target не изменён.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !strings.Contains(string(data), "UUID=bbb") {
		t.Fatalf("target на диске изменился read-only экраном: %q", data)
	}
}

// TestFormCompletionFlow проверяет сквозной сценарий автодополнения в TTY:
// Ins открывает форму с устройствами, подменёнными через seam (реальный lsblk
// не выполняется), Ctrl+N подставляет UUID= первого устройства в поле device,
// Esc отменяет форму без подтверждения, чистая сессия выходит по 'q', target
// на диске не тронут.
func TestFormCompletionFlow(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	opts := testOptions(t, target)
	opts.Deps.ListBlockDevices = func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error) {
		return []sysutil.BlockDevice{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part", FSType: "ext4",
				Size: "256G", Label: "DATA", UUID: "CCCC-3333"},
		}, nil
	}
	m := NewModel(opts)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "/mnt/data")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Ins — форма добавления: устройства загружены при открытии, панель
	// подсказок видна вместе с клавишами листания.
	tm.Send(tea.KeyMsg{Type: tea.KeyInsert})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "Новая запись") && strings.Contains(s, "Ctrl+N/Ctrl+P")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Ctrl+N — первая подсказка устройства подставлена в поле device.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlN})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(string(b), "UUID=CCCC-3333")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Esc — отмена формы без подтверждения: возврат на таблицу, сессия чистая.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		s := string(b)
		return strings.Contains(s, "/mnt/data") && !strings.Contains(s, "Новая запись")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !strings.Contains(string(data), "UUID=bbb") {
		t.Fatalf("target на диске изменился формой: %q", data)
	}
}
