// Package models_test проверяет публичный контракт моделей документа и диагностик.
package models_test

import (
	"encoding/json"
	"reflect"
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

// TestMergeReportsEmpty проверяет non-nil empty результат для отсутствующих,
// nil и пустых аргументов: такой Report сразу пригоден для JSON и append.
func TestMergeReportsEmpty(t *testing.T) {
	t.Parallel()

	var nilReports []models.Report
	tests := []struct {
		name    string
		reports []models.Report
	}{
		{name: "no arguments"},
		{name: "nil slice", reports: nilReports},
		{name: "zero reports", reports: []models.Report{{}, {}}},
		{name: "nil diagnostics", reports: []models.Report{{Diagnostics: nil}, {Diagnostics: nil}}},
		{name: "empty diagnostics", reports: []models.Report{{Diagnostics: []models.Diagnostic{}}, {Diagnostics: []models.Diagnostic{}}}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			merged := models.MergeReports(tt.reports...)
			if merged.Diagnostics == nil {
				t.Fatal("MergeReports должен возвращать non-nil Diagnostics")
			}
			if len(merged.Diagnostics) != 0 {
				t.Fatalf("пустые аргументы дали диагностики: %+v", merged.Diagnostics)
			}
			if merged.HasErrors() {
				t.Fatal("пустой Report не должен содержать ошибки")
			}
		})
	}
}

// TestMergeReportsPreservesOrderAndHasErrors проверяет объединение внутренней
// ошибки и внешнего предупреждения без изменения их относительного порядка.
func TestMergeReportsPreservesOrderAndHasErrors(t *testing.T) {
	t.Parallel()

	internal := models.Report{Diagnostics: []models.Diagnostic{
		{Code: "mount.relative", Severity: models.SeverityError, Message: "Внутренняя ошибка"},
		{Code: "fs.unknown", Severity: models.SeverityInfo, Message: "Внутреннее пояснение"},
	}}
	external := models.Report{Diagnostics: []models.Diagnostic{
		{Code: "findmnt.warning", Severity: models.SeverityWarning, Message: "Внешнее предупреждение"},
	}}
	merged := models.MergeReports(internal, external)

	if len(merged.Diagnostics) != 3 {
		t.Fatalf("len: got %d, want 3", len(merged.Diagnostics))
	}
	wantCodes := []string{"mount.relative", "fs.unknown", "findmnt.warning"}
	for i, want := range wantCodes {
		if merged.Diagnostics[i].Code != want {
			t.Errorf("диагностика %d: got %q, want %q", i, merged.Diagnostics[i].Code, want)
		}
	}
	if !merged.HasErrors() {
		t.Fatal("внутренняя error должна сохраняться после объединения")
	}
	if internal.Diagnostics[0].Code != "mount.relative" || external.Diagnostics[0].Code != "findmnt.warning" {
		t.Fatal("исходные Report изменены при объединении")
	}
}

// TestMergeReportsDoesNotShareSourceArrays проверяет копирование элементов и
// неизменность merged при последующем append к source slices с запасом capacity.
func TestMergeReportsDoesNotShareSourceArrays(t *testing.T) {
	t.Parallel()

	internalDiagnostics := make([]models.Diagnostic, 1, 4)
	internalDiagnostics[0] = models.Diagnostic{Code: "internal.error", Severity: models.SeverityError, Message: "Исходная ошибка"}
	externalDiagnostics := make([]models.Diagnostic, 1, 4)
	externalDiagnostics[0] = models.Diagnostic{Code: "external.warning", Severity: models.SeverityWarning, Message: "Исходное предупреждение"}
	internal := models.Report{Diagnostics: internalDiagnostics}
	external := models.Report{Diagnostics: externalDiagnostics}
	originalInternal := models.Report{Diagnostics: append([]models.Diagnostic(nil), internal.Diagnostics...)}
	originalExternal := models.Report{Diagnostics: append([]models.Diagnostic(nil), external.Diagnostics...)}

	merged := models.MergeReports(internal, external)
	merged.Diagnostics[0].Code = "merged.internal.changed"
	merged.Diagnostics[1].Code = "merged.external.changed"

	if !reflect.DeepEqual(internal, originalInternal) {
		t.Fatalf("internal изменился через merged:\ngot  %+v\nwant %+v", internal, originalInternal)
	}
	if !reflect.DeepEqual(external, originalExternal) {
		t.Fatalf("external изменился через merged:\ngot  %+v\nwant %+v", external, originalExternal)
	}

	mergedBeforeSourceAppends := append([]models.Diagnostic(nil), merged.Diagnostics...)
	internal.Diagnostics = append(internal.Diagnostics, models.Diagnostic{Code: "internal.added"})
	external.Diagnostics = append(external.Diagnostics, models.Diagnostic{Code: "external.added"})
	if !reflect.DeepEqual(merged.Diagnostics, mergedBeforeSourceAppends) {
		t.Fatalf("append к source slices изменил merged:\ngot  %+v\nwant %+v", merged.Diagnostics, mergedBeforeSourceAppends)
	}
}
