package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fstab-edit/internal/editor"
	"fstab-edit/internal/logger"
	"fstab-edit/internal/transaction"
	"fstab-edit/pkg/models"
)

// saveTestOptions собирает Options для save-тестов: tempdir target с journal-
// файлом, production-границы отказа (реальный transaction.Execute), backup-файлы
// падают рядом с target в tempdir.
func saveTestOptions(t *testing.T, target string) Options {
	t.Helper()
	opts := testOptions(t, target)
	opts.Log.File = filepath.Join(t.TempDir(), "fstab-edit.log")
	opts.Backup.Dir = filepath.Join(filepath.Dir(target), "backups")
	if err := os.MkdirAll(opts.Backup.Dir, 0o700); err != nil {
		t.Fatalf("saveTestOptions: создать каталог backup: %v", err)
	}
	deps := ProductionDeps(loggerOptions(opts))
	deps.ExecuteTransaction = transaction.Execute
	opts.Deps = &deps
	return opts
}

// newSaveTestModel собирает *model через NewModel и приводит tea.Model к
// конкретному типу: save и reloadFromDisk — методы model.
func newSaveTestModel(t *testing.T, opts Options) *model {
	t.Helper()
	m, ok := NewModel(opts).(model)
	if !ok {
		t.Fatalf("NewModel вернул %T, ожидался model", m)
	}
	return &m
}

// deleteFirstEntry удаляет первую запись документа через сессию (путь таблицы:
// applyOp фиксирует undo-снапшот и выставляет dirty).
func deleteFirstEntry(t *testing.T, m *model) {
	t.Helper()
	if err := m.session.applyOp(m.document, func(d *models.Document) error {
		_, err := editor.Delete(d, editor.ByLine(1))
		return err
	}); err != nil {
		t.Fatalf("deleteFirstEntry: %v", err)
	}
}

// readLogEvents читает JSON Lines-журнал и разбирает каждую строку.
func readLogEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readLogEvents: прочитать журнал %s: %v", path, err)
	}
	var events []map[string]any
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("readLogEvents: разобрать строку %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

// TestSaveSuccess проверяет сквозное сохранение: мутация → save() записывает
// candidate через transaction, сессия сбрасывается (dirty=false, undo пуст).
func TestSaveSuccess(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	opts := saveTestOptions(t, target)
	m := newSaveTestModel(t, opts)
	deleteFirstEntry(t, m)

	outcome := save(context.Background(), m)
	if outcome.Err != nil {
		t.Fatalf("save: неожиданная ошибка: %v", outcome.Err)
	}
	if !outcome.Result.Changed {
		t.Fatalf("save: Changed=false, ожидалась запись candidate")
	}
	if outcome.Result.RolledBack {
		t.Fatalf("save: RolledBack=true при успехе")
	}
	if !outcome.LogOpened {
		t.Fatalf("save: LogOpened=false, журнал обязан открываться до записи")
	}

	candidate := m.session.candidate(m.document)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !bytes.Equal(data, candidate) {
		t.Fatalf("target != candidate:\ntarget:    %q\ncandidate: %q", data, candidate)
	}
	if m.session.dirty {
		t.Fatalf("dirty=true после успешного сохранения")
	}
	if len(m.session.undoStack) != 0 {
		t.Fatalf("undo-стек не пуст после сохранения: %d снапшотов", len(m.session.undoStack))
	}
	if !bytes.Equal(m.session.original, candidate) {
		t.Fatalf("original не обновлён до candidate после сохранения")
	}

	// Повторный save без мутаций — no-op: target не тронут, Changed=false.
	outcome = save(context.Background(), m)
	if outcome.Err != nil {
		t.Fatalf("повторный save: неожиданная ошибка: %v", outcome.Err)
	}
	if outcome.Result.Changed {
		t.Fatalf("повторный save без мутаций: Changed=true, ожидался no-op")
	}
	data, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !bytes.Equal(data, candidate) {
		t.Fatalf("target изменён повторным no-op save")
	}
}

// TestSaveNoOpWhenUnchanged проверяет, что save без мутаций открывает журнал,
// возвращает Changed=false и не трогает target.
func TestSaveNoOpWhenUnchanged(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")
	original, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	opts := saveTestOptions(t, target)
	m := newSaveTestModel(t, opts)

	outcome := save(context.Background(), m)
	if outcome.Err != nil {
		t.Fatalf("save: неожиданная ошибка: %v", outcome.Err)
	}
	if outcome.Result.Changed {
		t.Fatalf("save без мутаций: Changed=true, ожидался no-op")
	}
	if !outcome.LogOpened {
		t.Fatalf("save: LogOpened=false, журнал открывается даже для no-op")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !bytes.Equal(data, original) {
		t.Fatalf("no-op save изменил target")
	}
}

// TestSaveConflictDoesNotOverwrite проверяет ТЗ §9.2: конфликт версии не
// перезаписывает target и не сбрасывает сессию.
func TestSaveConflictDoesNotOverwrite(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	diskContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	opts := saveTestOptions(t, target)
	conflict := &transaction.TransactionError{
		Kind: transaction.FailureConflict,
		Err:  transaction.ErrTransactionConflict,
	}
	opts.Deps.ExecuteTransaction = func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error) {
		return transaction.TransactionResult{}, conflict
	}
	m := newSaveTestModel(t, opts)
	deleteFirstEntry(t, m)

	outcome := save(context.Background(), m)
	if outcome.Err == nil {
		t.Fatalf("save: ожидалась ошибка конфликта")
	}
	if !outcome.Conflict {
		t.Fatalf("save: Conflict=false при FailureConflict")
	}
	if !errors.Is(outcome.Err, transaction.ErrTransactionConflict) {
		t.Fatalf("save: ошибка %v не содержит sentinel конфликта", outcome.Err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !bytes.Equal(data, diskContent) {
		t.Fatalf("конфликт перезаписал target:\n%q\nожидалось:\n%q", data, diskContent)
	}
	if !m.session.dirty {
		t.Fatalf("dirty сброшен после конфликта: сессия обязана сохранить мутации")
	}
}

// TestSaveLogUnavailableBlocksSave проверяет ТЗ §14: недоступный журнал до
// точки изменения отменяет сохранение до вызова ExecuteTransaction.
func TestSaveLogUnavailableBlocksSave(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n")
	original, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	opts := saveTestOptions(t, target)
	executeCalls := 0
	opts.Deps.ExecuteTransaction = func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error) {
		executeCalls++
		return transaction.TransactionResult{}, nil
	}
	opts.Deps.OpenLog = func() (*logger.Logger, error) {
		return nil, errors.New("журнал недоступен")
	}
	m := newSaveTestModel(t, opts)
	deleteFirstEntry(t, m)

	outcome := save(context.Background(), m)
	if outcome.LogOpened {
		t.Fatalf("LogOpened=true при недоступном журнале")
	}
	if outcome.Err == nil {
		t.Fatalf("save: ожидалась ошибка недоступного журнала")
	}
	if outcome.Err.Kind != transaction.FailureIO {
		t.Fatalf("Kind=%q, ожидался %q (журнал — I/O-граница)", outcome.Err.Kind, transaction.FailureIO)
	}
	if executeCalls != 0 {
		t.Fatalf("ExecuteTransaction вызван %d раз при недоступном журнале: сохранение не отменено до точки изменения", executeCalls)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !bytes.Equal(data, original) {
		t.Fatalf("target изменён при заблокированном сохранении")
	}
}

// TestSaveLogsEvent проверяет ТЗ §14: успешное сохранение пишет ровно одно
// событие журнала с result=success, operation=edit и непустым backup_id.
func TestSaveLogsEvent(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	opts := saveTestOptions(t, target)
	m := newSaveTestModel(t, opts)
	deleteFirstEntry(t, m)

	outcome := save(context.Background(), m)
	if outcome.Err != nil {
		t.Fatalf("save: неожиданная ошибка: %v", outcome.Err)
	}
	if !outcome.LogOpened {
		t.Fatalf("save: LogOpened=false")
	}

	events := readLogEvents(t, opts.Log.File)
	if len(events) != 1 {
		t.Fatalf("журнал содержит %d событий, ожидалось ровно 1", len(events))
	}
	event := events[0]
	if event["result"] != logger.ResultSuccess {
		t.Fatalf("result=%v, ожидался %q", event["result"], logger.ResultSuccess)
	}
	if event["operation"] != string(logger.OperationEdit) {
		t.Fatalf("operation=%v, ожидалась %q", event["operation"], logger.OperationEdit)
	}
	backupID, ok := event["backup_id"].(string)
	if !ok || backupID == "" {
		t.Fatalf("backup_id пустой или отсутствует: %v", event["backup_id"])
	}
	if backupID != outcome.Result.Backup.ID {
		t.Fatalf("backup_id события %q != ID backup результата %q", backupID, outcome.Result.Backup.ID)
	}
}

// TestReloadResetsSession проверяет F5: перечитывание target сбрасывает
// документ, сессию (dirty, undo-стек) и курсор.
func TestReloadResetsSession(t *testing.T) {
	target := writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\nUUID=bbb /mnt/data ext4 rw 0 2\n")
	opts := saveTestOptions(t, target)
	m := newSaveTestModel(t, opts)
	deleteFirstEntry(t, m)
	m.cursor = 1
	if !m.session.dirty {
		t.Fatalf("предусловие: сессия не dirty после мутации")
	}

	m.reloadFromDisk()

	diskContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("прочитать target: %v", err)
	}
	if !bytes.Equal(m.document.Serialize(), diskContent) {
		t.Fatalf("document != байты target после reloadFromDisk")
	}
	if m.session.dirty {
		t.Fatalf("dirty=true после reloadFromDisk")
	}
	if len(m.session.undoStack) != 0 {
		t.Fatalf("undo-стек не пуст после reloadFromDisk: %d снапшотов", len(m.session.undoStack))
	}
	if !bytes.Equal(m.session.original, diskContent) {
		t.Fatalf("original != байты target после reloadFromDisk")
	}
	if m.cursor != 0 {
		t.Fatalf("cursor=%d после reloadFromDisk, ожидался 0", m.cursor)
	}
}
