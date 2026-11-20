// Package cliapp содержит доменную логику CLI: селекторы, preview мутаций
// и типизированные exit errors. Пакет не выполняет ввод-вывод и не читает
// реальные системные файлы; все операции работают только с переданными
// models.Document и byte slices.
package cliapp_test

import (
	"errors"
	"fmt"
	"testing"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/transaction"
)

func TestExitCodeForFailureKind(t *testing.T) {
	cases := []struct {
		kind transaction.FailureKind
		want int
	}{
		{transaction.FailureValidation, 1},
		{transaction.FailureConflict, 4},
		{transaction.FailureExternal, 5},
		{transaction.FailureIO, 6},
		{transaction.FailureRollback, 7},
		{transaction.FailureRotation, 6},
	}
	for _, item := range cases {
		if got := cliapp.ExitCodeForFailureKind(item.kind); got != item.want {
			t.Fatalf("ExitCodeForFailureKind(%q) = %d, want %d", item.kind, got, item.want)
		}
	}
}

func TestExitErrorPreservesCauseAndCode(t *testing.T) {
	cause := errors.New("исходная причина")
	exit := cliapp.ValidationError(cause, "запись не прошла проверку")
	if exit.Code != 1 {
		t.Fatalf("Code = %d, want 1", exit.Code)
	}
	if !errors.Is(exit, cause) {
		t.Fatalf("errors.Is lost the cause: %v", exit)
	}
	if !errors.Is(exit, cliapp.ErrValidation) {
		t.Fatalf("sentinel cause lost: %v", exit)
	}
	if exit.Error() == "" {
		t.Fatalf("Error() must return a non-empty message")
	}
}

func TestExitErrorUnwrapNilCause(t *testing.T) {
	exit := &cliapp.ExitError{Code: 2, Message: "некорректные аргументы"}
	if exit.Error() != "некорректные аргументы" {
		t.Fatalf("Error() = %q", exit.Error())
	}
	var target *cliapp.ExitError
	if !errors.As(exit, &target) || target != exit {
		t.Fatalf("errors.As must recover the ExitError: %v", exit)
	}
}

func TestHelperErrorsFillCodes(t *testing.T) {
	cause := errors.New("причина")
	cases := []struct {
		name string
		got  *cliapp.ExitError
		want int
	}{
		{"UsageError", cliapp.UsageError(cause, "usage"), 2},
		{"ValidationError", cliapp.ValidationError(cause, "validation"), 1},
		{"AmbiguityError", cliapp.AmbiguityError(cause, []int{3, 7}, "ambiguous"), 8},
		{"PermissionError", cliapp.PermissionError(cause, "permission"), 4},
	}
	for _, item := range cases {
		if item.got.Code != item.want {
			t.Fatalf("%s Code = %d, want %d", item.name, item.got.Code, item.want)
		}
		if !errors.Is(item.got, cause) {
			t.Fatalf("%s lost cause: %v", item.name, item.got)
		}
	}
}

func TestAmbiguityErrorCarriesCandidates(t *testing.T) {
	exit := cliapp.AmbiguityError(fmt.Errorf("%w: /mnt", cliapp.ErrAmbiguous), []int{3, 7}, "неоднозначный селектор")
	if len(exit.Candidates) != 2 || exit.Candidates[0] != 3 || exit.Candidates[1] != 7 {
		t.Fatalf("Candidates = %v, want [3 7]", exit.Candidates)
	}
}

func TestUnknownFailureKindMapsToIO(t *testing.T) {
	if got := cliapp.ExitCodeForFailureKind(transaction.FailureKind("unknown")); got != 6 {
		t.Fatalf("unknown FailureKind must map to io code 6, got %d", got)
	}
}
