// Файл findmnt.go связывает безопасный запуск findmnt с обобщёнными диагностиками.
// Сырой вывод остаётся только в Verification.Execution, а Report содержит лишь
// стабильные коды, русские сообщения, номера строк и числовые параметры.
package sysutil

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"fstab-edit/pkg/models"
)

var (
	// findmntParseErrorPattern извлекает только номер строки, отбрасывая путь candidate
	// и остальной потенциально sensitive текст диагностики util-linux.
	findmntParseErrorPattern = regexp.MustCompile(`parse error at line ([0-9]+)`)
	// findmntSummaryPattern соответствует английской summary при принудительной LC_ALL=C.
	findmntSummaryPattern = regexp.MustCompile(`([0-9]+) parse errors?, ([0-9]+) errors?, ([0-9]+) warnings?`)
)

// Verification разделяет безопасный для отображения Report и исходный Execution.
// Execution может содержать sensitive stdout/stderr и не должен сериализоваться как Report.
type Verification struct {
	Report    models.Report
	Execution RunResult
}

// FindmntVerifier выполняет внешнюю проверку candidate через подменяемый runner.
type FindmntVerifier struct {
	runner CommandRunner
}

// NewFindmntVerifier создаёт verifier, зависящий только от интерфейса CommandRunner.
func NewFindmntVerifier(runner CommandRunner) *FindmntVerifier {
	return &FindmntVerifier{runner: runner}
}

// Verify проверяет candidate отдельными аргументами findmnt без shell. При локальной
// ошибке конфигурации runner не вызывается; исходный RunResult всегда сохраняется в Execution.
func (v *FindmntVerifier) Verify(ctx context.Context, path string) Verification {
	if path == "" {
		report := newFindmntReport()
		appendFindmntDiagnostic(&report, "findmnt.invalid_path", models.SeverityError,
			"Путь к проверяемому файлу не задан")
		return Verification{Report: report}
	}
	if v == nil || v.runner == nil {
		report := newFindmntReport()
		appendFindmntDiagnostic(&report, "findmnt.unavailable", models.SeverityError,
			"Утилита findmnt недоступна или не может быть запущена")
		return Verification{Report: report}
	}

	execution := v.runner.Run(ctx, "findmnt", "--verify", "--tab-file", path)
	return Verification{Report: classifyFindmnt(execution), Execution: execution}
}

// classifyFindmnt является чистым преобразованием RunResult в Report: функция не
// вызывает runner, не выполняет I/O и не копирует raw output в сообщения диагностик.
func classifyFindmnt(execution RunResult) models.Report {
	report := newFindmntReport()
	if execution.Err != nil {
		// ExitError означает состоявшийся запуск и ненулевой exit: его stdout/stderr
		// классифицируются обычным образом. Остальные ошибки описывают сам запуск.
		var exitError *exec.ExitError
		ordinaryExit := errors.As(execution.Err, &exitError) &&
			!execution.TimedOut &&
			!errors.Is(execution.Err, context.DeadlineExceeded) &&
			!errors.Is(execution.Err, context.Canceled)
		if !ordinaryExit {
			switch {
			case execution.TimedOut || errors.Is(execution.Err, context.DeadlineExceeded):
				appendFindmntDiagnostic(&report, "findmnt.timeout", models.SeverityError,
					"Проверка findmnt превысила отведённое время")
			case errors.Is(execution.Err, context.Canceled):
				appendFindmntDiagnostic(&report, "findmnt.canceled", models.SeverityError,
					"Проверка findmnt отменена")
			default:
				appendFindmntDiagnostic(&report, "findmnt.unavailable", models.SeverityError,
					"Утилита findmnt недоступна или не может быть запущена")
			}
			appendFindmntTruncation(&report, execution)
			return report
		}
	}

	hasParseErrors := false
	for _, match := range findmntParseErrorPattern.FindAllStringSubmatch(execution.Stderr, -1) {
		line, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		appendFindmntDiagnostic(&report, "findmnt.parse_error", models.SeverityError,
			"findmnt обнаружил ошибку разбора файла")
		report.Diagnostics[len(report.Diagnostics)-1].Line = line
		hasParseErrors = true
	}

	hasErrors := hasParseErrors
	hasErrorDetails := false
	hasWarnings := false
	for _, line := range strings.Split(execution.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "[E]"):
			appendFindmntDiagnostic(&report, "findmnt.error", models.SeverityError,
				"findmnt обнаружил ошибку конфигурации монтирования")
			hasErrors = true
			hasErrorDetails = true
		case strings.HasPrefix(trimmed, "[W]"):
			appendFindmntDiagnostic(&report, "findmnt.warning", models.SeverityWarning,
				"findmnt сообщил о предупреждении")
			hasWarnings = true
		}
	}

	// Summary дополняет только полностью отсутствующие категории detail diagnostics.
	// Один generic finding на категорию сохраняет count, но не раскрывает текст вывода.
	if match := findmntSummaryPattern.FindStringSubmatch(execution.Stdout); len(match) == 4 {
		counts := make([]int, 3)
		for i := range counts {
			count, err := strconv.Atoi(match[i+1])
			if err == nil {
				counts[i] = count
			}
		}
		if counts[0] > 0 && !hasParseErrors {
			appendFindmntSummary(&report, "findmnt.parse_error", models.SeverityError,
				"findmnt обнаружил ошибку разбора файла", counts[0])
			hasErrors = true
		}
		if counts[1] > 0 && !hasErrorDetails {
			appendFindmntSummary(&report, "findmnt.error", models.SeverityError,
				"findmnt обнаружил ошибку конфигурации монтирования", counts[1])
			hasErrors = true
		}
		if counts[2] > 0 && !hasWarnings {
			appendFindmntSummary(&report, "findmnt.warning", models.SeverityWarning,
				"findmnt сообщил о предупреждении", counts[2])
		}
	}

	if execution.ExitCode != 0 && !hasErrors {
		appendFindmntDiagnostic(&report, "findmnt.failed", models.SeverityError,
			"findmnt завершился с ошибкой")
		report.Diagnostics[len(report.Diagnostics)-1].Params = map[string]string{
			"exit_code": strconv.Itoa(execution.ExitCode),
		}
	}
	appendFindmntTruncation(&report, execution)
	return report
}

// newFindmntReport возвращает non-nil slice, чтобы successful JSON имел diagnostics: [].
func newFindmntReport() models.Report {
	return models.Report{Diagnostics: make([]models.Diagnostic, 0)}
}

// appendFindmntDiagnostic добавляет diagnostic без Field, Value и raw Params.
func appendFindmntDiagnostic(report *models.Report, code string, severity models.Severity, message string) {
	report.Diagnostics = append(report.Diagnostics, models.Diagnostic{
		Code: code, Severity: severity, Message: message,
	})
}

// appendFindmntSummary добавляет числовой count как единственный summary-параметр.
func appendFindmntSummary(report *models.Report, code string, severity models.Severity, message string, count int) {
	appendFindmntDiagnostic(report, code, severity, message)
	report.Diagnostics[len(report.Diagnostics)-1].Params = map[string]string{"count": strconv.Itoa(count)}
}

// appendFindmntTruncation не позволяет обрезанному successful output скрыть потерю данных.
func appendFindmntTruncation(report *models.Report, execution RunResult) {
	if execution.StdoutTruncated || execution.StderrTruncated {
		appendFindmntDiagnostic(report, "findmnt.output_truncated", models.SeverityError,
			"Вывод findmnt был обрезан из-за ограничения размера")
	}
}
