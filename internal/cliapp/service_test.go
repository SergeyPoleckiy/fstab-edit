// Файл service_test.go проверяет read-only операции Service (list, validate,
// verify-mount) и передачу mutation preview в transaction. Тесты используют
// только временные файлы и fake runner/seams: реальный /etc/fstab и root
// не требуются, внешние утилиты не запускаются.
package cliapp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/transaction"
	"fstab-edit/pkg/models"
)

// fakeRunner подсчитывает вызовы и возвращает заранее заданные результаты.
// Это единственная точка «внешней утилиты» в тестах сервиса.
type fakeRunner struct {
	calls   int
	results []sysutil.RunResult
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) sysutil.RunResult {
	f.calls++
	if len(f.results) == 0 {
		return sysutil.RunResult{ExitCode: 0}
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result
}

// fakeExecuteTransaction записывает plan/options, полученные от Service, и
// возвращает настроенный результат или ошибку.
type fakeExecuteTransaction struct {
	called  bool
	plan    transaction.Plan
	options transaction.Options
	result  transaction.TransactionResult
	err     error
}

func (f *fakeExecuteTransaction) execute(_ context.Context, plan transaction.Plan, options transaction.Options) (transaction.TransactionResult, error) {
	f.called = true
	f.plan = plan
	f.options = options
	return f.result, f.err
}

// writeTempTarget записывает content во временный fstab-файл и возвращает путь.
func writeTempTarget(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// canonicalPath повторяет нормализацию writer.ReadSnapshot для сравнения путей.
func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(resolved)
}

// listFixture: заголовок документа, две активные записи (одна с комментарием),
// закомментированная запись с пояснением и некорректная активная строка.
const listFixture = `# заголовок документа
/dev/sda1 / ext4 defaults 0 1
# данные диска
/dev/sdb1 /mnt/data ext4 defaults,noatime 0 2
# отключённая запись
# /dev/sdc1 /mnt/old ext4 defaults 0 2
это не запись
`

func findDiagnostic(diags []models.Diagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestServiceListBuildsEntryViewsAndDiagnostics(t *testing.T) {
	target := writeTempTarget(t, listFixture)
	service := cliapp.NewService(cliapp.ProductionDependencies(&fakeRunner{}), &fakeRunner{})

	result, exit := service.List(target)
	if exit != nil {
		t.Fatal(exit)
	}
	if result.Target != canonicalPath(t, target) {
		t.Fatalf("Target = %q, want %q", result.Target, canonicalPath(t, target))
	}
	if len(result.Entries) != 3 {
		t.Fatalf("Entries = %d, want 3: %+v", len(result.Entries), result.Entries)
	}

	first := result.Entries[0]
	if first.Line != 2 || first.State != "active" || first.Comment != "" {
		t.Fatalf("first entry: %+v", first)
	}
	if first.Source.Raw != "/dev/sda1" || first.Source.Decoded != "/dev/sda1" || !first.Source.Present {
		t.Fatalf("first source field: %+v", first.Source)
	}
	if !first.Dump.Present || first.Dump.Raw != "0" || !first.Pass.Present || first.Pass.Raw != "1" {
		t.Fatalf("dump/pass fields: %+v %+v", first.Dump, first.Pass)
	}

	second := result.Entries[1]
	if second.Line != 4 || second.Comment != "данные диска" {
		t.Fatalf("second entry: %+v", second)
	}
	if second.MountPoint.Decoded != "/mnt/data" {
		t.Fatalf("second mount point: %+v", second.MountPoint)
	}

	third := result.Entries[2]
	if third.Line != 6 || third.State != "commented" || third.Comment != "отключённая запись" {
		t.Fatalf("third entry: %+v", third)
	}

	// Комментарий-заголовок первой строки не привязывается к первой записи,
	// а диагностика некорректной строки не привязана ни к одной записи.
	for _, entry := range result.Entries {
		if entry.Diagnostics == nil {
			t.Fatalf("entry diagnostics must be non-nil: %+v", entry)
		}
	}
	if !findDiagnostic(result.Diagnostics, "parse.invalid") {
		t.Fatalf("document diagnostics missing parse.invalid: %+v", result.Diagnostics)
	}

	// Read-only операция: документ на диске не изменился.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != listFixture {
		t.Fatalf("target modified: %q", data)
	}
}

func TestServiceListMapsReadFailureToExitIO(t *testing.T) {
	service := cliapp.NewService(cliapp.ProductionDependencies(&fakeRunner{}), &fakeRunner{})
	missing := filepath.Join(t.TempDir(), "missing")

	_, exit := service.List(missing)
	if exit == nil {
		t.Fatal("expected ExitError for missing target")
	}
	if exit.Code != cliapp.ExitIO {
		t.Fatalf("Code = %d, want %d", exit.Code, cliapp.ExitIO)
	}
}

func TestServiceValidateOfflineSkipsExternalChecks(t *testing.T) {
	target := writeTempTarget(t, listFixture)
	runner := &fakeRunner{}
	service := cliapp.NewService(cliapp.ProductionDependencies(runner), runner)

	result, exit := service.Validate(context.Background(), target, true)
	if exit == nil || exit.Code != cliapp.ExitValidation {
		t.Fatalf("exit = %v, want code 1", exit)
	}
	if runner.calls != 0 {
		t.Fatalf("runner called %d times in offline mode", runner.calls)
	}
	if len(result.Verification.Report.Diagnostics) != 0 {
		t.Fatalf("offline verification must be empty: %+v", result.Verification)
	}
	if !findDiagnostic(result.Report.Diagnostics, "parse.invalid") {
		t.Fatalf("internal diagnostics missing: %+v", result.Report.Diagnostics)
	}
	for _, diagnostic := range result.Report.Diagnostics {
		if strings.HasPrefix(diagnostic.Code, "findmnt.") {
			t.Fatalf("offline report contains external diagnostic: %+v", diagnostic)
		}
	}
}

func TestServiceValidateOnlineMergesInternalAndExternalDiagnostics(t *testing.T) {
	target := writeTempTarget(t, validTarget)
	// Внешняя проверка сообщает только предупреждение: exit error не требуется.
	runner := &fakeRunner{results: []sysutil.RunResult{
		{ExitCode: 0, Stdout: "0 parse errors, 0 errors, 1 warnings"},
	}}
	service := cliapp.NewService(cliapp.ProductionDependencies(runner), runner)

	result, exit := service.Validate(context.Background(), target, false)
	if exit != nil {
		t.Fatal(exit)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if !findDiagnostic(result.Report.Diagnostics, "findmnt.warning") {
		t.Fatalf("merged report missing external diagnostics: %+v", result.Report.Diagnostics)
	}
	if len(result.Verification.Report.Diagnostics) == 0 {
		t.Fatal("online verification must be captured in result")
	}
}

func TestServiceValidateOnlineExternalErrorMapsCode1(t *testing.T) {
	target := writeTempTarget(t, validTarget)
	runner := &fakeRunner{results: []sysutil.RunResult{{ExitCode: 1}}}
	service := cliapp.NewService(cliapp.ProductionDependencies(runner), runner)

	result, exit := service.Validate(context.Background(), target, false)
	if exit == nil || exit.Code != cliapp.ExitValidation {
		t.Fatalf("exit = %v, want code 1", exit)
	}
	if !findDiagnostic(result.Report.Diagnostics, "findmnt.failed") {
		t.Fatalf("external diagnostics missing: %+v", result.Report.Diagnostics)
	}
}

func TestServiceVerifyMountChecksActualTargetBytes(t *testing.T) {
	target := writeTempTarget(t, validTarget)
	runner := &fakeRunner{results: []sysutil.RunResult{{ExitCode: 0, Stdout: "Success: no errors or warnings found"}}}
	service := cliapp.NewService(cliapp.ProductionDependencies(runner), runner)

	verification, exit := service.VerifyMount(context.Background(), target)
	if exit != nil {
		t.Fatal(exit)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if verification.Report.HasErrors() {
		t.Fatalf("valid target must not produce errors: %+v", verification.Report)
	}
	if verification.Execution.ExitCode != 0 {
		t.Fatalf("execution not captured: %+v", verification.Execution)
	}

	// Ошибки внешней проверки отображаются на code 1, warnings допускаются.
	runner.results = []sysutil.RunResult{{ExitCode: 1}}
	if _, exit := service.VerifyMount(context.Background(), target); exit == nil || exit.Code != cliapp.ExitValidation {
		t.Fatalf("exit = %v, want code 1", exit)
	}
	runner.results = []sysutil.RunResult{{ExitCode: 0, Stdout: "0 parse errors, 0 errors, 1 warnings"}}
	if _, exit := service.VerifyMount(context.Background(), target); exit != nil {
		t.Fatalf("warnings must not fail: %v", exit)
	}
}

func TestServiceRunMutationExecutesTransactionWithPlanFromPreview(t *testing.T) {
	target := writeTempTarget(t, validTarget)
	document := parser.Parse([]byte(validTarget))
	preview, previewErr := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind: cliapp.MutationAdd,
		NewEntry: editor.NewEntry{
			Source: "/dev/sdb1", MountPoint: "/mnt/data",
			FSType: "ext4", Options: "defaults,noatime",
		},
	})
	if previewErr != nil {
		t.Fatal(previewErr)
	}

	fake := &fakeExecuteTransaction{
		result: transaction.TransactionResult{Changed: true, TargetPath: target},
	}
	runner := &fakeRunner{}
	dependencies := cliapp.ProductionDependencies(runner)
	dependencies.ExecuteTransaction = fake.execute
	service := cliapp.NewService(dependencies, runner)

	options := transaction.Options{Runner: runner, LockTimeout: 42}
	result, exit := service.RunMutation(context.Background(), target, *preview, options)
	if exit != nil {
		t.Fatal(exit)
	}
	if !fake.called {
		t.Fatal("transaction.Execute was not called")
	}
	if fake.plan.TargetPath != target {
		t.Fatalf("plan.TargetPath = %q, want %q", fake.plan.TargetPath, target)
	}
	if !bytes.Equal(fake.plan.Candidate, preview.Candidate) {
		t.Fatal("plan.Candidate must come from preview")
	}
	if fake.plan.Operation != preview.Operation {
		t.Fatalf("plan.Operation = %q, want %q", fake.plan.Operation, preview.Operation)
	}
	if fake.options.Runner != runner || fake.options.LockTimeout != 42 {
		t.Fatalf("caller options not passed through: %+v", fake.options)
	}
	if result.Changed != true {
		t.Fatalf("transaction result not returned: %+v", result)
	}
}

func TestServiceRunMutationMapsTransactionErrors(t *testing.T) {
	target := writeTempTarget(t, validTarget)
	document := parser.Parse([]byte(validTarget))
	preview, previewErr := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationDelete,
		Selector: editor.ByLine(2),
	})
	if previewErr != nil {
		t.Fatal(previewErr)
	}

	cases := []struct {
		name string
		err  error
		code int
	}{
		{"validation", &transaction.TransactionError{
			Kind: transaction.FailureValidation, Err: transaction.ErrTransactionValidation,
			Recovery: transaction.RecoveryPaths{Target: target}}, cliapp.ExitValidation},
		{"rollback", &transaction.TransactionError{
			Kind: transaction.FailureRollback, Err: transaction.ErrTransactionRollback,
			Recovery: transaction.RecoveryPaths{Target: target}}, cliapp.ExitRollback},
		{"rotation", &transaction.TransactionError{
			Kind: transaction.FailureRotation, Err: transaction.ErrRotationAfterSuccess,
			Recovery: transaction.RecoveryPaths{Target: target}}, cliapp.ExitIO},
		{"plain", errors.New("диск переполнен"), cliapp.ExitIO},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			fake := &fakeExecuteTransaction{err: item.err}
			runner := &fakeRunner{}
			dependencies := cliapp.ProductionDependencies(runner)
			dependencies.ExecuteTransaction = fake.execute
			service := cliapp.NewService(dependencies, runner)

			_, runErr := service.RunMutation(context.Background(), target, *preview, transaction.Options{Runner: runner})
			var exit *cliapp.ExitError
			if !errors.As(runErr, &exit) {
				t.Fatalf("expected *ExitError, got %v", runErr)
			}
			if exit.Code != item.code {
				t.Fatalf("Code = %d, want %d", exit.Code, item.code)
			}
			if !errors.Is(exit, item.err) {
				t.Fatalf("original error must stay reachable via unwrap: %v", exit)
			}
			var txErr *transaction.TransactionError
			if errors.As(item.err, &txErr) {
				var recovered *transaction.TransactionError
				if !errors.As(exit, &recovered) || recovered != txErr {
					t.Fatalf("TransactionError identity lost: %v", exit)
				}
				if recovered.Recovery.Target != target {
					t.Fatalf("recovery paths lost: %+v", recovered.Recovery)
				}
			}
		})
	}
}
