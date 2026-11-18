// Package editor содержит операции поиска и последующего изменения записей fstab.
package editor

import (
	"errors"
	"fmt"

	"fstab-edit/pkg/models"
)

var (
	// ErrNilDocument означает, что вызывающий код не передал документ.
	ErrNilDocument = errors.New("документ не задан")
	// ErrInvalidSelector означает некорректный вид или значение селектора.
	ErrInvalidSelector = errors.New("некорректный селектор")
	// ErrNotFound означает отсутствие записи, соответствующей селектору.
	ErrNotFound = errors.New("запись не найдена")
	// ErrAmbiguous означает, что селектор соответствует нескольким записям.
	ErrAmbiguous = errors.New("селектор соответствует нескольким записям")
)

// SelectorKind определяет поле записи, используемое для однозначного выбора.
type SelectorKind uint8

const (
	// SelectorLine выбирает запись по one-based номеру физической строки.
	SelectorLine SelectorKind = iota
	// SelectorMountPoint выбирает запись по decoded mount point.
	SelectorMountPoint
	// SelectorSource выбирает запись по decoded source.
	SelectorSource
)

// Selector описывает критерий выбора одной активной или закомментированной записи.
type Selector struct {
	// Kind задаёт способ интерпретации Line или Value.
	Kind SelectorKind
	// Line содержит one-based номер физической строки для SelectorLine.
	Line int
	// Value содержит decoded значение для SelectorMountPoint или SelectorSource.
	Value string
}

// ByLine создаёт селектор по one-based номеру физической строки.
func ByLine(line int) Selector { return Selector{Kind: SelectorLine, Line: line} }

// ByMountPoint создаёт селектор по полному decoded значению mount point.
func ByMountPoint(value string) Selector {
	return Selector{Kind: SelectorMountPoint, Value: value}
}

// BySource создаёт селектор по полному decoded значению source.
func BySource(value string) Selector {
	return Selector{Kind: SelectorSource, Value: value}
}

// AmbiguityError сообщает исходный селектор и физические строки всех кандидатов.
type AmbiguityError struct {
	// Selector сохраняет критерий, который оказался неоднозначным.
	Selector Selector
	// Lines содержит one-based номера физических строк в порядке документа.
	Lines []int
}

// Error формирует диагностическое сообщение со списком строк-кандидатов.
func (e *AmbiguityError) Error() string {
	return fmt.Sprintf("селектор соответствует строкам %v", e.Lines)
}

// Unwrap связывает подробную ошибку неоднозначности с ErrAmbiguous.
func (e *AmbiguityError) Unwrap() error { return ErrAmbiguous }

// Select возвращает zero-based индекс узла записи, выбранной однозначно.
// Активные и закомментированные записи равноправно участвуют в поиске.
func Select(document *models.Document, selector Selector) (int, error) {
	// Nil document не имеет узлов и должен отличаться от отсутствующей записи.
	if document == nil {
		return -1, ErrNilDocument
	}
	// Селектор строки адресует физический узел, а не порядковый номер записи.
	if selector.Kind == SelectorLine {
		if selector.Line < 1 {
			return -1, ErrInvalidSelector
		}
		if selector.Line > len(document.Nodes) {
			return -1, ErrNotFound
		}
		index := selector.Line - 1
		if document.Nodes[index].Entry == nil {
			return -1, ErrNotFound
		}
		return index, nil
	}
	// Пустые decoded значения и неизвестные виды селекторов недопустимы.
	if selector.Value == "" || (selector.Kind != SelectorMountPoint && selector.Kind != SelectorSource) {
		return -1, ErrInvalidSelector
	}

	// Собираются все совпадения: первое найденное значение нельзя возвращать,
	// пока не доказано отсутствие других активных или закомментированных кандидатов.
	match := -1
	var lines []int
	for index, node := range document.Nodes {
		if node.Kind != models.NodeEntry && node.Kind != models.NodeCommentedEntry {
			continue
		}
		value := node.Entry.Source.Value
		if selector.Kind == SelectorMountPoint {
			value = node.Entry.MountPoint.Value
		}
		if value != selector.Value {
			continue
		}
		match = index
		lines = append(lines, index+1)
	}
	if len(lines) == 0 {
		return -1, ErrNotFound
	}
	// Несколько кандидатов возвращаются CLI вместе с их физическими строками.
	if len(lines) > 1 {
		return -1, &AmbiguityError{Selector: selector, Lines: lines}
	}
	return match, nil
}
