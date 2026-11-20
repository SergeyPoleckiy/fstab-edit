package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testOptions возвращает Options с фиксированным временем и файловым путём в t.TempDir().
func testOptions(t *testing.T, file string) Options {
	t.Helper()
	return Options{
		File: file, Level: "info", MaxSizeMB: 10, Keep: 5,
		RealUID: 1000, EffectiveUID: 1000,
		Now: func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) },
	}
}

// readEvents читает JSON Lines-журнал и возвращает распарсенные события.
func readEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("строка %q не JSON: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func TestOpenWritesJSONLinesEvent(t *testing.T) {
	// Open → Log(info, operation=add, target, backup_id, result=success,
	// old/new с \n) → Close. Файл содержит одну строку; поля severity,
	// operation, target, result, backup_id, real_uid, effective_uid,
	// timestamp (RFC3339) присутствуют; old_value содержит экранированный
	// перенос и не содержит сырого \n внутри строки файла.
	file := filepath.Join(t.TempDir(), "fstab-edit.log")
	l, err := Open(testOptions(t, file))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e := Event{
		Severity:  SeverityInfo,
		Operation: OperationAdd,
		Target:    "/etc/fstab",
		BackupID:  "bkp-1",
		Result:    ResultSuccess,
		OldValue:  Redact("a\nb"),
		NewValue:  Redact("c\nd"),
	}
	if err := l.Log(e); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}
	// Одно событие — строго одна строка: сырого \n внутри записи быть не может.
	if strings.Count(string(data), "\n") != 1 {
		t.Fatalf("ожидалась одна строка, получено %q", string(data))
	}
	events := readEvents(t, file)
	if len(events) != 1 {
		t.Fatalf("ожидалось одно событие, получено %d", len(events))
	}
	ev := events[0]
	for _, field := range []string{"severity", "operation", "target", "result", "backup_id", "real_uid", "effective_uid", "timestamp", "old_value", "new_value"} {
		if _, ok := ev[field]; !ok {
			t.Fatalf("поле %q отсутствует: %v", field, ev)
		}
	}
	if ev["severity"] != "info" || ev["operation"] != "add" || ev["target"] != "/etc/fstab" ||
		ev["result"] != "success" || ev["backup_id"] != "bkp-1" {
		t.Fatalf("неожиданные значения полей: %v", ev)
	}
	if ev["real_uid"] != float64(1000) || ev["effective_uid"] != float64(1000) {
		t.Fatalf("uid: %v", ev)
	}
	ts, ok := ev["timestamp"].(string)
	if !ok || !strings.HasSuffix(ts, "Z") {
		t.Fatalf("timestamp не RFC3339 UTC: %v", ev["timestamp"])
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("timestamp не парсится как RFC3339: %v", err)
	}
	// В распарсенном JSON значение содержит экранированную последовательность
	// из двух символов "\n" (обратный слеш + n), а не реальный перенос.
	if ev["old_value"] != `a\nb` {
		t.Fatalf("old_value = %q, want %q", ev["old_value"], `a\nb`)
	}
}

func TestLevelFilters(t *testing.T) {
	// Level=warn: Log(debug) и Log(info) не пишутся, Log(warn) и Log(error)
	// пишутся — в файле ровно две строки.
	file := filepath.Join(t.TempDir(), "fstab-edit.log")
	opts := testOptions(t, file)
	opts.Level = SeverityWarn
	l, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, sev := range []Severity{SeverityDebug, SeverityInfo, SeverityWarn, SeverityError} {
		if err := l.Log(Event{Severity: sev, Operation: OperationEdit, Target: "/etc/fstab", Result: ResultSuccess}); err != nil {
			t.Fatalf("Log(%s): %v", sev, err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	events := readEvents(t, file)
	if len(events) != 2 {
		t.Fatalf("ожидалось две строки, получено %d: %v", len(events), events)
	}
	if events[0]["severity"] != "warn" || events[1]["severity"] != "error" {
		t.Fatalf("неожиданные severity: %v", events)
	}
}

func TestRedactionAppliedToValues(t *testing.T) {
	// NewValue = "password=abc\nrw" → в JSON old/new значение содержит
	// "***" и "\n" как два символа, не разрывая строку.
	file := filepath.Join(t.TempDir(), "fstab-edit.log")
	l, err := Open(testOptions(t, file))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Log(Event{
		Severity: SeverityInfo, Operation: OperationEdit, Target: "/etc/fstab",
		Result: ResultSuccess, NewValue: Redact("password=abc\nrw"),
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Запись — одна строка файла, без сырого переноса внутри.
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("прочитать журнал: %v", err)
	}
	if strings.Count(strings.TrimRight(string(data), "\n"), "\n") != 0 {
		t.Fatalf("запись содержит сырой перенос: %q", string(data))
	}
	events := readEvents(t, file)
	if len(events) != 1 {
		t.Fatalf("ожидалось одно событие: %v", events)
	}
	// После Redact и парсинга JSON: секрет замаскирован, "\n" — два символа.
	want := `password=***\nrw`
	if events[0]["new_value"] != want {
		t.Fatalf("new_value = %q, want %q", events[0]["new_value"], want)
	}
}

func TestRotation(t *testing.T) {
	// MaxSizeMB=1, Keep=2. Предзаписать файл размером 1 МиБ + 1 байт через
	// os.WriteFile. Open → Log → Close. Ожидание: существуют file.1
	// (старые данные), текущий file содержит только новое событие,
	// file.2 отсутствует.
	dir := t.TempDir()
	file := filepath.Join(dir, "fstab-edit.log")
	old := make([]byte, 1024*1024+1)
	if err := os.WriteFile(file, old, 0o600); err != nil {
		t.Fatalf("предзапись: %v", err)
	}
	opts := testOptions(t, file)
	opts.MaxSizeMB = 1
	opts.Keep = 2
	l, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Log(Event{Severity: SeverityInfo, Operation: OperationAdd, Target: "/etc/fstab", Result: ResultSuccess}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if info, err := os.Stat(file + ".1"); err != nil || info.Size() != int64(len(old)) {
		t.Fatalf("file.1 (старые данные) отсутствует или размер неверен: %v", err)
	}
	if _, err := os.Stat(file + ".2"); !os.IsNotExist(err) {
		t.Fatalf("file.2 не должен существовать: %v", err)
	}
	events := readEvents(t, file)
	if len(events) != 1 || events[0]["operation"] != "add" {
		t.Fatalf("текущий файл должен содержать только новое событие: %v", events)
	}
}

func TestOpenPermissionDenied(t *testing.T) {
	// File в несуществующем каталоге без прав (t.TempDir удалён через
	// os.RemoveAll) → err != nil.
	dir := t.TempDir()
	file := filepath.Join(dir, "fstab-edit.log")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("создать файл: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("удалить каталог: %v", err)
	}
	l, err := Open(testOptions(t, file))
	if err == nil {
		_ = l.Close()
		t.Fatalf("ожидалась ошибка открытия в удалённом каталоге")
	}
}

func TestCloseIdempotent(t *testing.T) {
	// Двойной Close не паникует и не ошибка.
	file := filepath.Join(t.TempDir(), "fstab-edit.log")
	l, err := Open(testOptions(t, file))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("первый Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("второй Close: %v", err)
	}
}
