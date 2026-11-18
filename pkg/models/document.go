// Package models содержит типы данных, которыми обмениваются parser,
// валидатор, транзакционный writer и пользовательские интерфейсы.
package models

// NodeKind определяет структурную роль одной физической строки документа.
// Значения являются машиночитаемыми и не зависят от выбранной локали.
type NodeKind uint8

const (
	// NodeBlank обозначает строку, содержащую только пробелы или табуляции.
	NodeBlank NodeKind = iota
	// NodeComment обозначает обычный комментарий, не являющийся записью fstab.
	NodeComment
	// NodeEntry обозначает активную запись с четырьмя—шестью полями.
	NodeEntry
	// NodeCommentedEntry обозначает отключённую запись с префиксом '#'.
	NodeCommentedEntry
	// NodeInvalid обозначает активную строку, которую нельзя разобрать как запись.
	NodeInvalid
)

// Field хранит одновременно исходное и декодированное представление поля.
// Start и End указывают byte offsets в Node.Raw и позволяют позднее заменить
// только это поле, не переформатируя остальную строку.
type Field struct {
	Raw   string
	Value string
	Start int
	End   int
}

// Entry описывает активную или закомментированную запись fstab.
// Dump и Pass являются указателями, поскольку эти optional-поля могут
// отсутствовать; nil не подменяется значением по умолчанию при разборе.
type Entry struct {
	Line       int
	Active     bool
	Source     Field
	MountPoint Field
	FSType     Field
	Options    Field
	Dump       *Field
	Pass       *Field
}

// Node сохраняет исходное содержимое физической строки без line ending.
// Raw и Ending вместе обеспечивают побайтовый round-trip, а Entry заполнено
// только для NodeEntry и NodeCommentedEntry.
type Node struct {
	Kind       NodeKind
	Raw        string
	Ending     string
	Entry      *Entry
	ParseError string
}

// Document представляет fstab как упорядоченный набор физических строк.
// BaseSHA256 фиксирует исходную версию и позднее защитит от перезаписи
// изменений, сделанных другой программой после загрузки документа.
type Document struct {
	Nodes      []Node
	BaseSHA256 [32]byte
}

// Serialize собирает сохранённые content lines и их исходные endings.
// Метод не нормализует данные и поэтому безопасен даже для некорректных строк.
// Возвращается новый slice, чтобы вызывающий код не изменял внутренние строки.
func (d *Document) Serialize() []byte {
	if d == nil {
		return nil
	}

	size := 0
	for _, node := range d.Nodes {
		size += len(node.Raw) + len(node.Ending)
	}

	result := make([]byte, 0, size)
	for _, node := range d.Nodes {
		result = append(result, node.Raw...)
		result = append(result, node.Ending...)
	}
	return result
}
