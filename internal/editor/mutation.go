// Файл mutation.go содержит общую транзакционную инфраструктуру слоя editor.
// Он копирует models.Document, повторно разбирает изменённые узлы через parser,
// выбирает line endings и атомарно коммитит candidate только при реальном diff.
// Этот уровень отделяет операции над записями из entry.go от моделей и сохраняет
// BaseSHA256 для последующей проверки внешних изменений транзакционным writer-ом.
// Здесь нет файлового ввода-вывода: все побочные эффекты ограничены памятью вызывающего.
package editor

import (
	"bytes"
	"errors"
	"fmt"

	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

var (
	// ErrWrongState означает, что структура выбранного узла не позволяет
	// безопасно выполнить запрошенную операцию над записью.
	ErrWrongState = errors.New("операция не соответствует состоянию записи")
	// ErrInvalidChange означает противоречивый набор изменений, недопустимый comment text
	// или недопустимое итоговое сочетание optional-полей Dump и Pass.
	ErrInvalidChange = errors.New("некорректный набор изменений")
	// ErrEmptyField означает попытку записать пустое значение в явно заданное поле.
	ErrEmptyField = errors.New("значение поля не может быть пустым")
)

// cloneDocument копирует структуру документа и slice узлов перед мутацией.
// Изменяемые узлы затем заменяются целиком, поэтому shallow copy указателей Entry
// безопасен: существующие Entry до commitDocument не изменяются.
func cloneDocument(document *models.Document) *models.Document {
	clone := *document
	clone.Nodes = append([]models.Node(nil), document.Nodes...)
	return &clone
}

// commitDocument атомарно переносит candidate в исходный документ только при
// реальном изменении сериализованных байтов. BaseSHA256 копируется без пересчёта.
func commitDocument(document, candidate *models.Document) bool {
	if bytes.Equal(document.Serialize(), candidate.Serialize()) {
		return false
	}
	for index := range candidate.Nodes {
		if candidate.Nodes[index].Entry != nil {
			candidate.Nodes[index].Entry.Line = index + 1
		}
	}
	*document = *candidate
	return true
}

// rebuildNode заново разбирает изменённую raw-строку, чтобы обновить Field spans
// и указатели Entry. Исходные ending, физический номер и вид узла восстанавливаются;
// любая потеря распознавания отменяет операцию с ErrWrongState.
func rebuildNode(raw, ending string, line int, want models.NodeKind) (models.Node, error) {
	parsed := parser.Parse([]byte(raw))
	if len(parsed.Nodes) != 1 || parsed.Nodes[0].Kind != want || parsed.Nodes[0].Entry == nil {
		return models.Node{}, fmt.Errorf("%w: изменённая строка не распознана", ErrWrongState)
	}
	node := parsed.Nodes[0]
	node.Ending = ending
	node.Entry.Line = line
	return node, nil
}

// dominantEnding выбирает CRLF только при строгом преобладании над LF.
// При равенстве и в пустом документе используется LF.
func dominantEnding(document *models.Document) string {
	lf, crlf := 0, 0
	for _, node := range document.Nodes {
		switch node.Ending {
		case "\n":
			lf++
		case "\r\n":
			crlf++
		}
	}
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}
