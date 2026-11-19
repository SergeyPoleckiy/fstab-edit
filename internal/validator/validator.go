// Package validator выполняет детерминированные внутренние проверки активных
// записей fstab. Он не обращается к среде, файлам и внешним командам.
package validator

import "fstab-edit/pkg/models"

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
	return collector.report
}
