// Package models_test проверяет публичный контракт моделей документа и диагностик.
package models_test

import (
	"encoding/json"
	"testing"

	"fstab-edit/pkg/models"
)

// TestDiagnosticJSON фиксирует machine-readable ключи и enum values первой схемы.
func TestDiagnosticJSON(t *testing.T) {
	t.Parallel()

	diagnostic := models.Diagnostic{
		Code: "mount.relative", Severity: models.SeverityError,
		Line: 4, Field: "mount_point", Value: "relative/path",
		Message: "Точка монтирования должна быть абсолютной",
		Params:  map[string]string{"rule": "absolute"},
	}
	data, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"mount.relative","severity":"error","line":4,"field":"mount_point","value":"relative/path","message":"Точка монтирования должна быть абсолютной","params":{"rule":"absolute"}}`
	if string(data) != want {
		t.Fatalf("JSON:\ngot  %s\nwant %s", data, want)
	}
}

// TestDiagnosticJSONOmitsOptionalFields проверяет, что пустые необязательные
// поля не попадают в JSON, а обязательные поля сохраняются даже с нулевыми значениями.
func TestDiagnosticJSONOmitsOptionalFields(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(models.Diagnostic{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"","severity":"","message":""}`
	if string(data) != want {
		t.Fatalf("JSON:\ngot  %s\nwant %s", data, want)
	}
}

// TestReportHasErrors проверяет, что warning/info не блокируют запись,
// а хотя бы один error устанавливает блокирующий результат.
func TestReportHasErrors(t *testing.T) {
	t.Parallel()

	report := models.Report{Diagnostics: []models.Diagnostic{
		{Code: "fs.unknown", Severity: models.SeverityInfo},
		{Code: "option.unknown", Severity: models.SeverityWarning},
	}}
	if report.HasErrors() {
		t.Fatal("warning и info не должны считаться ошибками")
	}
	report.Diagnostics = append(report.Diagnostics, models.Diagnostic{Code: "parse.invalid", Severity: models.SeverityError})
	if !report.HasErrors() {
		t.Fatal("error должен блокировать сохранение")
	}
}
