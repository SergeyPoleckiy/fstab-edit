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

// testDeps собирает production-границы отказа с двумя подменами: реальный
// ReadSnapshot (нужен для загрузки документа) и ExecuteTransaction-заглушка,
// возвращающая ошибку, потому что скелет Task 1 ничего не сохраняет.
func testDeps(t *testing.T) *Deps {
	t.Helper()
	deps := ProductionDeps(logger.Options{})
	deps.ReadSnapshot = writer.ReadSnapshot
	deps.ExecuteTransaction = func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error) {
		return transaction.TransactionResult{}, errors.New("сохранение недоступно в скелете Task 1")
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
