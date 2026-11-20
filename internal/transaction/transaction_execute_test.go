// Тесты Execute покрывают полный changed path transaction без ротации: lock,
// повторный base hash, findmnt pre/post checks, backup текущей операции,
// atomic replace и ровно одну попытку automatic rollback.
package transaction

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/writer"
	"fstab-edit/pkg/models"
)

// fakeRunner реализует sysutil.CommandRunner без запуска findmnt. Он фиксирует
// argv, чтобы tests могли отличить candidate check от post-check target.
type fakeRunner struct {
	calls  [][]string
	result sysutil.RunResult
}

// Run возвращает заранее заданный результат и сохраняет команду полностью.
func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) sysutil.RunResult {
	call := append([]string{name}, args...)
	runner.calls = append(runner.calls, call)
	result := runner.result
	result.Path = name
	return result
}

// validRunnerResult имитирует успешный вывод findmnt для util-linux 2.42.4.
func validRunnerResult() sysutil.RunResult {
	return sysutil.RunResult{
		ExitCode: 0,
		Stdout:   "0 parse errors, 0 errors, 0 warnings\n",
		Duration: time.Millisecond,
	}
}

// fakeLock отслеживает число Close и позволяет имитировать ошибку освобождения
// без создания реального lock-файла.
type fakeLock struct {
	closes int
	err    error
}

// Close идемпотентен с точки зрения transaction: вызывается через guarded defer
// ровно один раз, а повторный вызов из теста фиксирует реальное поведение.
func (lock *fakeLock) Close() error {
	lock.closes++
	return lock.err
}

// successfulVerification возвращает non-nil empty report, соответствующий успешной
// внутренней и внешней проверкам.
func successfulVerification() sysutil.Verification {
	return sysutil.Verification{
		Report: models.Report{Diagnostics: make([]models.Diagnostic, 0)},
		Execution: sysutil.RunResult{
			ExitCode: 0,
			Stdout:   "0 parse errors, 0 errors, 0 warnings\n",
		},
	}
}

// errorVerification формирует внешнюю диагностику с error severity без raw output.
func errorVerification(code string) sysutil.Verification {
	return sysutil.Verification{
		Report: models.Report{Diagnostics: []models.Diagnostic{{
			Code:     code,
			Severity: models.SeverityError,
			Message:  "Синтетическая ошибка проверки",
		}}},
		Execution: sysutil.RunResult{ExitCode: 1},
	}
}

// warningVerification проверяет, что warning после записи не вызывает rollback.
func warningVerification() sysutil.Verification {
	return sysutil.Verification{
		Report: models.Report{Diagnostics: []models.Diagnostic{{
			Code:     "findmnt.warning",
			Severity: models.SeverityWarning,
			Message:  "Синтетическое предупреждение",
		}}},
		Execution: sysutil.RunResult{ExitCode: 0},
	}
}

// testPlan создаёт changed Plan для существующего temp target.
func testPlan(target writer.Snapshot, operation backup.Operation) Plan {
	return Plan{
		TargetPath: target.Path,
		Candidate:  []byte(validCandidate),
		Operation:  operation,
	}
}

// productionSeamsWithFakeLock копирует production operations и заменяет только lock.
// Это интеграционный вариант: backup, writer.Replace и secure snapshots реальные,
// а внешний findmnt заменён fake runner.
func productionSeamsWithFakeLock(lock *fakeLock) transactionOperations {
	operations := productionTransactionOperations()
	operations.acquireLock = func(context.Context, string, time.Duration) (transactionLock, error) {
		return lock, nil
	}
	return operations
}

// assertTargetBytes сравнивает target с ожидаемым содержимым после transaction.
func assertTargetBytes(t *testing.T, path string, expected []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(expected) {
		t.Fatalf("target bytes: получено %q, ожидалось %q", data, expected)
	}
}

// fakeBackup возвращает минимальный verified-shaped объект для seams, где файлы
// backup не должны создаваться.
func fakeBackup(path string) backup.Backup {
	return backup.Backup{
		ID:           "2026-11-20_10-00-00.000000000Z",
		Path:         path,
		MetadataPath: path + ".json",
	}
}

// TestExecuteSuccessRealBackupReplace проводит changed transaction через реальные
// backup.Create/Open и writer.Replace, заменяя только lock и findmnt process.
func TestExecuteSuccessRealBackupReplace(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	runner := &fakeRunner{result: validRunnerResult()}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(ctx context.Context, commandRunner sysutil.CommandRunner, candidate []byte) sysutil.Verification {
		return sysutil.VerifyCandidate(ctx, commandRunner, candidate)
	}
	operations.verifyTarget = func(ctx context.Context, commandRunner sysutil.CommandRunner, path string) sysutil.Verification {
		return sysutil.NewFindmntVerifier(commandRunner).Verify(ctx, path)
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{Runner: runner}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.TargetPath != target.Path {
		t.Fatalf("неожиданный result: %+v", result)
	}
	assertTargetBytes(t, target.Path, []byte(validCandidate))
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d, ожидался 1", lock.closes)
	}
	if result.Backup.Path == "" || result.Backup.MetadataPath == "" {
		t.Fatalf("backup paths должны быть заполнены: %+v", result.Backup)
	}
	assertTargetBytes(t, result.Backup.Path, []byte(validTarget))
	if result.Backup.Metadata.SourcePath != target.Path {
		t.Fatalf("source path metadata: получено %q", result.Backup.Metadata.SourcePath)
	}
	if result.Backup.Metadata.Operation != backup.OperationBeforeEdit {
		t.Fatalf("operation metadata: получено %q", result.Backup.Metadata.Operation)
	}
	if result.Backup.Metadata.SourceVerification != backup.SourceValid {
		t.Fatalf("source verification: получено %q", result.Backup.Metadata.SourceVerification)
	}
	if result.PreVerification.Report.Diagnostics == nil || result.PostVerification.Report.Diagnostics == nil {
		t.Fatal("pre/post verification reports должны быть non-nil")
	}
	if result.PreVerification.Report.HasErrors() || result.PostVerification.Report.HasErrors() {
		t.Fatal("successful verification не должен содержать errors")
	}
	if result.RolledBack || result.RollbackError != nil {
		t.Fatalf("successful transaction не должен rollback: %+v", result)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("fake runner calls: получено %d, ожидалось 2", len(runner.calls))
	}
}

// TestExecuteNoOpSkipsLockBackupAndReplace проверяет публичный Execute и no-op
// invariant: ни lock, ни findmnt, ни backup, ни replace не вызываются.
func TestExecuteNoOpSkipsLockBackupAndReplace(t *testing.T) {
	target := newValidTarget(t)
	runner := &fakeRunner{result: validRunnerResult()}
	result, err := Execute(context.Background(), Plan{
		TargetPath: target.Path,
		Candidate:  []byte(validTarget),
		Operation:  backup.OperationManual,
	}, Options{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("no-op Changed должен быть false")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("no-op не должен вызывать runner: %v", runner.calls)
	}
	assertTargetBytes(t, target.Path, []byte(validTarget))
}

// TestExecuteLockAcquireFailure проверяет conflict classification до backup/write.
func TestExecuteLockAcquireFailure(t *testing.T) {
	target := newValidTarget(t)
	lockErr := errors.New("lock timeout")
	operations := productionTransactionOperations()
	operations.acquireLock = func(context.Context, string, time.Duration) (transactionLock, error) {
		return nil, lockErr
	}
	verifyCalls, createCalls, replaceCalls := 0, 0, 0
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		verifyCalls++
		return successfulVerification()
	}
	operations.createBackup = func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) {
		createCalls++
		return backup.Backup{}, nil
	}
	operations.replace = func(string, []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		return writer.ReplaceResult{}, nil
	}

	_, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureConflict, ErrTransactionConflict, lockErr)
	if verifyCalls != 0 || createCalls != 0 || replaceCalls != 0 {
		t.Fatalf("lock failure не должен вызывать verify/backup/replace: %d/%d/%d", verifyCalls, createCalls, replaceCalls)
	}
	assertTargetBytes(t, target.Path, []byte(validTarget))
}

// TestExecuteBaseHashConflictUnlocksWithoutMutation доказывает, что внешнее
// изменение target после base read запрещает backup и replace.
func TestExecuteBaseHashConflictUnlocksWithoutMutation(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
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
	createCalls, replaceCalls := 0, 0
	operations.createBackup = func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) {
		createCalls++
		return backup.Backup{}, nil
	}
	operations.replace = func(string, []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		return writer.ReplaceResult{}, nil
	}

	_, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureConflict, ErrTransactionConflict, writer.ErrBaseHashConflict)
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d, ожидался 1", lock.closes)
	}
	if createCalls != 0 || replaceCalls != 0 {
		t.Fatalf("base conflict не должен создавать backup или replace: %d/%d", createCalls, replaceCalls)
	}
}

// TestExecuteCandidateVerificationFailure блокирует мутацию до backup и replace.
func TestExecuteCandidateVerificationFailure(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	verificationErr := errors.New("candidate check")
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return errorVerification("findmnt.error")
	}
	createCalls, replaceCalls := 0, 0
	operations.createBackup = func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) {
		createCalls++
		return backup.Backup{}, verificationErr
	}
	operations.replace = func(string, []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		return writer.ReplaceResult{}, verificationErr
	}

	_, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureExternal, ErrTransactionExternal, nil)
	if lock.closes != 1 || createCalls != 0 || replaceCalls != 0 {
		t.Fatalf("pre-check failure: lock=%d create=%d replace=%d", lock.closes, createCalls, replaceCalls)
	}
	assertTargetBytes(t, target.Path, []byte(validTarget))
}

// TestExecuteBackupCreateFailure не должен вызывать replace и должен освобождать lock.
func TestExecuteBackupCreateFailure(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	createErr := errors.New("create backup")
	replaceCalls := 0
	operations.createBackup = func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) {
		return backup.Backup{}, createErr
	}
	operations.replace = func(string, []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		return writer.ReplaceResult{}, nil
	}

	_, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureIO, ErrTransactionIO, createErr)
	if lock.closes != 1 || replaceCalls != 0 {
		t.Fatalf("backup failure: lock=%d replace=%d", lock.closes, replaceCalls)
	}
	assertTargetBytes(t, target.Path, []byte(validTarget))
}

// TestExecuteBackupOpenFailure сохраняет backup path в recovery и не вызывает replace.
func TestExecuteBackupOpenFailure(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	created := fakeBackup(filepath.Join(t.TempDir(), "backup"))
	operations.createBackup = func(current writer.Snapshot, options backup.CreateOptions) (backup.Backup, error) {
		if string(current.Data) != validTarget {
			t.Fatalf("backup должен использовать current snapshot: %q", current.Data)
		}
		if options.SourceVerification != backup.SourceValid {
			t.Fatalf("source verification: получено %q", options.SourceVerification)
		}
		return created, nil
	}
	openErr := errors.New("open backup")
	replaceCalls := 0
	operations.openBackup = func(path string) (backup.Backup, error) {
		if path != created.Path {
			t.Fatalf("open backup path: получено %q", path)
		}
		return backup.Backup{}, openErr
	}
	operations.replace = func(string, []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		return writer.ReplaceResult{}, nil
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureIO, ErrTransactionIO, openErr)
	if lock.closes != 1 || replaceCalls != 0 {
		t.Fatalf("open failure: lock=%d replace=%d", lock.closes, replaceCalls)
	}
	if result.Recovery.Target != target.Path || result.Recovery.Backup != created.Path || result.Recovery.Metadata != created.MetadataPath {
		t.Fatalf("recovery paths: %+v", result.Recovery)
	}
}

// TestExecuteReplaceFailureWithoutReplaced не вызывает rollback.
func TestExecuteReplaceFailureWithoutReplaced(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	created := fakeBackup(filepath.Join(t.TempDir(), "backup"))
	operations.createBackup = func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) { return created, nil }
	operations.openBackup = func(string) (backup.Backup, error) { return created, nil }
	replaceErr := errors.New("replace before rename")
	rollbackCalls := 0
	operations.replace = func(string, []byte) (writer.ReplaceResult, error) {
		return writer.ReplaceResult{Path: target.Path, Replaced: false}, replaceErr
	}
	operations.readBackupSnapshot = func(string) (writer.Snapshot, error) {
		rollbackCalls++
		return writer.Snapshot{}, nil
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureIO, ErrTransactionIO, replaceErr)
	if result.RolledBack || rollbackCalls != 0 || lock.closes != 1 {
		t.Fatalf("Replaced=false не должен rollback: result=%+v rollbackCalls=%d lock=%d", result, rollbackCalls, lock.closes)
	}
	assertTargetBytes(t, target.Path, []byte(validTarget))
}

// TestExecuteReplaceErrorAfterRenameRollsBackOnce имитирует ошибку после rename и
// проверяет восстановление ровно одной попыткой.
func TestExecuteReplaceErrorAfterRenameRollsBackOnce(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	created := fakeBackup(filepath.Join(t.TempDir(), "backup"))
	operations.createBackup = func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) { return created, nil }
	operations.openBackup = func(string) (backup.Backup, error) { return created, nil }
	operations.readBackupSnapshot = func(string) (writer.Snapshot, error) {
		return target, nil
	}
	postReplaceErr := errors.New("directory sync failed")
	replaceCalls := 0
	operations.replace = func(path string, data []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		result, err := writer.Replace(path, data)
		if replaceCalls == 1 {
			if !result.Replaced || err != nil {
				t.Fatalf("первая замена должна быть Replaced=true без ошибки: %+v, %v", result, err)
			}
			return result, postReplaceErr
		}
		return result, err
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureIO, ErrTransactionIO, postReplaceErr)
	if !result.RolledBack || result.RollbackError != nil {
		t.Fatalf("rollback должен завершиться успешно: %+v", result)
	}
	if replaceCalls != 2 {
		t.Fatalf("rollback попыток replace: получено %d, ожидалось 2", replaceCalls)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
	assertTargetBytes(t, target.Path, []byte(validTarget))
}

// TestExecutePostVerificationErrorRollsBack использует реальный backup и restore.
func TestExecutePostVerificationErrorRollsBack(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		return errorVerification("findmnt.post_error")
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureExternal, ErrTransactionExternal, nil)
	if !result.RolledBack || result.RollbackError != nil {
		t.Fatalf("post verification error должен быть откачен: %+v", result)
	}
	if result.PostVerification.Report.Diagnostics[0].Code != "findmnt.post_error" {
		t.Fatalf("post verification diagnostics: %+v", result.PostVerification.Report.Diagnostics)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
	assertTargetBytes(t, target.Path, []byte(validTarget))
}

// TestExecutePostVerificationWarningSucceeds проверяет, что warning не откатывает.
func TestExecutePostVerificationWarningSucceeds(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		return warningVerification()
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if result.RolledBack || !result.Changed || result.PostVerification.Report.Diagnostics[0].Severity != models.SeverityWarning {
		t.Fatalf("warning после записи не должен rollback: %+v", result)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
	assertTargetBytes(t, target.Path, []byte(validCandidate))
}

// TestExecuteRollbackReadFailure является критическим rollback failure.
func TestExecuteRollbackReadFailure(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		return errorVerification("findmnt.post_error")
	}
	rollbackReadErr := errors.New("read backup")
	replaceCalls := 0
	operations.readBackupSnapshot = func(string) (writer.Snapshot, error) {
		return writer.Snapshot{}, rollbackReadErr
	}
	operations.replace = func(string, []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		return writer.Replace(target.Path, []byte(validCandidate))
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureRollback, ErrTransactionRollback, rollbackReadErr)
	if result.RolledBack || !errors.Is(result.RollbackError, rollbackReadErr) || replaceCalls != 1 {
		t.Fatalf("rollback read failure: result=%+v calls=%d", result, replaceCalls)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
}

// TestExecuteRollbackReplaceFailure сохраняет primary и rollback causes.
func TestExecuteRollbackReplaceFailure(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		return errorVerification("findmnt.post_error")
	}
	operations.readBackupSnapshot = func(path string) (writer.Snapshot, error) {
		return writer.ReadSnapshot(path)
	}
	rollbackReplaceErr := errors.New("rollback replace")
	replaceCalls := 0
	operations.replace = func(path string, data []byte) (writer.ReplaceResult, error) {
		replaceCalls++
		if replaceCalls == 1 {
			return writer.Replace(path, data)
		}
		return writer.ReplaceResult{Path: path, Replaced: true}, rollbackReplaceErr
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureRollback, ErrTransactionRollback, rollbackReplaceErr)
	if !errors.Is(err, ErrTransactionExternal) {
		t.Fatalf("primary external class должен сохраняться: %v", err)
	}
	if result.RolledBack || !errors.Is(result.RollbackError, rollbackReplaceErr) || replaceCalls != 2 {
		t.Fatalf("rollback replace failure: result=%+v calls=%d", result, replaceCalls)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
}

// TestExecuteLockCloseErrorPreservesSuccessfulMutation проверяет cleanup invariant:
// target уже успешно изменён, ошибка Close не отменяет результат и присоединяется
// к returned error.
func TestExecuteLockCloseErrorPreservesSuccessfulMutation(t *testing.T) {
	target := newValidTarget(t)
	closeErr := errors.New("close lock")
	lock := &fakeLock{err: closeErr}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		return successfulVerification()
	}

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	if !result.Changed || result.RolledBack {
		t.Fatalf("successful mutation не должен меняться из-за close error: %+v", result)
	}
	if err == nil || !errors.Is(err, closeErr) {
		t.Fatalf("close error должна возвращаться: %v", err)
	}
	if lock.closes != 1 {
		t.Fatalf("lock closes: получено %d", lock.closes)
	}
	assertTargetBytes(t, target.Path, []byte(validCandidate))
}

// TestExecuteIncompleteOperationsConfig отклоняет programming error до чтения target.
func TestExecuteIncompleteOperationsConfig(t *testing.T) {
	readCalls := 0
	operations := transactionOperations{
		readSnapshot: func(string) (writer.Snapshot, error) {
			readCalls++
			return writer.Snapshot{}, nil
		},
	}
	_, err := executeWithOperations(context.Background(), Plan{
		TargetPath: filepath.Join(t.TempDir(), "fstab"),
		Candidate:  []byte(validCandidate),
		Operation:  backup.OperationManual,
	}, Options{}, operations)
	if err == nil || errors.Is(err, ErrTransactionValidation) || errors.Is(err, ErrTransactionIO) {
		t.Fatalf("incomplete operations config должен вернуть programming error вне доменных классов: %v", err)
	}
	if readCalls != 0 {
		t.Fatal("incomplete config не должен читать target")
	}
}

// TestExecuteFakeRunnerNeverLaunchesFindmnt фиксирует, что fake runner получает
// только findmnt argv, а никакая команда не запускаетсья через shell/process.
func TestExecuteFakeRunnerNeverLaunchesFindmnt(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	runner := &fakeRunner{result: validRunnerResult()}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(ctx context.Context, commandRunner sysutil.CommandRunner, candidate []byte) sysutil.Verification {
		return sysutil.VerifyCandidate(ctx, commandRunner, candidate)
	}
	operations.verifyTarget = func(ctx context.Context, commandRunner sysutil.CommandRunner, path string) sysutil.Verification {
		return sysutil.NewFindmntVerifier(commandRunner).Verify(ctx, path)
	}
	if _, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationManual), Options{Runner: runner}, operations); err != nil {
		t.Fatal(err)
	}
	for index, call := range runner.calls {
		if len(call) != 4 || call[0] != "findmnt" || call[1] != "--verify" || call[2] != "--tab-file" {
			t.Fatalf("runner call %d не является findmnt --verify --tab-file: %v", index, call)
		}
	}
	if len(runner.calls) != 2 {
		t.Fatalf("ожидались pre/post findmnt calls, получено %d", len(runner.calls))
	}
}

// TestExecuteResultContainsPreparedFields проверяет, что changed path сохраняет
// candidate hash и internal report от Task 1.
func TestExecuteResultContainsPreparedFields(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	operations.verifyTarget = func(context.Context, sysutil.CommandRunner, string) sysutil.Verification {
		return successfulVerification()
	}
	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	if err != nil {
		t.Fatal(err)
	}
	if result.InternalReport.Diagnostics == nil {
		t.Fatal("InternalReport должен быть non-nil")
	}
	if result.CandidateSHA256 != sha256Sum([]byte(validCandidate)) {
		t.Fatalf("candidate hash: получено %x", result.CandidateSHA256)
	}
}

// sha256Sum — маленький test helper, чтобы не импортировать crypto/sha256 в этом
// файле повторно сложными обёртками.
func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}

// TestExecuteRecoveryPathsAfterBackup заполняет target/backup/metadata как только
// backup создан; log намеренно остаётся пустым до logger.
func TestExecuteRecoveryPathsAfterBackup(t *testing.T) {
	target := newValidTarget(t)
	lock := &fakeLock{}
	operations := productionSeamsWithFakeLock(lock)
	operations.verifyCandidate = func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification {
		return successfulVerification()
	}
	created := fakeBackup(filepath.Join(t.TempDir(), "backup"))
	operations.createBackup = func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error) { return created, nil }
	openErr := errors.New("open backup")
	operations.openBackup = func(string) (backup.Backup, error) { return backup.Backup{}, openErr }

	result, err := executeWithOperations(context.Background(), testPlan(target, backup.OperationBeforeEdit), Options{}, operations)
	mustTxError(t, err, FailureIO, ErrTransactionIO, openErr)
	if result.Recovery.Target != target.Path || result.Recovery.Backup != created.Path || result.Recovery.Metadata != created.MetadataPath || result.Recovery.Log != "" {
		t.Fatalf("recovery paths: %+v", result.Recovery)
	}
}
