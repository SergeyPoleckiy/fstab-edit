package tui

import (
	"bytes"

	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// sessionState хранит несохранённые изменения сессии: исходные байты target,
// стек снапшотов для undo и признак dirty. ТЗ §9.2: изменения накапливаются
// в памяти до сохранения; undo работает до момента сохранения.
type sessionState struct {
	original  []byte   // байты target на момент загрузки
	undoStack [][]byte // снапшоты Serialize() перед каждой мутацией
	dirty     bool
}

// applyOp выполняет одну операцию над документом внутри сессии. Снапшот для
// undo фиксируется только если операция вернула nil и реально изменила
// документ: ошибка (недопустимый селектор/поля) не оставляет след в истории
// и не делает сессию dirty, равно как и no-op операция.
func (s *sessionState) applyOp(document *models.Document, op func(*models.Document) error) error {
	before := document.Serialize()
	if err := op(document); err != nil {
		return err
	}
	after := document.Serialize()
	if bytes.Equal(before, after) {
		return nil // no-op не попадает в историю и не меняет dirty
	}
	s.undoStack = append(s.undoStack, append([]byte(nil), before...))
	// dirty сравнивается с original сессии, а не с дельтой операции: иначе
	// no-op при уже изменённом документе ошибочно сбрасывал бы dirty.
	s.dirty = !bytes.Equal(after, s.original)
	return nil
}

// undo восстанавливает последний снапшот; false при пустом стеке. После
// восстановления dirty пересчитывается сравнением с original, поэтому серия
// undo из нескольких мутаций корректно возвращает dirty=false на original.
func (s *sessionState) undo(document *models.Document) bool {
	if len(s.undoStack) == 0 {
		return false
	}
	last := s.undoStack[len(s.undoStack)-1]
	s.undoStack = s.undoStack[:len(s.undoStack)-1]
	restored := parser.Parse(append([]byte(nil), last...))
	*document = *restored
	s.dirty = !bytes.Equal(document.Serialize(), s.original)
	return true
}

// candidate — сериализованные байты текущего документа для transaction.
func (s *sessionState) candidate(document *models.Document) []byte {
	return document.Serialize()
}
