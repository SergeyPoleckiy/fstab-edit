// Тесты ротации фиксируют её место в transaction: только после полного успешного
// post-check, с защитой backup текущей transaction и без rollback уже записанного target.
package transaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/writer"
)

// emptyRotationResult возвращает non-nil slices, которые rotate seam может вернуть
// как успешный пустой partial result.
func emptyRotationResult() backup.RotationResult {
	return backup.RotationResult{
		Removed: make([]backup.Backup, 0),
		Skipped: make([]backup.Backup, 0),
		Failed:  make([]backup.Backup, 0),
	}
}

// successfulRotationSeams создаёт production seams с fake lock и успешными checks,
// оставляя rotate подменяемым тестом.
func successfulRotationSeams(lock *fakeLock) transactionOperations {
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		return successfulVerification()
	}
	return operations
}

// TestExecuteRotationSuccessAfterPostCheck проверяет effective prefix, directory,
// keep и protected list. Backup текущей transaction всегда добавляется последним.
func TestExecuteRotationSuccessAfterPostCheck(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := successfulRotationSeams(lock)

	type rotateCall struct {
		target       string
		prefix       string
		directory    string
		keep         int
		protectedIDs []string
	}
	var got rotateCall
	operations.rotate = func(targetPath, prefix, directory string, keep int, protectedIDs []string) (backup.RotationResult, error) {
		got = rotateCall{
			target:       targetPath,
			prefix:       prefix,
			directory:    directory,
			keep:         keep,
			protectedIDs: append([]string(nil), protectedIDs...),
		}
		return emptyRotationResult(), nil
	}

	callerProtected := []string{"caller-protected-id"}
	plan := testPlan(target, backup.OperationBeforeEdit)
	plan.RotationKeep = 3
	plan.BackupDirectory = filepath.Join(t.TempDir(), "backups")
	result, err := executeWithOperations(context.Background(), plan, Options{RotationProtectedIDs: callerProtected}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.RolledBack {
		t.Fatalf("successful rotation не должен менять result: %+v", result)
	}
	if got.target != target.Path {
		t.Fatalf("rotation target: получено %q", got.target)
	}
	if got.prefix != backup.DefaultBackupPrefix {
		t.Fatalf("empty prefix должен нормализоваться: получено %q", got.prefix)
	}
	if got.directory != plan.BackupDirectory || got.keep != 3 {
		t.Fatalf("rotation args: %+v", got)
	}
	expectedProtected := []string{"caller-protected-id", result.Backup.ID}
	if !reflect.DeepEqual(got.protectedIDs, expectedProtected) {
		t.Fatalf("protected IDs: получено %v, ожидалось %v", got.protectedIDs, expectedProtected)
	}
	if !reflect.DeepEqual(callerProtected, []string{"caller-protected-id"}) {
		t.Fatalf("caller slice изменён: %v", callerProtected)
	}
	if result.Rotation.Removed == nil || result.Rotation.Skipped == nil || result.Rotation.Failed == nil {
		t.Fatalf("Rotation slices должны быть non-nil: %+v", result.Rotation)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
}

// TestExecuteRotationUsesExplicitPrefix проверяет, что caller prefix не заменяется
// default и совпадает с prefix backup metadata.
func TestExecuteRotationUsesExplicitPrefix(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := successfulRotationSeams(lock)
	var gotPrefix string
	operations.rotate = func(_ string, prefix string, _ string, _ int, _ []string) (backup.RotationResult, error) {
		gotPrefix = prefix
		return emptyRotationResult(), nil
	}
	plan := testPlan(target, backup.OperationBeforeEdit)
	plan.BackupPrefix = "custom-prefix"
	if _, err := executeWithOperations(context.Background(), plan, Options{}, operations); err != nil {
		t.Fatal(err)
	}
	if gotPrefix != "custom-prefix" {
		t.Fatalf("explicit prefix изменён: получено %q", gotPrefix)
	}
}

// TestExecuteRotationFailureDoesNotRollbackMutation проверяет отдельный класс
// rotation_after_success: target уже записан, rollback не вызывается.
func TestExecuteRotationFailureDoesNotRollbackMutation(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := successfulRotationSeams(lock)
	rotationErr := errors.New("remove backup")
	replaceCalls := 0
	originalReplace := operations.replace
	operations.replace = func(path string, data []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		return originalReplace(path, data)
	}
	operations.rotate = func(string, string, string, int, []string) (backup.RotationResult, error) {
		return backup.RotationResult{
			Removed: []backup.Backup{resultBackupFixture()},
			Skipped: make([]backup.Backup, 0),
			Failed:  []backup.Backup{resultBackupFixture()},
		}, rotationErr
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureRotation, ErrRotationAfterSuccess, rotationErr)
	if !result.Changed || result.RolledBack {
		t.Fatalf("rotation error не должна откатывать mutation: %+v", result)
	}
	if replaceCalls != 1 {
		t.Fatalf("rollback не должен вызывать второй replace: calls=%d", replaceCalls)
	}
	if result.Rotation.Removed == nil || result.Rotation.Skipped == nil || result.Rotation.Failed == nil {
		t.Fatalf("partial rotation result должен сохранять non-nil slices: %+v", result.Rotation)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
	assertTargetBytes(t, target.Path, []byte(validCandidate))
}

// resultBackupFixture возвращает lightweight Backup для проверки partial rotation
// result без создания файлов.
func resultBackupFixture() backup.Backup {
	return backup.Backup{ID: "fixture-id", Path: "/tmp/fixture", MetadataPath: "/tmp/fixture.json"}
}

// TestExecuteRotationProtectedCurrentBackupWithKeepOne проверяет contract, который
// должен передаться backup.Rotate: caller protected ID и backup текущей transaction.
func TestExecuteRotationProtectedCurrentBackupWithKeepOne(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := successfulRotationSeams(lock)
	var protected []string
	var keep int
	operations.rotate = func(_ string, _ string, _ string, keepArg int, protectedArg []string) (backup.RotationResult, error) {
		keep = keepArg
		protected = append([]string(nil), protectedArg...)
		return emptyRotationResult(), nil
	}
	plan := testPlan(target, backup.OperationBeforeEdit)
	plan.RotationKeep = 1
	if _, err := executeWithOperations(context.Background(), plan, Options{RotationProtectedIDs: []string{"manual-id"}}, operations); err != nil {
		t.Fatal(err)
	}
	if keep != 1 {
		t.Fatalf("keep: получено %d", keep)
	}
	expected := map[string]bool{"manual-id": false}
	// Фактический backup ID проверяется отдельно по длине protected list.
	if len(protected) != 2 {
		t.Fatalf("protected list: получено %v", protected)
	}
	for _, id := range protected {
		if id == "manual-id" {
			expected["manual-id"] = true
		}
	}
	if !expected["manual-id"] || protected[0] != "manual-id" || protected[1] == "" || protected[1] == "manual-id" {
		t.Fatalf("protected list должен содержать caller ID и distinct current backup ID: %v", protected)
	}
}

// TestExecuteRotationNotCalledOnUnsuccessfulPaths покрывает все ранние и rollback
// пути, где ротация по ТЗ запрещена.
func TestExecuteRotationNotCalledOnUnsuccessfulPaths(t *testing.T) {
	t.Run("no-op", func(t *testing.T) {
		target := newValidTarget(t)
		rotateCalls := 0
		operations := productionTransactionOperations()
		operations.rotate = rotationCounter(&rotateCalls)
		result, err := executeWithOperations(context.Background(), Plan{
			TargetPath: target.Path,
			Candidate:  []byte(validTarget),
			Operation:  backup.OperationManual,
		}, Options{}, operations)
		if err != nil || result.Changed || rotateCalls != 0 {
			t.Fatalf("no-op: result=%+v err=%v rotate=%d", result, err, rotateCalls)
		}
	})

	t.Run("validation failure", func(t *testing.T) {
		target := newValidTarget(t)
		rotateCalls := 0
		operations := productionTransactionOperations()
		operations.rotate = rotationCounter(&rotateCalls)
		_, err := executeWithOperations(context.Background(), Plan{
			TargetPath: target.Path,
			Candidate:  []byte("not-a-valid-entry\n"),
			Operation:  backup.OperationBeforeEdit,
		}, Options{}, operations)
		mustTxError(t, err, FailureValidation, ErrTransactionValidation, nil)
		if rotateCalls != 0 {
			t.Fatalf("validation failure вызвал rotation: %d", rotateCalls)
		}
	})

	t.Run("base conflict", func(t *testing.T) {
		target := newValidTarget(t)
		rotateCalls := 0
		lock := &fakeLock{}
		operations := productionSeamsWithFakeLock(lock)
		operations.rotate = rotationCounter(&rotateCalls)
		readCalls := 0
		operations.readSnapshot = func(path string) (writer.Snapshot, error) {
			readCalls++
			if readCalls == 2 {
				if err := os.WriteFile(path, []byte("/dev/sdc1 /changed xfs defaults 0 0\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return writer.ReadSnapshot(path)
		}
		_, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
		mustTxError(t, err, FailureConflict, ErrTransactionConflict, writer.ErrBaseHashConflict)
		if rotateCalls != 0 {
			t.Fatalf("base conflict вызвал rotation: %d", rotateCalls)
		}
	})

	t.Run("pre-check failure", func(t *testing.T) {
		target := newValidTarget(t)
		rotateCalls := 0
		operations := successfulRotationSeams(&fakeLock{})
		operations.rotate = rotationCounter(&rotateCalls)
		operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
			return errorVerification("findmnt.error")
		}
		_, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
		mustTxError(t, err, FailureExternal, ErrTransactionExternal, nil)
		if rotateCalls != 0 {
			t.Fatalf("pre-check failure вызвал rotation: %d", rotateCalls)
		}
	})

	t.Run("backup failure", func(t *testing.T) {
		target := newValidTarget(t)
		rotateCalls := 0
		operations := successfulRotationSeams(&fakeLock{})
		operations.rotate = rotationCounter(&rotateCalls)
		operations.createBackup = func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) {
			return backup.Backup{}, errors.New("create backup")
		}
		_, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
		mustTxError(t, err, FailureIO, ErrTransactionIO, nil)
		if rotateCalls != 0 {
			t.Fatalf("backup failure вызвал rotation: %d", rotateCalls)
		}
	})

	t.Run("rollback result", func(t *testing.T) {
		target := newValidTarget(t)
		rotateCalls := 0
		operations := successfulRotationSeams(&fakeLock{})
		operations.rotate = rotationCounter(&rotateCalls)
		operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
			return errorVerification("findmnt.post_error")
		}
		result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
		mustTxError(t, err, FailureExternal, ErrTransactionExternal, nil)
		if !result.RolledBack || rotateCalls != 0 {
			t.Fatalf("rollback result: %+v rotate=%d", result, rotateCalls)
		}
		assertTargetBytes(t, target.Path, []byte(validTarget))
	})
}

// rotationCounter возвращает rotate seam, который только считает вызовы и не
// удаляет файлы. Он применяется в неуспешных путях, где ротация недопустима.
func rotationCounter(calls *int) func(string, string, string, int, []string) (backup.RotationResult, error) {
	return func(string, string, string, int, []string) (backup.RotationResult, error) {
		*calls++
		return emptyRotationResult(), nil
	}
}

// eventLock добавляет событие Close в общий журнал порядка операций.
type eventLock struct {
	events *[]string
}

// Close фиксирует освобождение lock после всех доменных операций.
func (lock eventLock) Close() error {
	*lock.events = append(*lock.events, "lock-close")
	return nil
}

// TestExecuteRotationEventOrder доказывает, что rotation запускается после backup,
// replace и post verification, но до освобождения lock.
func TestExecuteRotationEventOrder(t *testing.T) {
	target := newValidTarget(t)
	events := make([]string, 0, 12)
	operations := productionTransactionOperations()
	operations.readSnapshot = func(path string) (writer.Snapshot, error) {
		events = append(events, "read")
		return writer.ReadSnapshot(path)
	}
	operations.acquireLock = func(context.Context, string, time.Duration) (transactionLock, error) {
		events = append(events, "lock-acquire")
		return eventLock{events: &events}, nil
	}
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		events = append(events, "verify-candidate")
		return successfulVerification()
	}
	operations.createBackup = func(snapshot writer.Snapshot, options backup.CreateOptions) (backup.Backup, error) {
		events = append(events, "backup-create")
		return backup.Create(snapshot, options)
	}
	operations.openBackup = func(path string) (backup.Backup, error) {
		events = append(events, "backup-open")
		return backup.Open(path)
	}
	operations.replace = func(path string, data []byte) (writer.ReplaceResult, error) {
		events = append(events, "replace")
		return writer.Replace(path, data)
	}
	operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		events = append(events, "verify-target")
		return successfulVerification()
	}
	operations.rotate = func(string, string, string, int, []string) (backup.RotationResult, error) {
		events = append(events, "rotate")
		return emptyRotationResult(), nil
	}

	if _, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"read",
		"lock-acquire",
		"read",
		"verify-candidate",
		"backup-create",
		"backup-open",
		"replace",
		"verify-target",
		"rotate",
		"lock-close",
	}
	if !reflect.DeepEqual(events, expected) {
		t.Fatalf("event order:\nполучено  %v\nожидалось %v", events, expected)
	}
}

// TestExecuteRotationCloseErrorPreservesRotationFailure проверяет, что cleanup
// после rotation failure присоединяется, но не заменяет primary rotation class.
func TestExecuteRotationCloseErrorPreservesRotationFailure(t *testing.T) {
	target := newValidTarget(t)
	closeErr := errors.New("close lock")
	lock := &fakeLock{err: closeErr}
	operations := successfulRotationSeams(lock)
	rotationErr := errors.New("rotation failed")
	operations.rotate = func(string, string, string, int, []string) (backup.RotationResult, error) {
		return emptyRotationResult(), rotationErr
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	if !result.Changed || result.RolledBack {
		t.Fatalf("mutation result изменился: %+v", result)
	}
	mustTxError(t, err, FailureRotation, ErrRotationAfterSuccess, rotationErr)
	if !errors.Is(err, closeErr) {
		t.Fatalf("lock close error должен присоединяться: %v", err)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
	assertTargetBytes(t, target.Path, []byte(validCandidate))
}

// TestExecuteRotationFailureKeepsCallerProtectedCopy проверяет, что при сбое
// ротации caller slice не изменяется и rotate получает копию protected IDs.
func TestExecuteRotationFailureKeepsCallerProtectedCopy(t *testing.T) {
	target := newValidTarget(t)
	operations := successfulRotationSeams(&fakeLock{})
	var gotProtected []string
	operations.rotate = func(_ string, _ string, _ string, _ int, protected []string) (backup.RotationResult, error) {
		gotProtected = append([]string(nil), protected...)
		return emptyRotationResult(), errors.New("rotation failed")
	}
	caller := []string{"stable-id"}
	if _, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationManual), Options{RotationProtectedIDs: caller}, operations); err == nil {
		t.Fatal("ожидалась ошибка rotation")
	}
	if !reflect.DeepEqual(caller, []string{"stable-id"}) {
		t.Fatalf("caller protected slice изменён: %v", caller)
	}
	if len(gotProtected) != 2 || gotProtected[0] != "stable-id" || gotProtected[1] == "stable-id" {
		t.Fatalf("protected copy: %v", gotProtected)
	}
}

// TestExecuteRejectsNilRotateSeam фиксирует programming error по имени поля: общий
// incomplete-config test останавливается на первом nil field и не доказывает,
// что rotate входит в обязательный production seam contract.
func TestExecuteRejectsNilRotateSeam(t *testing.T) {
	operations := productionTransactionOperations()
	operations.rotate = nil
	_, err := executeWithOperations(context.Background(), Plan{
		TargetPath: filepath.Join(t.TempDir(), "fstab"),
		Candidate:  []byte(validCandidate),
		Operation:  backup.OperationManual,
	}, Options{}, operations)
	if err == nil {
		t.Fatal("ожидалась ошибка nil rotate seam")
	}
	if !strings.Contains(err.Error(), "rotate") {
		t.Fatalf("ошибка должна указывать именно rotate seam: %v", err)
	}
	var transactionErr *TransactionError
	if errors.As(err, &transactionErr) {
		t.Fatalf("programming error seam не должен классифицироваться как TransactionError: %v", err)
	}
}
