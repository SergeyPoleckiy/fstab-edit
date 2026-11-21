package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/sysutil"
	"fstab-edit/pkg/models"
)

// scriptedRunner — скриптованная заглушка sysutil.CommandRunner: результат
// подбирается по объединённым аргументам. Точное совпадение ключа preferred;
// иначе выбирается самый длинный ключ-prefix, чтобы matчить вызовы с
// непредсказуемыми путями (findmnt --verify --tab-file <tempdir>/...).
// Определена здесь, а не в app_test.go, чтобы не менять чужие тестовые helper'ы.
type scriptedRunner map[string]sysutil.RunResult

// Run возвращает скриптованный результат без запуска процесса.
func (s scriptedRunner) Run(_ context.Context, _ string, args ...string) sysutil.RunResult {
	joined := strings.Join(args, " ")
	if result, ok := s[joined]; ok {
		return result
	}
	match, matched := "", false
	for prefix := range s {
		if strings.HasPrefix(joined, prefix) && (!matched || len(prefix) > len(match)) {
			match, matched = prefix, true
		}
	}
	if matched {
		return s[match]
	}
	return sysutil.RunResult{ExitCode: 0}
}

// testModel собирает модель через NewModel и возвращает её значение для
// прямых вызовов load*/Update/View вне псевдо-TTY teatest.
func testModel(t *testing.T, opts Options) model {
	t.Helper()
	m, ok := NewModel(opts).(model)
	if !ok {
		t.Fatalf("NewModel вернул %T, ожидался model", m)
	}
	return m
}

func TestNewDiffModelAndScroll(t *testing.T) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = fmt.Sprintf("diff-line-%02d", i)
	}
	d := newDiffModel(strings.Join(lines, "\n"))
	if len(d.lines) != 50 {
		t.Fatalf("newDiffModel: ожидалось 50 строк, got %d", len(d.lines))
	}
	d.scroll(10)
	if d.offset != 10 {
		t.Fatalf("scroll(+10): offset=%d, want 10", d.offset)
	}
	d.scroll(-10)
	if d.offset != 0 {
		t.Fatalf("scroll(-10): offset=%d, want 0", d.offset)
	}
	d.scroll(-5) // за нижнюю границу — зажат в 0 без паники
	if d.offset != 0 {
		t.Fatalf("нижняя граница: offset=%d, want 0", d.offset)
	}
	d.scroll(1000) // за верхнюю границу — зажат в len-1
	if d.offset != 49 {
		t.Fatalf("верхняя граница: offset=%d, want 49", d.offset)
	}
}

func TestLoadMountsWithFakeRunner(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n/dev/sdb1 /mnt/usb vfat noauto,user 0 0\n")
	opts := testOptions(t, target)
	opts.Runner = scriptedRunner{
		"--verify --tab-file":                   {ExitCode: 0},
		"--noheadings --output=TARGET /mnt/usb": {ExitCode: 1},
		"--noheadings --output=TARGET /":        {ExitCode: 0, Stdout: "/\n"},
	}
	m := testModel(t, opts)

	got := loadMounts(context.Background(), &m)
	if got.Err != nil {
		t.Fatalf("loadMounts: нежданная ошибка: %v", got.Err)
	}
	for _, d := range got.Verification.Report.Diagnostics {
		if d.Severity == models.SeverityError {
			t.Fatalf("Report содержит ошибку: %+v", d)
		}
	}
	// Физические строки 1 и 2: «/» смонтирован, «/mnt/usb» — нет.
	if !got.Mounted[1] {
		t.Error("Mounted[1] (/) должен быть true")
	}
	if got.Mounted[2] {
		t.Error("Mounted[2] (/mnt/usb) должен быть false")
	}
}

func TestLoadMountsRunnerError(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")
	opts := testOptions(t, target)
	// Ненулевой exit без ошибки запуска не считается сбоем runner'а
	// (findmnt.failed остаётся в Report), поэтому фиксируем именно Err.
	opts.Runner = scriptedRunner{
		"--verify --tab-file": {ExitCode: 2, Err: errors.New("findmnt: exit status 2")},
	}
	m := testModel(t, opts)

	got := loadMounts(context.Background(), &m)
	if got.Err == nil {
		t.Fatal("ожидалась ошибка runner'а в mountsModel.Err")
	}
	if len(got.Mounted) != 0 {
		t.Fatalf("Mounted должен остаться пустым, got %v", got.Mounted)
	}
}

func TestLoadLogParsesJSONLines(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "fstab-edit.log")
	lines := []string{
		`{"severity":"info","timestamp":"2026-11-21T09:00:00Z","operation":"edit","target":"/tmp/a","backup_id":"b1","result":"success"}`,
		`{"severity":"info","timestamp":"2026-11-21T09:05:00Z","operation":"edit","target":"/tmp/a","backup_id":"b2","result":"success"}`,
		`{"severity":"error","timestamp":"2026-11-21T09:10:00Z","operation":"edit","target":"/tmp/a","backup_id":"","result":"error","error_code":6}`,
	}
	if err := os.WriteFile(logFile, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("записать журнал: %v", err)
	}
	opts := testOptions(t, writeTestFstab(t, "UUID=a / ext4 defaults 0 1\n"))
	opts.Log.File = logFile
	m := testModel(t, opts)

	got := loadLog(&m, 2)
	if got.Err != nil {
		t.Fatalf("loadLog: нежданная ошибка: %v", got.Err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("ожидалось ровно 2 события (limit), got %d", len(got.Events))
	}
	// Порядок newest-last: последнее событие — самая свежая строка файла.
	last := got.Events[1]
	if last.Severity != "error" || last.Result != "error" || last.Operation != "edit" {
		t.Fatalf("поля последнего события не заполнены: %+v", last)
	}
	if got.Events[0].BackupID != "b2" {
		t.Fatalf("порядок не newest-last: %+v", got.Events[0])
	}
}

func TestLoadLogSkipsBrokenLines(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "fstab-edit.log")
	content := strings.Join([]string{
		`{"severity":"info","timestamp":"2026-11-21T09:00:00Z","operation":"edit","target":"/tmp/a","result":"success"}`,
		`not-a-json-line{{{`,
		`{"severity":"error","timestamp":"2026-11-21T09:10:00Z","operation":"edit","target":"/tmp/a","result":"error"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(logFile, []byte(content), 0o600); err != nil {
		t.Fatalf("записать журнал: %v", err)
	}
	opts := testOptions(t, writeTestFstab(t, "UUID=a / ext4 defaults 0 1\n"))
	opts.Log.File = logFile
	m := testModel(t, opts)

	got := loadLog(&m, 10)
	if got.Err != nil {
		t.Fatalf("loadLog: нежданная ошибка: %v", got.Err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("битая строка должна пропускаться: событий %d, want 2", len(got.Events))
	}
}

func TestLoadLogMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.log")
	opts := testOptions(t, writeTestFstab(t, "UUID=a / ext4 defaults 0 1\n"))
	opts.Log.File = missing
	m := testModel(t, opts)

	got := loadLog(&m, 10)
	if got.Err == nil {
		t.Fatal("отсутствующий файл должен давать logModel.Err")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only экран не должен создавать файл журнала")
	}
}

func TestHelpScreenContainsKeys(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")
	m := testModel(t, testOptions(t, target))
	// Переход через прямой вызов Update: '?' — тот же путь, что и в TTY.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	view := updated.(model).View()
	for _, want := range []string{"Ctrl+S", "F10", "M", "H", "L"} {
		if !strings.Contains(view, want) {
			t.Errorf("экран помощи не содержит %q", want)
		}
	}
}
