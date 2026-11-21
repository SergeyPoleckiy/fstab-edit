package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/writer"
)

// keyRunes собирает клавишу из рун для прямого вызова Update вне псевдо-TTY.
func keyRunes(runes ...rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: runes} }

// createTestBackup перезаписывает target заданным содержимым и создаёт
// production-копию через backup.Create с фиксированным временем. Каталог и
// prefix берутся по умолчанию (рядом с target, fstab.bak.*), что совпадает с
// пустыми backupSettings тестовых options.
func createTestBackup(t *testing.T, target, content string, moment time.Time) backup.Backup {
	t.Helper()
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatalf("записать target: %v", err)
	}
	snapshot, err := writer.ReadSnapshot(target)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	created, err := backup.Create(snapshot, backup.CreateOptions{
		Operation:          backup.OperationManual,
		SourceVerification: backup.SourceNotChecked,
		Now:                moment,
	})
	if err != nil {
		t.Fatalf("backup.Create: %v", err)
	}
	return created
}

// stubVerifyTarget заменяет production post-verify на заглушку «проверки нет»:
// пустой sysutil.Verification без диагностик не имеет ошибок, поэтому
// cliapp.Rollback не падает на post-verify без запуска findmnt.
func stubVerifyTarget(m *model) {
	m.deps().VerifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		return sysutil.Verification{}
	}
}

func TestLoadHistoryListsBackups(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")
	older := createTestBackup(t, target, "UUID=aaa / ext4 defaults 0 1\n",
		time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	newer := createTestBackup(t, target, "UUID=bbb / ext4 defaults 0 1\n",
		time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC))

	m := testModel(t, testOptions(t, target))
	got := loadHistory(&m)
	if got.Err != nil {
		t.Fatalf("loadHistory: нежданная ошибка: %v", got.Err)
	}
	if len(got.Backups) != 2 {
		t.Fatalf("ожидалось 2 копии, got %d", len(got.Backups))
	}
	// Порядок newest-first гарантирует backup.List: первая строка — свежая копия.
	if got.Backups[0].ID != newer.ID || got.Backups[1].ID != older.ID {
		t.Fatalf("порядок не newest-first: [0]=%s [1]=%s, newer=%s older=%s",
			got.Backups[0].ID, got.Backups[1].ID, newer.ID, older.ID)
	}
}

func TestLoadHistoryEmptyIsNotError(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
	got := loadHistory(&m)
	if got.Err != nil {
		t.Fatalf("пустой список не является сбоем: %v", got.Err)
	}
	if len(got.Backups) != 0 {
		t.Fatalf("ожидался пустой список, got %d записей", len(got.Backups))
	}
}

func TestLoadHistoryFailure(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
	m.deps().ListBackups = func(string, string, string) ([]backup.Backup, error) {
		return nil, errors.New("прочитать backup directory: отказано")
	}
	got := loadHistory(&m)
	if got.Err == nil {
		t.Fatal("ожидалась ошибка ListBackups в historyModel.Err")
	}
	if len(got.Backups) != 0 {
		t.Fatalf("при сбое список должен оставаться пустым, got %d", len(got.Backups))
	}
}

func TestRestoreSelectedSuccess(t *testing.T) {
	const v1 = "UUID=aaa / ext4 defaults 0 1\n"
	const v2 = "UUID=bbb /mnt/data ext4 rw 0 2\n"
	const v3 = "UUID=ccc /mnt/tmp ext4 noauto 0 0\n"
	target := writeTestFstab(t, v1)
	older := createTestBackup(t, target, v1, time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	createTestBackup(t, target, v2, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC))
	// Текущий target отличается от обеих копий — restore должен вернуть v1.
	if err := os.WriteFile(target, []byte(v3), 0o600); err != nil {
		t.Fatalf("записать target: %v", err)
	}

	m := testModel(t, testOptions(t, target))
	stubVerifyTarget(&m)
	result := restoreSelected(context.Background(), &m, older)
	if result.Err != nil {
		t.Fatalf("restoreSelected: нежданная ошибка: %v", result.Err)
	}
	if result.Result.SafetyBackup.ID == "" {
		t.Fatal("safety backup обязан быть создан до replace")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target после restore: %v", err)
	}
	if !bytes.Equal(data, []byte(v1)) {
		t.Fatalf("target после restore: %q, ожидалось %q", data, v1)
	}
	// Сессия сброшена reloadFromDisk-семантикой: original = байты target,
	// dirty=false, документ перечитан из восстановленных байт.
	if m.session == nil {
		t.Fatal("сессия должна быть пересоздана после restore")
	}
	if !bytes.Equal(m.session.original, []byte(v1)) {
		t.Fatalf("session.original: %q, ожидалось %q", m.session.original, v1)
	}
	if m.session.dirty {
		t.Fatal("dirty должен быть false после restore")
	}
	if m.document == nil || !strings.Contains(string(m.document.Serialize()), "UUID=aaa") {
		t.Fatalf("документ не перечитан из восстановленного target: %+v", m.document)
	}
}

func TestRestoreSelectedFailureKeepsTarget(t *testing.T) {
	const v1 = "UUID=aaa / ext4 defaults 0 1\n"
	const v2 = "UUID=bbb /mnt/data ext4 rw 0 2\n"
	target := writeTestFstab(t, v1)
	older := createTestBackup(t, target, v1, time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	if err := os.WriteFile(target, []byte(v2), 0o600); err != nil {
		t.Fatalf("записать target: %v", err)
	}

	m := testModel(t, testOptions(t, target))
	stubVerifyTarget(&m)
	// Подменяем seam Replace: сбой записи, target обязан остаться нетронутым.
	m.deps().Replace = func(string, []byte) (writer.ReplaceResult, error) {
		return writer.ReplaceResult{}, errors.New("rename: отказано")
	}
	result := restoreSelected(context.Background(), &m, older)
	if result.Err == nil {
		t.Fatal("ожидался ExitError при сбое Replace")
	}
	if result.Err.Code != cliapp.ExitIO {
		t.Fatalf("Code = %d, want %d (ExitIO)", result.Err.Code, cliapp.ExitIO)
	}
	if result.Result.SafetyBackup.ID == "" {
		t.Fatal("safety backup обязан существовать даже при сбое replace")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !bytes.Equal(data, []byte(v2)) {
		t.Fatalf("target при сбое replace изменился: %q, ожидалось %q", data, v2)
	}
}

// TestHistoryScreenFlow разворачивает сквозной сценарий экрана: вход по 'h',
// подтверждение restore (default No), отмена и подтверждение, итог в статусе.
func TestHistoryScreenFlow(t *testing.T) {
	const v1 = "UUID=aaa / ext4 defaults 0 1\n"
	const v2 = "UUID=bbb /mnt/data ext4 rw 0 2\n"
	target := writeTestFstab(t, v1)
	older := createTestBackup(t, target, v1, time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	if err := os.WriteFile(target, []byte(v2), 0o600); err != nil {
		t.Fatalf("записать target: %v", err)
	}

	m := testModel(t, testOptions(t, target))
	stubVerifyTarget(&m)
	updated, _ := m.Update(keyRunes('h'))
	m = updated.(model)
	if m.ScreenNow() != ScreenHistory {
		t.Fatalf("'h' должен открывать историю, screen=%v", m.ScreenNow())
	}
	if view := m.View(); !strings.Contains(view, older.ID) {
		t.Fatalf("экран истории не показывает ID копии %q", older.ID)
	}

	// 'r' — модальное подтверждение (default No); 'n' отменяет restore.
	updated, _ = m.Update(keyRunes('r'))
	m = updated.(model)
	if !strings.Contains(m.View(), "history_confirm_restore") {
		t.Fatal("после 'r' должно показываться подтверждение restore")
	}
	updated, _ = m.Update(keyRunes('n'))
	m = updated.(model)
	if m.ScreenNow() != ScreenHistory {
		t.Fatalf("отмена restore должна оставать на истории, screen=%v", m.ScreenNow())
	}
	data, _ := os.ReadFile(target)
	if !bytes.Equal(data, []byte(v2)) {
		t.Fatalf("отмена restore изменила target: %q", data)
	}

	// Повторный 'r' → 'y': restore выполнен, итог — статус, экран — таблица.
	updated, _ = m.Update(keyRunes('r'))
	m = updated.(model)
	updated, _ = m.Update(keyRunes('y'))
	m = updated.(model)
	if m.ScreenNow() != ScreenTable {
		t.Fatalf("после restore ожидалась таблица, screen=%v", m.ScreenNow())
	}
	if !strings.Contains(m.status, "history_restore_ok") {
		t.Fatalf("статус после restore: %q", m.status)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !bytes.Equal(data, []byte(v1)) {
		t.Fatalf("target после restore: %q, ожидалось %q", data, v1)
	}
}

// TestHistoryCursorClamped проверяет навигацию списка: курсор зажат в границах
// и выбор корректно следует за курсором.
func TestHistoryCursorClamped(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")
	first := createTestBackup(t, target, "UUID=aaa / ext4 defaults 0 1\n",
		time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	second := createTestBackup(t, target, "UUID=bbb / ext4 defaults 0 1\n",
		time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC))

	m := testModel(t, testOptions(t, target))
	updated, _ := m.Update(keyRunes('h'))
	m = updated.(model)
	if got := m.history.Selected(); got.ID != second.ID {
		t.Fatalf("курсор по умолчанию на первой строке (newest): %s", got.ID)
	}
	updated, _ = m.Update(keyRunes('j'))
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.history.Cursor != 1 {
		t.Fatalf("курсор после j+down: %d, want 1", m.history.Cursor)
	}
	if got := m.history.Selected(); got.ID != first.ID {
		t.Fatalf("Selected после перехода вниз: %s, want %s", got.ID, first.ID)
	}
	// Зажим внизу: курсор не уходит за последнюю строку.
	updated, _ = m.Update(keyRunes('j'))
	m = updated.(model)
	if m.history.Cursor != 1 {
		t.Fatalf("курсор должен зажиматься внизу: %d, want 1", m.history.Cursor)
	}
	// Зажим вверху.
	updated, _ = m.Update(keyRunes('k'))
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.history.Cursor != 0 {
		t.Fatalf("курсор должен зажиматься вверху: %d, want 0", m.history.Cursor)
	}
}

// TestHistoryDiffKeyOpensDiff проверяет 'd' в истории: diff текущего target
// против выбранной копии открывается в общем экране ScreenDiff.
func TestHistoryDiffKeyOpensDiff(t *testing.T) {
	const v1 = "UUID=aaa / ext4 defaults 0 1\n"
	const v2 = "UUID=bbb /mnt/data ext4 rw 0 2\n"
	target := writeTestFstab(t, v1)
	createTestBackup(t, target, v1, time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	if err := os.WriteFile(target, []byte(v2), 0o600); err != nil {
		t.Fatalf("записать target: %v", err)
	}

	m := testModel(t, testOptions(t, target))
	updated, _ := m.Update(keyRunes('h'))
	m = updated.(model)
	updated, _ = m.Update(keyRunes('d'))
	m = updated.(model)
	if m.ScreenNow() != ScreenDiff {
		t.Fatalf("'d' в истории должен открывать diff, screen=%v", m.ScreenNow())
	}
	if len(m.diff.lines) == 0 {
		t.Fatal("diff target против копии непуст")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.ScreenNow() != ScreenTable {
		t.Fatalf("Esc из diff возвращает на таблицу, screen=%v", m.ScreenNow())
	}
}

// TestHistoryEmptyAndErrorViews разворачивает отображение пограничных
// состояний: пустой список (не сбой) и сбой загрузки (MarkError).
func TestHistoryEmptyAndErrorViews(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
		updated, _ := m.Update(keyRunes('h'))
		m = updated.(model)
		if view := m.View(); !strings.Contains(view, "history_empty") {
			t.Fatalf("пустой список должен показывать muted-подпись: %q", view)
		}
		// Клавиши действий на пустом списке — без паники и без переходов.
		for _, key := range []rune{'r', 'd'} {
			updated, _ = m.Update(keyRunes(key))
			m = updated.(model)
		}
		if m.ScreenNow() != ScreenHistory {
			t.Fatalf("пустой список: screen=%v", m.ScreenNow())
		}
	})

	t.Run("error", func(t *testing.T) {
		m := testModel(t, testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")))
		m.deps().ListBackups = func(string, string, string) ([]backup.Backup, error) {
			return nil, errors.New("scan failed")
		}
		updated, _ := m.Update(keyRunes('h'))
		m = updated.(model)
		view := m.View()
		// MarkError ставит текстовый маркер "!" — цвет недостаточен (ТЗ §9.4).
		if !strings.Contains(view, "!") || !strings.Contains(view, "history_error") {
			t.Fatalf("сбой загрузки должен рендериться через MarkError: %q", view)
		}
	})
}
