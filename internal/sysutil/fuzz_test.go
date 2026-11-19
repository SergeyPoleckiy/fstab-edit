// Файл fuzz_test.go проверяет чистый классификатор findmnt на произвольных
// строках и любом int exit code. Fuzzing не вызывает Runner, не выполняет I/O и
// сериализует только Report: raw Execution не считается безопасным для журнала.
package sysutil

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"fstab-edit/pkg/models"
)

// FuzzClassifyFindmnt проверяет отсутствие panic, детерминированность, контракт
// diagnostics и redaction. Устойчивые prefix-маркеры исключают ложное совпадение
// короткого fuzz input с числовыми exit_code/count в сериализованном Report.
func FuzzClassifyFindmnt(f *testing.F) {
	seeds := []struct {
		stdout   string
		stderr   string
		exitCode int
	}{
		{stdout: "", stderr: "", exitCode: 0},
		{stdout: "0 parse errors, 0 errors, 0 warnings\n", stderr: "", exitCode: 0},
		{stdout: "/synthetic\n   [E] sensitive error\n", stderr: "", exitCode: 0},
		{stdout: "0 parse errors, 0 errors, 1 warnings\n/synthetic\n [W] sensitive warning\n", stderr: "", exitCode: 7},
		{stdout: "", stderr: "findmnt: /private/candidate: parse error at line 12 -- ignored\n", exitCode: 1},
		{stdout: "\xff\x00[E] invalid UTF-8", stderr: "\xfe parse error at line 999999999999999999999", exitCode: -1},
		{stdout: "999999999999999999999 parse errors, 999999999999999999999 errors, 999999999999999999999 warnings", stderr: "", exitCode: 0},
		{stdout: "\n\t[E] after blank\n", stderr: "", exitCode: -2147483648},
		{stdout: "quote \" backslash \\ newline\nnext stdout", stderr: "quote \" backslash \\ newline\nnext stderr", exitCode: 0},
		{stdout: "", stderr: "", exitCode: 2147483647},
	}
	for _, seed := range seeds {
		f.Add(seed.stdout, seed.stderr, seed.exitCode)
	}

	f.Fuzz(func(t *testing.T, stdout, stderr string, exitCode int) {
		sensitiveStdout := "do-not-leak-stdout:" + stdout
		sensitiveStderr := "do-not-leak-stderr:" + stderr
		execution := RunResult{
			Stdout: sensitiveStdout, Stderr: sensitiveStderr,
			ExitCode: exitCode, Err: nil,
			StdoutTruncated: false, StderrTruncated: false,
		}

		first := classifyFindmnt(execution)
		second := classifyFindmnt(execution)
		if first.Diagnostics == nil || second.Diagnostics == nil {
			t.Fatal("Report.Diagnostics должен быть non-nil")
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("classifyFindmnt не детерминирован:\nfirst  %+v\nsecond %+v", first, second)
		}
		for index, diagnostic := range first.Diagnostics {
			if strings.TrimSpace(diagnostic.Code) == "" || strings.TrimSpace(diagnostic.Message) == "" {
				t.Fatalf("диагностика %d имеет пустой Code/Message: %+v", index, diagnostic)
			}
			switch diagnostic.Severity {
			case models.SeverityError, models.SeverityWarning, models.SeverityInfo:
			default:
				t.Fatalf("диагностика %d имеет недопустимую Severity %q", index, diagnostic.Severity)
			}
		}

		serialized, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("Report не сериализуется: %v", err)
		}
		encoded := string(serialized)
		for _, prefix := range []string{"do-not-leak-stdout:", "do-not-leak-stderr:"} {
			if strings.Contains(encoded, prefix) {
				t.Fatalf("Report раскрывает маркерный raw output с prefix %q: %s", prefix, encoded)
			}
		}

		hasErrorLine := false
		for _, line := range strings.Split(sensitiveStdout, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "[E]") {
				hasErrorLine = true
				break
			}
		}
		if hasErrorLine && !first.HasErrors() {
			t.Fatalf("[E] при exitCode=%d не установил HasErrors", exitCode)
		}

		trimmedStdout := strings.TrimSpace(stdout)
		onlySuccessfulOutput := strings.TrimSpace(stderr) == "" &&
			(trimmedStdout == "" || trimmedStdout == "0 parse errors, 0 errors, 0 warnings")
		if exitCode == 0 && onlySuccessfulOutput && first.HasErrors() {
			t.Fatalf("пустой/success summary при exit 0 дал error Report: %+v", first)
		}
	})
}
