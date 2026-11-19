// Файл comment.go реализует in-memory переключение активности записей fstab.
// Обе операции сохраняют исходные whitespace и line endings, повторно классифицируют
// изменённую строку и атомарно коммитят candidate только после успешного разбора.
package editor

import (
	"strings"

	"fstab-edit/pkg/models"
)

// associatedCommentRange возвращает непрерывный NodeComment block непосредственно
// перед записью. Блок с индексом 0 неотличим от заголовка документа, поэтому
// намеренно сохраняется и никогда не считается связанным даже для первой записи.
func associatedCommentRange(document *models.Document, entryIndex int) (int, int) {
	start := entryIndex
	for start > 0 && document.Nodes[start-1].Kind == models.NodeComment {
		start--
	}
	if start == 0 {
		return entryIndex, entryIndex
	}
	return start, entryIndex
}

// makeCommentNodes создаёт по одному "# "-узлу на каждую LF-разделённую строку.
// NUL и CR отклоняются, чтобы текст не создавал недопустимые байты или новые endings.
func makeCommentNodes(text, ending string) ([]models.Node, error) {
	if strings.ContainsAny(text, "\x00\r") {
		return nil, ErrInvalidChange
	}
	lines := strings.Split(text, "\n")
	nodes := make([]models.Node, len(lines))
	for index, line := range lines {
		nodes[index] = models.Node{Kind: models.NodeComment, Raw: "# " + line, Ending: ending}
	}
	return nodes, nil
}

// Comment добавляет "# " перед исходным Raw активной записи, выбранной selector.
// Изменённая строка обязана пройти conservative classification как NodeCommentedEntry;
// при ошибке или неподходящем состоянии document остаётся неизменным. BaseSHA256 сохраняется.
func Comment(document *models.Document, selector Selector) (bool, error) {
	index, err := Select(document, selector)
	if err != nil {
		return false, err
	}
	node := document.Nodes[index]
	if node.Kind != models.NodeEntry || node.Entry == nil {
		return false, ErrWrongState
	}

	candidate := cloneDocument(document)
	changedNode, err := rebuildNode("# "+node.Raw, node.Ending, index+1, models.NodeCommentedEntry)
	if err != nil {
		return false, err
	}
	candidate.Nodes[index] = changedNode
	return commitDocument(document, candidate), nil
}

// Uncomment удаляет первый non-space '#' и максимум один следующий space/tab.
// Остальные leading spaces и исходный ending сохраняются. Строка обязана пересобраться
// как NodeEntry; при любой ошибке исходный document не коммитит частичные изменения.
func Uncomment(document *models.Document, selector Selector) (bool, error) {
	index, err := Select(document, selector)
	if err != nil {
		return false, err
	}
	node := document.Nodes[index]
	if node.Kind != models.NodeCommentedEntry || node.Entry == nil {
		return false, ErrWrongState
	}

	hash := 0
	for hash < len(node.Raw) && (node.Raw[hash] == ' ' || node.Raw[hash] == '\t') {
		hash++
	}
	if hash == len(node.Raw) || node.Raw[hash] != '#' {
		return false, ErrWrongState
	}
	end := hash + 1
	if end < len(node.Raw) && (node.Raw[end] == ' ' || node.Raw[end] == '\t') {
		end++
	}

	candidate := cloneDocument(document)
	changedNode, err := rebuildNode(node.Raw[:hash]+node.Raw[end:], node.Ending, index+1, models.NodeEntry)
	if err != nil {
		return false, err
	}
	candidate.Nodes[index] = changedNode
	return commitDocument(document, candidate), nil
}
