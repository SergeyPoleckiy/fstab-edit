package sysutil

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"fstab-edit/pkg/models"
)

// fakeFindmntRunner фиксирует отдельные argv и возвращает заранее заданный
// результат, не запуская findmnt или какой-либо другой внешний процесс.
type fakeFindmntRunner struct {
	result RunResult
	calls  int
	ctx    context.Context
	name   string
	args   []string
}

func (r *fakeFindmntRunner) Run(ctx context.Context, name string, args ...string) RunResult {
	r.calls++
	r.ctx = ctx
	r.name = name
	r.args = append([]string(nil), args...)
	return r.result
}

// findmntFixture читает только синтетический наблюдённый вывод из testdata.
func findmntFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "findmnt", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// assertFindmntDiagnosticContract проверяет обязательные поля и отсутствие
// исходных строк stdout/stderr во всех принятых внешних диагностиках.
func assertFindmntDiagnosticContract(t *testing.T, report models.Report) {
	t.Helper()
	if report.Diagnostics == nil {
		t.Fatal("Report.Diagnostics должен быть non-nil")
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	for _, marker := range []string{"[E]", "[W]", "parse error at line", "findmnt: ", "/dev/"} {
		if strings.Contains(encoded, marker) {
			t.Errorf("Report раскрывает raw marker %q: %s", marker, encoded)
		}
	}
	for i, diagnostic := range report.Diagnostics {
		if strings.TrimSpace(diagnostic.Code) == "" {
			t.Errorf("диагностика %d имеет пустой Code: %+v", i, diagnostic)
		}
		if strings.TrimSpace(diagnostic.Message) == "" {
			t.Errorf("диагностика %d имеет пустой Message: %+v", i, diagnostic)
		}
		if diagnostic.Severity != models.SeverityError && diagnostic.Severity != models.SeverityWarning {
			t.Errorf("диагностика %d имеет недопустимую Severity %q", i, diagnostic.Severity)
		}
		if diagnostic.Value != "" {
			t.Errorf("диагностика %d раскрывает Value %q", i, diagnostic.Value)
		}
		for key, value := range diagnostic.Params {
			if key != "exit_code" && key != "count" {
				t.Errorf("диагностика %d имеет неожиданный параметр %q", i, key)
			}
			if _, err := strconv.Atoi(value); err != nil {
				t.Errorf("параметр %s=%q должен быть числовым: %v", key, value, err)
			}
		}
	}
}

// assertFindmntCodes сравнивает одновременно коды, severity и порядок диагностик.
func assertFindmntCodes(t *testing.T, report models.Report, want ...string) {
	t.Helper()
	got := make([]string, 0, len(report.Diagnostics))
	for _, diagnostic := range report.Diagnostics {
		got = append(got, diagnostic.Code+"/"+string(diagnostic.Severity))
	}
	if len(got) != len(want) {
		t.Fatalf("диагностики:\ngot  %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("диагностики:\ngot  %v\nwant %v", got, want)
		}
	}
}

// TestFindmntVerifyArgvAndExecution проверяет точный argv без shell-конкатенации
// и сохранение исходного результата запуска вместе с очищенным Report.
func TestFindmntVerifyArgvAndExecution(t *testing.T) {
	stdout := findmntFixture(t, "valid-2.42.4.stdout")
	execution := RunResult{
		Path: "/usr/bin/findmnt", ExitCode: 0,
		Stdout: stdout, Stderr: "синтетический raw stderr",
		Duration: 73 * time.Millisecond,
	}
	runner := &fakeFindmntRunner{result: execution}
	verifier := NewFindmntVerifier(runner)
	ctx := context.WithValue(context.Background(), struct{}{}, "test context")
	candidate := "/tmp/candidate --verify with spaces"

	verification := verifier.Verify(ctx, candidate)

	if runner.calls != 1 {
		t.Fatalf("вызовов runner: got %d, want 1", runner.calls)
	}
	if runner.ctx != ctx {
		t.Fatal("Verify не сохранил исходный context")
	}
	if runner.name != "findmnt" {
		t.Fatalf("name: got %q, want findmnt", runner.name)
	}
	wantArgs := []string{"--verify", "--tab-file", candidate}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args:\ngot  %#v\nwant %#v", runner.args, wantArgs)
	}
	if !reflect.DeepEqual(verification.Execution, execution) {
		t.Fatalf("Execution изменён:\ngot  %+v\nwant %+v", verification.Execution, execution)
	}
	if verification.Report.Diagnostics == nil || len(verification.Report.Diagnostics) != 0 {
		t.Fatalf("valid output должен дать non-nil empty Report: %+v", verification.Report)
	}
	if verification.Report.HasErrors() {
		t.Fatal("valid output не должен блокировать сохранение")
	}
	assertFindmntDiagnosticContract(t, verification.Report)
	if strings.Contains(mustFindmntReportJSON(t, verification.Report), "синтетический raw stderr") {
		t.Fatal("raw stderr попал в Report")
	}
}

// TestFindmntVerifyRejectsEmptyPath проверяет локальную ошибку без запуска утилиты.
func TestFindmntVerifyRejectsEmptyPath(t *testing.T) {
	runner := &fakeFindmntRunner{}
	verification := NewFindmntVerifier(runner).Verify(context.Background(), "")

	if runner.calls != 0 {
		t.Fatalf("runner вызван %d раз для пустого path", runner.calls)
	}
	assertFindmntCodes(t, verification.Report, "findmnt.invalid_path/error")
	assertFindmntDiagnosticContract(t, verification.Report)
}

// TestFindmntVerifyNilRunner проверяет управляемый unavailable вместо panic.
func TestFindmntVerifyNilRunner(t *testing.T) {
	verification := NewFindmntVerifier(nil).Verify(context.Background(), "/private/candidate")

	assertFindmntCodes(t, verification.Report, "findmnt.unavailable/error")
	assertFindmntDiagnosticContract(t, verification.Report)
	if strings.Contains(mustFindmntReportJSON(t, verification.Report), "/private/candidate") {
		t.Fatal("candidate path попал в Report для nil runner")
	}
}

// TestFindmntVerifyValid проверяет успешную классификацию summary без findings.
func TestFindmntVerifyValid(t *testing.T) {
	execution := RunResult{ExitCode: 0, Stdout: findmntFixture(t, "valid-2.42.4.stdout")}
	verification := NewFindmntVerifier(&fakeFindmntRunner{result: execution}).Verify(context.Background(), "/private/candidate")

	assertFindmntCodes(t, verification.Report)
	assertFindmntDiagnosticContract(t, verification.Report)
	if verification.Report.HasErrors() {
		t.Fatal("valid fixture не должен содержать ошибки")
	}
	if !reflect.DeepEqual(verification.Execution, execution) {
		t.Fatalf("Execution изменён: %+v", verification.Execution)
	}
}

// TestFindmntVerifyWarnings проверяет три detail warning без summary duplicate.
func TestFindmntVerifyWarnings(t *testing.T) {
	execution := RunResult{ExitCode: 0, Stdout: findmntFixture(t, "warnings-2.42.4.stdout")}
	verification := NewFindmntVerifier(&fakeFindmntRunner{result: execution}).Verify(context.Background(), "/private/candidate")

	assertFindmntCodes(t, verification.Report,
		"findmnt.warning/warning", "findmnt.warning/warning", "findmnt.warning/warning")
	assertFindmntDiagnosticContract(t, verification.Report)
	if verification.Report.HasErrors() {
		t.Fatal("одни warnings не должны блокировать сохранение")
	}
	encoded := mustFindmntReportJSON(t, verification.Report)
	for _, raw := range []string{"/dev/sda2", "ext4", "btrfs", "unreachable source"} {
		if strings.Contains(encoded, raw) {
			t.Errorf("Report раскрывает warning detail %q: %s", raw, encoded)
		}
	}
}

// TestFindmntVerifyErrorsAndRedaction проверяет все parse/E/W findings, их порядок,
// номера строк и разделение sensitive Execution и обобщённого Report. ExitError
// воспроизводит результат реального Runner при обычном ненулевом exit без запуска процесса.
func TestFindmntVerifyErrorsAndRedaction(t *testing.T) {
	stdout := findmntFixture(t, "errors-2.42.4.stdout")
	stderr := findmntFixture(t, "errors-2.42.4.stderr")
	execution := RunResult{
		Path: "/usr/bin/findmnt", ExitCode: 1,
		Stdout: stdout, Stderr: stderr, Duration: 129 * time.Millisecond,
		Err: &exec.ExitError{},
	}
	verification := NewFindmntVerifier(&fakeFindmntRunner{result: execution}).Verify(context.Background(), "/private/candidate")

	assertFindmntCodes(t, verification.Report,
		"findmnt.parse_error/error", "findmnt.parse_error/error",
		"findmnt.error/error", "findmnt.warning/warning",
		"findmnt.error/error", "findmnt.warning/warning", "findmnt.warning/warning")
	assertFindmntDiagnosticContract(t, verification.Report)
	if !verification.Report.HasErrors() {
		t.Fatal("errors fixture должен блокировать сохранение")
	}
	if verification.Report.Diagnostics[0].Line != 3 || verification.Report.Diagnostics[1].Line != 4 {
		t.Fatalf("номера parse errors: got %d и %d, want 3 и 4",
			verification.Report.Diagnostics[0].Line, verification.Report.Diagnostics[1].Line)
	}
	for i, diagnostic := range verification.Report.Diagnostics[2:] {
		if diagnostic.Line != 0 {
			t.Errorf("stdout diagnostic %d не должен копировать heading как Line: %+v", i, diagnostic)
		}
	}
	encoded := mustFindmntReportJSON(t, verification.Report)
	for _, raw := range []string{
		"/dev/sda3", "/private/candidate", "/mnt/data", "relative",
		"unreachable on boot", "cannot detect", "No such file or directory",
	} {
		if strings.Contains(encoded, raw) {
			t.Errorf("Report раскрывает raw detail %q: %s", raw, encoded)
		}
	}
	if !reflect.DeepEqual(verification.Execution, execution) {
		t.Fatalf("Execution изменён:\ngot  %+v\nwant %+v", verification.Execution, execution)
	}
	if verification.Execution.Stdout != stdout || verification.Execution.Stderr != stderr {
		t.Fatal("raw stdout/stderr не сохранены в Execution")
	}
}

// TestFindmntVerifyErrorDetailAtZeroExit проверяет, что exit 0 не отменяет [E].
func TestFindmntVerifyErrorDetailAtZeroExit(t *testing.T) {
	execution := RunResult{ExitCode: 0, Stdout: "0 parse errors, 1 errors, 0 warnings\n/private/heading\n   [E] sensitive detail\n"}
	verification := NewFindmntVerifier(&fakeFindmntRunner{result: execution}).Verify(context.Background(), "/private/candidate")

	assertFindmntCodes(t, verification.Report, "findmnt.error/error")
	assertFindmntDiagnosticContract(t, verification.Report)
	if !verification.Report.HasErrors() {
		t.Fatal("[E] при exit 0 обязан оставаться error")
	}
}

// TestFindmntVerifySummaryWithoutDetails проверяет generic parse/error по counts.
func TestFindmntVerifySummaryWithoutDetails(t *testing.T) {
	execution := RunResult{ExitCode: 0, Stdout: "1 parse errors, 1 errors, 0 warnings\n"}
	verification := NewFindmntVerifier(&fakeFindmntRunner{result: execution}).Verify(context.Background(), "/private/candidate")

	assertFindmntCodes(t, verification.Report, "findmnt.parse_error/error", "findmnt.error/error")
	assertFindmntDiagnosticContract(t, verification.Report)
	for i, diagnostic := range verification.Report.Diagnostics {
		if !reflect.DeepEqual(diagnostic.Params, map[string]string{"count": "1"}) {
			t.Errorf("summary diagnostic %d Params: got %#v", i, diagnostic.Params)
		}
	}
}

// TestFindmntVerifySummaryFallbacks проверяет числовой count и одну generic
// диагностику на категорию независимо от величины summary count.
func TestFindmntVerifySummaryFallbacks(t *testing.T) {
	tests := []struct {
		name     string
		stdout   string
		wantCode string
		count    string
	}{
		{name: "parse", stdout: "2 parse errors, 0 errors, 0 warnings\n", wantCode: "findmnt.parse_error/error", count: "2"},
		{name: "errors", stdout: "0 parse errors, 2 errors, 0 warnings\n", wantCode: "findmnt.error/error", count: "2"},
		{name: "warnings", stdout: "0 parse errors, 0 errors, 3 warnings\n", wantCode: "findmnt.warning/warning", count: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			execution := RunResult{ExitCode: 0, Stdout: tt.stdout}
			verification := NewFindmntVerifier(&fakeFindmntRunner{result: execution}).Verify(context.Background(), "/private/candidate")
			assertFindmntCodes(t, verification.Report, tt.wantCode)
			assertFindmntDiagnosticContract(t, verification.Report)
			if !reflect.DeepEqual(verification.Report.Diagnostics[0].Params, map[string]string{"count": tt.count}) {
				t.Fatalf("summary Params: got %#v, want count=%s", verification.Report.Diagnostics[0].Params, tt.count)
			}
		})
	}
}

// TestFindmntVerifyNonzeroWarningsAddsFailure проверяет объяснение необъяснённого
// ненулевого exit отдельной ошибкой даже при наличии warnings.
func TestFindmntVerifyNonzeroWarningsAddsFailure(t *testing.T) {
	execution := RunResult{ExitCode: 7, Stdout: "0 parse errors, 0 errors, 1 warnings\nnone\n   [W] sensitive warning\n"}
	verification := NewFindmntVerifier(&fakeFindmntRunner{result: execution}).Verify(context.Background(), "/private/candidate")

	assertFindmntCodes(t, verification.Report, "findmnt.warning/warning", "findmnt.failed/error")
	assertFindmntDiagnosticContract(t, verification.Report)
	failure := verification.Report.Diagnostics[1]
	if !reflect.DeepEqual(failure.Params, map[string]string{"exit_code": "7"}) {
		t.Fatalf("failure Params: got %#v, want numeric exit_code=7", failure.Params)
	}
}

// TestFindmntVerifyNonzeroWithoutOutput проверяет numeric exit_code и отсутствие
// argv, candidate path и технических raw details в сообщении generic failure.
func TestFindmntVerifyNonzeroWithoutOutput(t *testing.T) {
	execution := RunResult{ExitCode: 19}
	verification := NewFindmntVerifier(&fakeFindmntRunner{result: execution}).Verify(context.Background(), "/private/candidate --verify")

	assertFindmntCodes(t, verification.Report, "findmnt.failed/error")
	assertFindmntDiagnosticContract(t, verification.Report)
	failure := verification.Report.Diagnostics[0]
	if !reflect.DeepEqual(failure.Params, map[string]string{"exit_code": "19"}) {
		t.Fatalf("failure Params: got %#v, want numeric exit_code=19", failure.Params)
	}
	for _, raw := range []string{"/private/candidate", "--verify", "--tab-file", "19"} {
		if strings.Contains(failure.Message, raw) {
			t.Errorf("Message раскрывает %q: %q", raw, failure.Message)
		}
	}
}

// TestFindmntVerifyExecutionErrors проверяет специальные сбои запуска до разбора
// output: частичные [E]/[W] и Err.Error не должны превращаться в обычные findings.
func TestFindmntVerifyExecutionErrors(t *testing.T) {
	rawOutput := "1 parse errors, 1 errors, 1 warnings\n/private/heading\n [E] sensitive\n [W] sensitive\n"
	tests := []struct {
		name      string
		execution RunResult
		wantCode  string
	}{
		{
			name: "start",
			execution: RunResult{ExitCode: -1, Stdout: rawOutput,
				Stderr: "findmnt: /private/candidate: parse error at line 8 -- ignored",
				Err:    errors.New("start /private/candidate: секрет запуска")},
			wantCode: "findmnt.unavailable/error",
		},
		{
			name: "timeout",
			execution: RunResult{ExitCode: -1, Stdout: rawOutput, Stderr: "private timeout output",
				TimedOut: true, Err: context.DeadlineExceeded},
			wantCode: "findmnt.timeout/error",
		},
		{
			name: "canceled",
			execution: RunResult{ExitCode: -1, Stdout: rawOutput, Stderr: "private cancel output",
				Err: context.Canceled},
			wantCode: "findmnt.canceled/error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verification := NewFindmntVerifier(&fakeFindmntRunner{result: tt.execution}).Verify(context.Background(), "/private/candidate")
			assertFindmntCodes(t, verification.Report, tt.wantCode)
			assertFindmntDiagnosticContract(t, verification.Report)
			encoded := mustFindmntReportJSON(t, verification.Report)
			for _, raw := range []string{"/private/candidate", "секрет запуска", "private timeout output", "private cancel output", "sensitive"} {
				if strings.Contains(encoded, raw) {
					t.Errorf("Report раскрывает %q: %s", raw, encoded)
				}
			}
		})
	}
}

// TestFindmntVerifyTruncation проверяет обязательную ошибку при потере любого
// потока, в том числе вместе со специальной ошибкой запуска.
func TestFindmntVerifyTruncation(t *testing.T) {
	partialWarnings := "0 parse errors, 0 errors, 1 warnings\nnone\n   [W] обрезан"
	tests := []struct {
		name      string
		execution RunResult
		wantCodes []string
	}{
		{
			name:      "stdout",
			execution: RunResult{ExitCode: 0, Stdout: partialWarnings, StdoutTruncated: true},
			wantCodes: []string{"findmnt.warning/warning", "findmnt.output_truncated/error"},
		},
		{
			name:      "stderr",
			execution: RunResult{ExitCode: 0, Stdout: findmntFixture(t, "valid-2.42.4.stdout"), StderrTruncated: true},
			wantCodes: []string{"findmnt.output_truncated/error"},
		},
		{
			name: "timeout and stdout",
			execution: RunResult{ExitCode: -1, Stdout: partialWarnings, StdoutTruncated: true,
				TimedOut: true, Err: context.DeadlineExceeded},
			wantCodes: []string{"findmnt.timeout/error", "findmnt.output_truncated/error"},
		},
		{
			name:      "start and stderr",
			execution: RunResult{ExitCode: -1, StderrTruncated: true, Err: errors.New("private start error")},
			wantCodes: []string{"findmnt.unavailable/error", "findmnt.output_truncated/error"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verification := NewFindmntVerifier(&fakeFindmntRunner{result: tt.execution}).Verify(context.Background(), "/private/candidate")
			assertFindmntCodes(t, verification.Report, tt.wantCodes...)
			assertFindmntDiagnosticContract(t, verification.Report)
			if !verification.Report.HasErrors() {
				t.Fatal("truncation обязана блокировать сохранение")
			}
		})
	}
}

// mustFindmntReportJSON сериализует именно публичный Report, а не Execution.
func mustFindmntReportJSON(t *testing.T, report models.Report) string {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
