// Package validator выполняет детерминированные внутренние проверки активных
// записей fstab. Он не обращается к среде, файлам и внешним командам.
package validator

import (
	"strconv"

	"fstab-edit/pkg/models"
)

// collector накапливает diagnostics в порядке обхода документа и правил.
type collector struct {
	report models.Report
}

// newCollector сразу создаёт non-nil slice, чтобы пустой Report сохранял
// предсказуемое представление при сериализации и сравнении результатов.
func newCollector() *collector {
	return &collector{report: models.Report{Diagnostics: make([]models.Diagnostic, 0)}}
}

// add добавляет одну заполненную диагностику без дедупликации по тексту.
func (c *collector) add(diagnostic models.Diagnostic) {
	c.report.Diagnostics = append(c.report.Diagnostics, diagnostic)
}

// Validate возвращает все внутренние диагностики и не изменяет document.
// Обход идёт только по физическим узлам: неактивные записи и комментарии
// пропускаются, а повторный вызов не зависит от состояния предыдущего.
func Validate(document *models.Document) models.Report {
	collector := newCollector()
	if document == nil {
		collector.add(models.Diagnostic{
			Code:     "document.nil",
			Severity: models.SeverityError,
			Message:  "Документ не задан",
		})
		return collector.report
	}

	for index, node := range document.Nodes {
		switch node.Kind {
		case models.NodeInvalid:
			collector.add(models.Diagnostic{
				Code:     "parse.invalid",
				Severity: models.SeverityError,
				Line:     index + 1,
				Message:  "Активная строка не является корректной записью fstab",
			})
		case models.NodeEntry:
			validateEntry(collector, node.Entry)
		case models.NodeBlank, models.NodeComment, models.NodeCommentedEntry:
			// Отключённые записи и служебные узлы не влияют на конфигурацию.
		}
	}
	validateDuplicates(collector, document)
	return collector.report
}

// validateDuplicates сравнивает decoded mount/source только активных записей.
// Физический номер строки берётся из позиции узла, поэтому comments и blank
// lines корректно учитываются, а NodeCommentedEntry в обход не попадает.
func validateDuplicates(collector *collector, document *models.Document) {
	firstMount := make(map[string]int)
	firstSource := make(map[string]int)
	for index, node := range document.Nodes {
		if node.Kind != models.NodeEntry || node.Entry == nil {
			continue
		}
		line := index + 1
		checkDuplicate(collector, firstMount, node.Entry.MountPoint.Value, line, "mount_point", "duplicate.mount", models.SeverityError)
		checkDuplicate(collector, firstSource, node.Entry.Source.Value, line, "source", "duplicate.source", models.SeverityWarning)
	}
}

// checkDuplicate сохраняет первую строку для каждого непустого decoded value.
// Source никогда не копируется в Diagnostic; mount считается безопасным Value.
func checkDuplicate(collector *collector, firstLines map[string]int, value string, line int, field, code string, severity models.Severity) {
	if value == "" {
		return
	}
	firstLine, exists := firstLines[value]
	if !exists {
		firstLines[value] = line
		return
	}

	diagnostic := models.Diagnostic{
		Code:     code,
		Severity: severity,
		Line:     line,
		Field:    field,
		Message:  "Источник повторяется в активных записях",
		Params:   map[string]string{"first_line": strconv.Itoa(firstLine)},
	}
	if field == "mount_point" {
		diagnostic.Value = value
		diagnostic.Message = "Точка монтирования повторяется в активных записях"
	}
	collector.add(diagnostic)
}
