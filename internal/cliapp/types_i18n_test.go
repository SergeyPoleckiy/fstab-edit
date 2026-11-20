// Package cliapp содержит доменную логику CLI: селекторы, preview мутаций
// и типизированные exit errors. Тесты фиксируют i18n-поля ExitError
// (MessageKey/MessageArgs) и новый exit code 3 для PermissionError.
package cliapp

import (
	"context"
	"errors"
	"testing"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/transaction"
	"fstab-edit/internal/writer"
)

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

// i18nFailingRunner всегда сообщает ненулевой exit code внешней утилиты.
type i18nFailingRunner struct{}

func (i18nFailingRunner) Run(_ context.Context, _ string, _ ...string) sysutil.RunResult {
	return sysutil.RunResult{ExitCode: 1}
}

// i18nValidSnapshot возвращает snapshot корректного минимального fstab.
func i18nValidSnapshot(_ string) (writer.Snapshot, error) {
	return writer.Snapshot{Path: "/tmp/fstab", Data: []byte("/dev/sda1 / ext4 defaults 0 1\n")}, nil
}

// i18nInvalidSnapshot возвращает snapshot с некорректной строкой: внутренний
// валидатор обязан сообщить severity=error.
func i18nInvalidSnapshot(_ string) (writer.Snapshot, error) {
	return writer.Snapshot{Path: "/tmp/fstab", Data: []byte("это не запись\n")}, nil
}

// i18nFailingRead имитирует недоступный target.
func i18nFailingRead(path string) (writer.Snapshot, error) {
	return writer.Snapshot{}, errors.New("файл не найден: " + path)
}

// requireExitKey проверяет, что ExitError не nil, имеет ожидаемый code и
// непустой i18n-ключ: каждая ошибка, доходящая до cmd, обязана переводиться.
func requireExitKey(t *testing.T, exit *ExitError, code int) {
	t.Helper()
	if exit == nil {
		t.Fatal("expected *ExitError, got nil")
	}
	if exit.Code != code {
		t.Fatalf("Code = %d, want %d (%v)", exit.Code, code, exit)
	}
	if exit.MessageKey == "" {
		t.Fatalf("MessageKey is empty for %q (code %d)", exit.Message, exit.Code)
	}
}

// duplicateMountFixture содержит две активные записи с одной точкой монтирования.
const duplicateMountFixture = `/dev/sda1 / ext4 defaults 0 1
/dev/sdb1 /mnt/data ext4 defaults 0 2
/dev/sdc1 /mnt/data xfs noatime 0 0
`

// TestAllExitErrorsHaveKeys перебирает представительные сценарии каждого
// публичного метода cliapp и требует: returned *ExitError != nil и
// MessageKey != "". Для методов, возвращающих error (RunMutation), проверяется
// errors.As на *ExitError и непустой MessageKey.
func TestAllExitErrorsHaveKeys(t *testing.T) {
	ctx := context.Background()

	t.Run("List read failure", func(t *testing.T) {
		service := NewService(Dependencies{ReadSnapshot: i18nFailingRead}, i18nFailingRunner{})
		_, exit := service.List("/missing")
		requireExitKey(t, exit, ExitIO)
	})

	t.Run("Validate internal diagnostics", func(t *testing.T) {
		service := NewService(Dependencies{ReadSnapshot: i18nInvalidSnapshot}, i18nFailingRunner{})
		_, exit := service.Validate(ctx, "/tmp/fstab", true)
		requireExitKey(t, exit, ExitValidation)
	})

	t.Run("Validate read failure", func(t *testing.T) {
		service := NewService(Dependencies{ReadSnapshot: i18nFailingRead}, i18nFailingRunner{})
		_, exit := service.Validate(ctx, "/missing", true)
		requireExitKey(t, exit, ExitIO)
	})

	t.Run("VerifyMount external diagnostics", func(t *testing.T) {
		service := NewService(Dependencies{ReadSnapshot: i18nValidSnapshot}, i18nFailingRunner{})
		_, exit := service.VerifyMount(ctx, "/tmp/fstab")
		requireExitKey(t, exit, ExitValidation)
	})

	t.Run("VerifyMount read failure", func(t *testing.T) {
		service := NewService(Dependencies{ReadSnapshot: i18nFailingRead}, i18nFailingRunner{})
		_, exit := service.VerifyMount(ctx, "/missing")
		requireExitKey(t, exit, ExitIO)
	})

	t.Run("Backup create failure", func(t *testing.T) {
		dependencies := Dependencies{
			ReadSnapshot: i18nValidSnapshot,
			CreateBackup: func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) {
				return backup.Backup{}, errors.New("диск переполнен")
			},
		}
		service := NewService(dependencies, i18nFailingRunner{})
		_, exit := service.Backup("/tmp/fstab", BackupRequest{})
		requireExitKey(t, exit, ExitIO)
	})

	t.Run("Rollback backup not found", func(t *testing.T) {
		dependencies := Dependencies{
			SelectBackup: func(_, _, _, _ string) (backup.Selection, error) {
				return backup.Selection{}, backup.ErrBackupNotFound
			},
		}
		service := NewService(dependencies, i18nFailingRunner{})
		_, exit := service.Rollback(ctx, "/tmp/fstab", RollbackRequest{RestoreID: "nope"})
		requireExitKey(t, exit, ExitValidation)
	})

	t.Run("Rollback select failure", func(t *testing.T) {
		dependencies := Dependencies{
			SelectBackup: func(_, _, _, _ string) (backup.Selection, error) {
				return backup.Selection{}, errors.New("каталог недоступен")
			},
		}
		service := NewService(dependencies, i18nFailingRunner{})
		_, exit := service.Rollback(ctx, "/tmp/fstab", RollbackRequest{})
		requireExitKey(t, exit, ExitIO)
	})

	t.Run("SelectorFromValues missing required", func(t *testing.T) {
		_, exit := SelectorFromValues("", "", "", "", true)
		requireExitKey(t, exit, ExitUsage)
	})

	t.Run("SelectorFromValues multiple ways", func(t *testing.T) {
		_, exit := SelectorFromValues("1", "2", "", "", true)
		requireExitKey(t, exit, ExitUsage)
	})

	t.Run("SelectorFromValues bad line", func(t *testing.T) {
		_, exit := SelectorFromValues("", "0", "", "", true)
		requireExitKey(t, exit, ExitUsage)
	})

	t.Run("Resolve ambiguity without chooser", func(t *testing.T) {
		document := parser.Parse([]byte(duplicateMountFixture))
		_, exit := Resolve(document, editor.ByMountPoint("/mnt/data"), nil, false)
		requireExitKey(t, exit, ExitAmbiguous)
	})

	t.Run("Resolve not found", func(t *testing.T) {
		document := parser.Parse([]byte(duplicateMountFixture))
		_, exit := Resolve(document, editor.ByMountPoint("/absent"), nil, false)
		requireExitKey(t, exit, ExitValidation)
	})

	t.Run("PrepareMutation ambiguous selector", func(t *testing.T) {
		document := parser.Parse([]byte(duplicateMountFixture))
		_, exit := PrepareMutation(document, MutationRequest{
			Kind:     MutationDelete,
			Selector: editor.ByMountPoint("/mnt/data"),
		})
		requireExitKey(t, exit, ExitAmbiguous)
	})

	t.Run("PrepareMutation unknown kind", func(t *testing.T) {
		document := parser.Parse([]byte(duplicateMountFixture))
		_, exit := PrepareMutation(document, MutationRequest{Kind: MutationKind(99)})
		requireExitKey(t, exit, ExitValidation)
	})

	t.Run("PrepareMutation nil document", func(t *testing.T) {
		_, exit := PrepareMutation(nil, MutationRequest{Kind: MutationAdd})
		requireExitKey(t, exit, ExitValidation)
	})

	t.Run("RunMutation transaction error", func(t *testing.T) {
		dependencies := Dependencies{
			ExecuteTransaction: func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error) {
				return transaction.TransactionResult{}, &transaction.TransactionError{
					Kind: transaction.FailureRollback,
					Err:  transaction.ErrTransactionRollback,
				}
			},
		}
		service := NewService(dependencies, i18nFailingRunner{})
		_, err := service.RunMutation(ctx, "/tmp/fstab", MutationPreview{}, transaction.Options{})
		var exit *ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("expected *ExitError, got %v", err)
		}
		requireExitKey(t, exit, ExitRollback)
	})

	t.Run("RunMutation plain error", func(t *testing.T) {
		dependencies := Dependencies{
			ExecuteTransaction: func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error) {
				return transaction.TransactionResult{}, errors.New("диск переполнен")
			},
		}
		service := NewService(dependencies, i18nFailingRunner{})
		_, err := service.RunMutation(ctx, "/tmp/fstab", MutationPreview{}, transaction.Options{})
		var exit *ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("expected *ExitError, got %v", err)
		}
		requireExitKey(t, exit, ExitIO)
	})
}
