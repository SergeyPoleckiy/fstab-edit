// Файл rollback_test.go проверяет ручной backup и ручной rollback сервиса.
// Фикстуры готовятся только во временных каталогах; границы отказа подменяются
// fake seams с записью порядка событий, поэтому root и реальный /etc/fstab
// не используются.
package cliapp_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/writer"
)

// rollbackEvents записывает порядок границ отказа, через которые прошёл вызов.
type rollbackEvents struct {
	events []string
}

func (r *rollbackEvents) record(event string) {
	r.events = append(r.events, event)
}

func (r *rollbackEvents) selectBackup(target, prefix, directory, id string) (backup.Selection, error) {
	r.record("select")
	return backup.Select(target, prefix, directory, id)
}

func (r *rollbackEvents) openBackup(path string) (backup.Backup, error) {
	r.record("open")
	return backup.Open(path)
}

func (r *rollbackEvents) readSnapshot(path string) (writer.Snapshot, error) {
	r.record("read")
	return writer.ReadSnapshot(path)
}

func (r *rollbackEvents) createBackup(source writer.Snapshot, options backup.CreateOptions) (backup.Backup, error) {
	r.record("create")
	return backup.Create(source, options)
}

func (r *rollbackEvents) replace(path string, data []byte) (writer.ReplaceResult, error) {
	r.record("replace")
	return writer.Replace(path, data)
}

func (r *rollbackEvents) verifyTarget(ctx context.Context, runner sysutil.CommandRunner, path string) sysutil.Verification {
	r.record("verify")
	return sysutil.NewFindmntVerifier(runner).Verify(ctx, path)
}

// rollbackDependencies собирает Dependencies с записью событий; Replace можно
// переопределить отдельно для проверки сбоя записи.
func rollbackDependencies(events *rollbackEvents) cliapp.Dependencies {
	return cliapp.Dependencies{
		ReadSnapshot: events.readSnapshot,
		SelectBackup: events.selectBackup,
		OpenBackup:   events.openBackup,
		CreateBackup: events.createBackup,
		Replace:      events.replace,
		VerifyTarget: events.verifyTarget,
	}
}

// countingSeam считает вызовы границы отказа.
type countingSeam struct {
	calls int
}

// prepareRollbackFixture создаёт target с текущим содержимым current и два
// backup его прежних версий: older (старше) и newer (новее). Target лежит в
// отдельном от backup directory каталоге.
func prepareRollbackFixture(t *testing.T) (target, directory string, older, newer backup.Backup) {
	t.Helper()
	target = writeTempTarget(t, rollbackVersion1)
	directory = t.TempDir()

	moments := []struct {
		content string
		moment  time.Time
	}{
		{rollbackVersion1, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)},
		{rollbackVersion2, time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)},
	}
	for index, step := range moments {
		if err := os.WriteFile(target, []byte(step.content), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot, err := writer.ReadSnapshot(target)
		if err != nil {
			t.Fatal(err)
		}
		created, err := backup.Create(snapshot, backup.CreateOptions{
			Directory:          directory,
			Operation:          backup.OperationManual,
			SourceVerification: backup.SourceNotChecked,
			Now:                step.moment,
		})
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			older = created
		} else {
			newer = created
		}
	}
	if err := os.WriteFile(target, []byte(rollbackCurrent), 0o600); err != nil {
		t.Fatal(err)
	}
	return target, directory, older, newer
}

const (
	rollbackVersion1 = "# версия 1\n/dev/sda1 / ext4 defaults 0 1\n"
	rollbackVersion2 = "# версия 2\n/dev/sda1 / ext4 defaults 0 1\n"
	rollbackCurrent  = "# текущая версия\n/dev/sda1 / ext4 defaults 0 1\n"
)

func readTarget(t *testing.T, target string) string {
	t.Helper()
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func countBackups(t *testing.T, directory string) int {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestManualBackupCreatesDataAndMetadata(t *testing.T) {
	target := writeTempTarget(t, validTarget)
	directory := t.TempDir()
	service := cliapp.NewService(cliapp.ProductionDependencies(&fakeRunner{}), &fakeRunner{})

	result, exit := service.Backup(target, cliapp.BackupRequest{Directory: directory})
	if exit != nil {
		t.Fatal(exit)
	}
	if result.DryRun {
		t.Fatal("real backup must not be marked dry-run")
	}
	if result.Backup.ID == "" || result.Backup.Metadata.Operation != backup.OperationManual {
		t.Fatalf("backup not described in result: %+v", result.Backup)
	}
	if result.Backup.Metadata.SourceVerification != backup.SourceNotChecked {
		t.Fatalf("manual backup must keep SourceNotChecked: %+v", result.Backup.Metadata)
	}
	for _, path := range []string{result.Backup.Path, result.Backup.MetadataPath} {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			t.Fatalf("backup file missing: %s (%v)", path, err)
		}
	}
}

func TestManualBackupDryRunCreatesNothing(t *testing.T) {
	target := writeTempTarget(t, validTarget)
	directory := t.TempDir()
	read := &countingSeam{}
	create := &countingSeam{}
	runner := &fakeRunner{}
	dependencies := cliapp.ProductionDependencies(runner)
	dependencies.ReadSnapshot = func(path string) (writer.Snapshot, error) {
		read.calls++
		return writer.ReadSnapshot(path)
	}
	dependencies.CreateBackup = func(source writer.Snapshot, options backup.CreateOptions) (backup.Backup, error) {
		create.calls++
		return backup.Create(source, options)
	}
	service := cliapp.NewService(dependencies, runner)

	result, exit := service.Backup(target, cliapp.BackupRequest{Directory: directory, DryRun: true})
	if exit != nil {
		t.Fatal(exit)
	}
	if !result.DryRun {
		t.Fatal("dry-run must be reported in result")
	}
	if read.calls != 0 || create.calls != 0 {
		t.Fatalf("dry-run performed I/O: read=%d create=%d", read.calls, create.calls)
	}
	if countBackups(t, directory) != 0 {
		t.Fatal("dry-run created backup files")
	}
}

func TestManualRollbackDryRunShowsDiffWithoutSafetyBackup(t *testing.T) {
	target, directory, _, newer := prepareRollbackFixture(t)
	events := &rollbackEvents{}
	runner := &fakeRunner{}
	service := cliapp.NewService(rollbackDependencies(events), runner)

	result, exit := service.Rollback(context.Background(), target, cliapp.RollbackRequest{
		Directory: directory,
		DryRun:    true,
	})
	if exit != nil {
		t.Fatal(exit)
	}
	if !result.DryRun {
		t.Fatal("dry-run must be reported in result")
	}
	if result.Selected.ID != newer.ID {
		t.Fatalf("empty RestoreID must select newest backup: %q, want %q", result.Selected.ID, newer.ID)
	}
	// Diff направлен current → selected backup.
	if !strings.Contains(result.Diff, "-# текущая версия") || !strings.Contains(result.Diff, "+# версия 2") {
		t.Fatalf("diff direction wrong: %q", result.Diff)
	}
	if countBackups(t, directory) != 4 {
		t.Fatalf("dry-run created safety backup: %d files", countBackups(t, directory))
	}
	if readTarget(t, target) != rollbackCurrent {
		t.Fatal("dry-run modified target")
	}
	wantEvents := []string{"select", "open", "read", "read"}
	if !reflect.DeepEqual(events.events, wantEvents) {
		t.Fatalf("events = %v, want %v", events.events, wantEvents)
	}
}

func TestManualRollbackExplicitIDSelectsExactBackup(t *testing.T) {
	target, directory, older, _ := prepareRollbackFixture(t)
	events := &rollbackEvents{}
	runner := &fakeRunner{}
	service := cliapp.NewService(rollbackDependencies(events), runner)

	result, exit := service.Rollback(context.Background(), target, cliapp.RollbackRequest{
		RestoreID: older.ID,
		Directory: directory,
	})
	if exit != nil {
		t.Fatal(exit)
	}
	if result.Selected.ID != older.ID {
		t.Fatalf("explicit RestoreID not honored: %q, want %q", result.Selected.ID, older.ID)
	}
	if readTarget(t, target) != rollbackVersion1 {
		t.Fatalf("target = %q, want restored version 1", readTarget(t, target))
	}
	if result.SafetyBackup.Metadata.Operation != backup.OperationBeforeRollback {
		t.Fatalf("safety backup operation: %+v", result.SafetyBackup.Metadata)
	}
}

func TestManualRollbackNewestByDefaultRestoresAndVerifies(t *testing.T) {
	target, directory, _, newer := prepareRollbackFixture(t)
	events := &rollbackEvents{}
	runner := &fakeRunner{results: []sysutil.RunResult{
		{ExitCode: 0, Stdout: "Success: no errors or warnings found"},
	}}
	service := cliapp.NewService(rollbackDependencies(events), runner)

	result, exit := service.Rollback(context.Background(), target, cliapp.RollbackRequest{
		Directory: directory,
	})
	if exit != nil {
		t.Fatal(exit)
	}
	if result.Selected.ID != newer.ID {
		t.Fatalf("newest backup not selected: %q", result.Selected.ID)
	}
	if readTarget(t, target) != rollbackVersion2 {
		t.Fatalf("target = %q, want restored version 2", readTarget(t, target))
	}
	if result.SafetyBackup.ID == "" {
		t.Fatal("safety backup must be recorded in result")
	}
	if result.SafetyBackup.Metadata.Operation != backup.OperationBeforeRollback {
		t.Fatalf("safety backup operation: %+v", result.SafetyBackup.Metadata)
	}
	// Safety backup содержит пре-restore состояние target.
	safetyData, err := os.ReadFile(result.SafetyBackup.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(safetyData) != rollbackCurrent {
		t.Fatalf("safety backup content = %q", safetyData)
	}
	if runner.calls != 1 {
		t.Fatalf("post verification not run: calls=%d", runner.calls)
	}
	// Два исходных backup + новый safety backup (data+sidecar каждый).
	if countBackups(t, directory) != 6 {
		t.Fatalf("backup files = %d, want 6", countBackups(t, directory))
	}
}

func TestManualRollbackEventOrder(t *testing.T) {
	target, directory, _, _ := prepareRollbackFixture(t)
	events := &rollbackEvents{}
	runner := &fakeRunner{}
	service := cliapp.NewService(rollbackDependencies(events), runner)

	if _, exit := service.Rollback(context.Background(), target, cliapp.RollbackRequest{
		Directory: directory,
	}); exit != nil {
		t.Fatal(exit)
	}
	wantEvents := []string{"select", "open", "read", "read", "create", "replace", "verify"}
	if !reflect.DeepEqual(events.events, wantEvents) {
		t.Fatalf("events = %v, want %v", events.events, wantEvents)
	}
}

func TestManualRollbackReplaceFailureKeepsTargetAndSafetyBackup(t *testing.T) {
	target, directory, _, _ := prepareRollbackFixture(t)
	events := &rollbackEvents{}
	runner := &fakeRunner{}
	dependencies := rollbackDependencies(events)
	dependencies.Replace = func(path string, data []byte) (writer.ReplaceResult, error) {
		events.record("replace")
		return writer.ReplaceResult{}, errors.New("rename не выполнен")
	}
	service := cliapp.NewService(dependencies, runner)

	result, exit := service.Rollback(context.Background(), target, cliapp.RollbackRequest{
		Directory: directory,
	})
	if exit == nil || exit.Code != cliapp.ExitIO {
		t.Fatalf("exit = %v, want code 6", exit)
	}
	if !strings.Contains(exit.Error(), "rename не выполнен") {
		t.Fatalf("replace cause lost: %v", exit)
	}
	if readTarget(t, target) != rollbackCurrent {
		t.Fatal("failed replace must not change target")
	}
	if result.SafetyBackup.ID == "" {
		t.Fatal("safety backup must survive replace failure")
	}
	if countBackups(t, directory) != 6 {
		t.Fatalf("backup files = %d, want 6", countBackups(t, directory))
	}
	wantEvents := []string{"select", "open", "read", "read", "create", "replace"}
	if !reflect.DeepEqual(events.events, wantEvents) {
		t.Fatalf("events = %v, want %v", events.events, wantEvents)
	}
}

func TestManualRollbackPostVerificationFailureKeepsSelectedCopy(t *testing.T) {
	target, directory, _, newer := prepareRollbackFixture(t)
	events := &rollbackEvents{}
	runner := &fakeRunner{results: []sysutil.RunResult{
		{ExitCode: 0, Stdout: "0 parse errors, 0 errors, 1 warnings"},
		{ExitCode: 1},
	}}
	dependencies := rollbackDependencies(events)
	service := cliapp.NewService(dependencies, runner)

	// Предупреждения post verification не откатывают выбранную копию.
	result, exit := service.Rollback(context.Background(), target, cliapp.RollbackRequest{
		Directory: directory,
	})
	if exit != nil {
		t.Fatalf("warnings must not fail rollback: %v", exit)
	}
	if readTarget(t, target) != rollbackVersion2 {
		t.Fatal("selected copy must stay in target after warnings")
	}
	if len(result.PostVerification.Report.Diagnostics) == 0 {
		t.Fatal("post verification must be captured in result")
	}

	// Ошибки post verification сообщаются, но автоматического отката нет.
	// RestoreID фиксирует копию версии 2: после первого отката newest backup
	// — это safety backup с пре-restore содержимым, и без явного ID выборка
	// изменилась бы на него.
	result, exit = service.Rollback(context.Background(), target, cliapp.RollbackRequest{
		RestoreID: newer.ID,
		Directory: directory,
	})
	if exit == nil || exit.Code != cliapp.ExitExternal {
		t.Fatalf("exit = %v, want code 5", exit)
	}
	if len(result.PostVerification.Report.Diagnostics) == 0 {
		t.Fatal("post verification must be captured even on errors")
	}
	if readTarget(t, target) != rollbackVersion2 {
		t.Fatal("failed post verification must not auto-restore target")
	}
}

func TestManualRollbackSelectionHappensBeforeAnyWrites(t *testing.T) {
	target, directory, _, _ := prepareRollbackFixture(t)
	events := &rollbackEvents{}
	runner := &fakeRunner{}
	service := cliapp.NewService(rollbackDependencies(events), runner)

	// Несуществующий RestoreID обязан завершить команду до safety backup.
	_, exit := service.Rollback(context.Background(), target, cliapp.RollbackRequest{
		RestoreID: "2099-01-01_00-00-00.000000000Z",
		Directory: directory,
	})
	if exit == nil {
		t.Fatal("expected ExitError for unknown RestoreID")
	}
	if countBackups(t, directory) != 4 {
		t.Fatalf("selection failure created backups: %d files", countBackups(t, directory))
	}
	if readTarget(t, target) != rollbackCurrent {
		t.Fatal("selection failure modified target")
	}
}
