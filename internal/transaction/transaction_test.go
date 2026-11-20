// Тесты Task 1 фиксируют контракт доменной transaction до появления Execute:
// классы ошибок, no-op detection и внутреннюю проверку candidate должны работать
// без lock, backup, findmnt и изменения target.
package transaction

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/writer"
)

const (
	validTarget    = "/dev/sda1 / ext4 defaults 0 1\n"
	validCandidate = "/dev/sdb1 /mnt/data ext4 defaults,noatime 0 2\n"
)

// newValidTarget создаёт временный fstab, читает его secure snapshot и одновременно
// даёт тестам canonical target path. Реальный /etc/fstab никогда не используется.
func newValidTarget(t *testing.T) writer.Snapshot {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, []byte(validTarget), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := writer.ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// mustTxError извлекает typed transaction error и убеждается, что cause сохраняет
// ожидаемый sentinel, а также конкретную причину, если она задана.
func mustTxError(t *testing.T, err error, kind FailureKind, sentinel, cause error) {
	t.Helper()
	if err == nil {
		t.Fatal("ожидалась ошибка transaction")
	}
	var txErr *TransactionError
	if !errors.As(err, &txErr) {
		t.Fatalf("ожидался *TransactionError, получено %T: %v", err, err)
	}
	if txErr.Kind != kind {
		t.Fatalf("ожидался kind %q, получен %q: %v", kind, txErr.Kind, err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("ошибка не сохраняет sentinel %v: %v", sentinel, err)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("ошибка не сохраняет причину %v: %v", cause, err)
	}
}

// TestPrepareTransactionValidCandidate проверяет положительный путь: snapshot
// target сохраняется, hash candidate фиксируется и internal report доступен caller.
func TestPrepareTransactionValidCandidate(t *testing.T) {
	target := newValidTarget(t)
	prepared, err := prepareTransaction(Plan{
		TargetPath: target.Path,
		Candidate:  []byte(validCandidate),
		Operation:  backup.OperationBeforeEdit,
	}, prepareOperations{readSnapshot: writer.ReadSnapshot})
	if err != nil {
		t.Fatal(err)
	}

	if prepared.base.Path != target.Path {
		t.Fatalf("canonical target path: получено %q, ожидалось %q", prepared.base.Path, target.Path)
	}
	if prepared.changed != true {
		t.Fatal("ожидалось Changed=true для отличающегося candidate")
	}
	expectedHash := sha256.Sum256([]byte(validCandidate))
	if prepared.candidateSHA256 != expectedHash {
		t.Fatalf("candidate SHA-256: получено %x, ожидалось %x", prepared.candidateSHA256, expectedHash)
	}
	if prepared.internalReport.Diagnostics == nil {
		t.Fatal("InternalReport.Diagnostics должен быть non-nil")
	}
	if prepared.internalReport.HasErrors() {
		t.Fatalf("valid candidate не должен иметь internal errors: %+v", prepared.internalReport.Diagnostics)
	}
}

// TestPrepareTransactionNoOpByteEquality проверяет, что одинаковые bytes завершают
// подготовку до внутренней проверки и без признака изменения.
func TestPrepareTransactionNoOpByteEquality(t *testing.T) {
	target := newValidTarget(t)
	prepared, err := prepareTransaction(Plan{
		TargetPath: target.Path,
		Candidate:  []byte(validTarget),
		Operation:  backup.OperationManual,
	}, prepareOperations{readSnapshot: writer.ReadSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.changed {
		t.Fatal("no-op candidate обязан иметь Changed=false")
	}
	if prepared.internalReport.Diagnostics == nil {
		t.Fatal("no-op InternalReport.Diagnostics должен быть non-nil empty slice")
	}
	if len(prepared.internalReport.Diagnostics) != 0 {
		t.Fatalf("no-op не должен выполнять внутреннюю валидацию: %+v", prepared.internalReport.Diagnostics)
	}
}

// TestPrepareTransactionRejectsNilCandidateBeforeRead доказывает, что nil slice
// отклоняется до обращения к target seam.
func TestPrepareTransactionRejectsNilCandidateBeforeRead(t *testing.T) {
	called := false
	_, err := prepareTransaction(Plan{
		TargetPath: filepath.Join(t.TempDir(), "fstab"),
		Operation:  backup.OperationManual,
	}, prepareOperations{readSnapshot: func(string) (writer.Snapshot, error) {
		called = true
		return writer.Snapshot{}, nil
	}})
	mustTxError(t, err, FailureValidation, ErrTransactionValidation, nil)
	if called {
		t.Fatal("nil candidate не должен приводить к чтению target")
	}
}

// TestPrepareTransactionRejectsInvalidOperationBeforeRead проверяет все неизвестные
// operation values до target I/O. Валидная operation допускается к чтению.
func TestPrepareTransactionRejectsInvalidOperationBeforeRead(t *testing.T) {
	for _, operation := range []backup.Operation{"", "unknown", backup.Operation("BEFORE-EDIT")} {
		t.Run(string(operation), func(t *testing.T) {
			called := false
			_, err := prepareTransaction(Plan{
				TargetPath: filepath.Join(t.TempDir(), "fstab"),
				Candidate:  []byte(validCandidate),
				Operation:  operation,
			}, prepareOperations{readSnapshot: func(string) (writer.Snapshot, error) {
				called = true
				return writer.Snapshot{}, nil
			}})
			mustTxError(t, err, FailureValidation, ErrTransactionValidation, nil)
			if called {
				t.Fatal("невалидная operation не должна приводить к чтению target")
			}
		})
	}
}

// TestPrepareTransactionRejectsCorruptCandidate покрывает internal validator path
// без findmnt и lock.
func TestPrepareTransactionRejectsCorruptCandidate(t *testing.T) {
	target := newValidTarget(t)
	_, err := prepareTransaction(Plan{
		TargetPath: target.Path,
		Candidate:  []byte("not-a-valid-entry\n"),
		Operation:  backup.OperationBeforeEdit,
	}, prepareOperations{readSnapshot: writer.ReadSnapshot})
	mustTxError(t, err, FailureValidation, ErrTransactionValidation, nil)
}

// TestPrepareTransactionTargetReadFailure сохраняет конкретную I/O причину target
// и классифицирует сбой как io.
func TestPrepareTransactionTargetReadFailure(t *testing.T) {
	readErr := errors.New("read target boom")
	_, err := prepareTransaction(Plan{
		TargetPath: filepath.Join(t.TempDir(), "missing"),
		Candidate:  []byte(validCandidate),
		Operation:  backup.OperationManual,
	}, prepareOperations{readSnapshot: func(string) (writer.Snapshot, error) {
		return writer.Snapshot{}, readErr
	}})
	mustTxError(t, err, FailureIO, ErrTransactionIO, readErr)
}

// TestPrepareTransactionUsesConfiguredSnapshotSeam доказывает, что prepare принимает
// путь из Plan и использует именно переданный seam.
func TestPrepareTransactionUsesConfiguredSnapshotSeam(t *testing.T) {
	expectedPath := filepath.Join(t.TempDir(), "custom-fstab")
	seamErr := errors.New("stop after path check")
	var gotPath string
	_, err := prepareTransaction(Plan{
		TargetPath: expectedPath,
		Candidate:  []byte(validCandidate),
		Operation:  backup.OperationManual,
	}, prepareOperations{readSnapshot: func(path string) (writer.Snapshot, error) {
		gotPath = path
		return writer.Snapshot{}, seamErr
	}})
	if gotPath != expectedPath {
		t.Fatalf("snapshot seam получил путь %q, ожидался %q", gotPath, expectedPath)
	}
	mustTxError(t, err, FailureIO, ErrTransactionIO, seamErr)
}

// TestTransactionErrorContract фиксирует typed error: kind, Russian message, unwrap
// и recovery paths не зависят от конкретной фазы transaction.
func TestTransactionErrorContract(t *testing.T) {
	cause := errors.New("original cause")
	recovery := RecoveryPaths{Target: "/tmp/target", Backup: "/tmp/backup", Metadata: "/tmp/backup.json"}
	failure := transactionFailure(FailureExternal, ErrTransactionExternal, cause, recovery)

	if failure.Kind != FailureExternal {
		t.Fatalf("kind: получено %q", failure.Kind)
	}
	if !errors.Is(failure, ErrTransactionExternal) || !errors.Is(failure, cause) {
		t.Fatalf("errors.Is не сохраняет sentinel и cause: %v", failure)
	}
	if !errors.Is(failure.Unwrap(), cause) {
		t.Fatalf("Unwrap должен возвращать первичную причину, получено %v", failure.Unwrap())
	}
	message := failure.Error()
	if !strings.Contains(message, "внешняя проверка") || !strings.Contains(message, "original cause") {
		t.Fatalf("сообщение должно содержать русский класс и cause: %q", message)
	}
	if failure.Recovery != recovery {
		t.Fatalf("recovery paths изменились: %+v", failure.Recovery)
	}

	wrapped := wrapTransactionFailure(FailureRollback, ErrTransactionRollback, failure, recovery)
	if wrapped.Kind != FailureRollback || !errors.Is(wrapped, ErrTransactionExternal) {
		t.Fatalf("wrap не сохраняет первичный класс: %v", wrapped)
	}
}

// TestFailureKindStableValues защищает будущие CLI exit-code mapping от accidental
// изменения machine-readable значений.
func TestFailureKindStableValues(t *testing.T) {
	values := map[FailureKind]string{
		FailureValidation: "validation",
		FailureConflict:   "conflict",
		FailureExternal:   "external",
		FailureIO:         "io",
		FailureRollback:   "rollback",
		FailureRotation:   "rotation_after_success",
	}
	for kind, expected := range values {
		if string(kind) != expected {
			t.Fatalf("FailureKind %q изменил значение: ожидалось %q", kind, expected)
		}
	}
	if len(values) != 6 {
		t.Fatalf("ожидалось ровно шесть failure kinds, получено %d", len(values))
	}
}

// TestTransactionResultBaseFields проверяет, что результат no-op уже содержит путь,
// hash и non-nil slices, которые caller сможет сериализовать без специальных cases.
func TestTransactionResultBaseFields(t *testing.T) {
	target := newValidTarget(t)
	prepared, err := prepareTransaction(Plan{
		TargetPath: target.Path,
		Candidate:  []byte(validTarget),
		Operation:  backup.OperationManual,
	}, prepareOperations{readSnapshot: writer.ReadSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	result := TransactionResult{
		Changed:         prepared.changed,
		TargetPath:      prepared.base.Path,
		CandidateSHA256: prepared.candidateSHA256,
		InternalReport:  prepared.internalReport,
	}
	if result.TargetPath != target.Path || result.Changed {
		t.Fatalf("неожиданный no-op result: %+v", result)
	}
	if result.InternalReport.Diagnostics == nil {
		t.Fatal("result diagnostics должен быть non-nil")
	}
}

// TestPrepareTransactionRejectsNilSnapshotSeam отсекает programming error до target
// I/O и не маскирует его под доменный validation failure.
func TestPrepareTransactionRejectsNilSnapshotSeam(t *testing.T) {
	_, err := prepareTransaction(Plan{
		TargetPath: filepath.Join(t.TempDir(), "fstab"),
		Candidate:  []byte(validCandidate),
		Operation:  backup.OperationManual,
	}, prepareOperations{})
	if err == nil || errors.Is(err, ErrTransactionValidation) || errors.Is(err, ErrTransactionIO) {
		t.Fatalf("nil seam должен вернуть programming error вне доменных классов: %v", err)
	}
}

// TestTransactionFailureFormatting не допускает утечки stack trace или слишком
// технических деталей в пользовательское сообщение.
func TestTransactionFailureFormatting(t *testing.T) {
	failure := transactionFailure(FailureIO, ErrTransactionIO, fmt.Errorf("open %s", "/tmp/fstab"), RecoveryPaths{Target: "/tmp/fstab"})
	if strings.Contains(failure.Error(), "goroutine") || strings.Contains(failure.Error(), ".go:") {
		t.Fatalf("пользовательское сообщение не должно содержать stack: %q", failure.Error())
	}
}
