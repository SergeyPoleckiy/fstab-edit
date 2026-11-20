// Package cliapp содержит доменную логику CLI: селекторы, preview мутаций
// и типизированные exit errors. Тесты фиксируют i18n-поля ExitError
// (MessageKey/MessageArgs) и новый exit code 3 для PermissionError.
package cliapp

import "testing"

// TestExitErrorCarriesKey проверяет, что K-конструкторы заполняют
// MessageKey/MessageArgs, сохраняя русский Message как fallback для Error().
func TestExitErrorCarriesKey(t *testing.T) {
	e := UsageErrorK(nil, "usage.bad", "плохой флаг %s", "--foo")
	if e.Code != ExitUsage || e.MessageKey != "usage.bad" || len(e.MessageArgs) != 1 {
		t.Fatalf("UsageErrorK: %+v", e)
	}
	if e.Error() == "" {
		t.Fatal("fallback Message пуст")
	}
}

// TestPermissionErrorUsesCode3 фиксирует новый контракт: PermissionError*
// возвращают ExitPermission (3) вместо прежнего ExitConflict (4).
func TestPermissionErrorUsesCode3(t *testing.T) {
	if got := PermissionError(nil, "x"); got.Code != ExitPermission {
		t.Fatalf("code = %d, want 3", got.Code)
	}
	if got := PermissionErrorK(nil, "x.key", "x"); got.Code != ExitPermission {
		t.Fatalf("code = %d, want 3", got.Code)
	}
}
