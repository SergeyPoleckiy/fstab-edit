// Package models содержит общие модели документа и диагностик. Модель Diagnostic
// не зависит от validator, CLI или внешних утилит и пригодна для объединения
// внутренних и внешних результатов проверки.
package models

// Severity определяет влияние диагностики на сохранение документа.
type Severity string

const (
	// SeverityError обозначает недопустимый документ и запрещает сохранение.
	SeverityError Severity = "error"
	// SeverityWarning обозначает риск, который требует отображения и подтверждения.
	SeverityWarning Severity = "warning"
	// SeverityInfo обозначает пояснение без блокировки сохранения.
	SeverityInfo Severity = "info"
)

// Diagnostic описывает один стабильный результат проверки. Line — one-based
// physical line или 0; Field — стабильное имя поля; Value заполняется только
// при отсутствии риска раскрытия секретов. Params содержит структурированные
// данные для i18n/output, а Message — текущее русское человекочитаемое сообщение.
type Diagnostic struct {
	Code     string            `json:"code"`
	Severity Severity          `json:"severity"`
	Line     int               `json:"line,omitempty"`
	Field    string            `json:"field,omitempty"`
	Value    string            `json:"value,omitempty"`
	Message  string            `json:"message"`
	Params   map[string]string `json:"params,omitempty"`
}

// Report объединяет диагностики всех внутренних и внешних проверок.
type Report struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// HasErrors сообщает, запрещает ли хотя бы одна диагностика сохранение.
// Метод не изменяет report и безопасен для нулевого Report.
func (r Report) HasErrors() bool {
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}

// MergeReports объединяет диагностики внутренних и внешних проверок в порядке
// аргументов и в порядке следования элементов каждого Report. Функция всегда
// возвращает Report с non-nil Diagnostics, включая отсутствие аргументов.
// Элементы копируются в новый slice: исходные отчёты не изменяются и не делят
// backing array с результатом, поэтому последующий append к любому из них
// не перезаписывает диагностики другого Report.
func MergeReports(reports ...Report) Report {
	total := 0
	for _, report := range reports {
		total += len(report.Diagnostics)
	}
	merged := Report{Diagnostics: make([]Diagnostic, 0, total)}
	for _, report := range reports {
		merged.Diagnostics = append(merged.Diagnostics, report.Diagnostics...)
	}
	return merged
}
