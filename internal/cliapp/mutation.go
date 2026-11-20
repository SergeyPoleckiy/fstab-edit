// Файл mutation.go готовит preview одной доменной мутации fstab для CLI.
// Он копирует документ, применяет ровно одну операцию editor, строит unified
// diff и внутреннюю валидацию candidate. Реальная запись, lock и backup
// подключаются позже через transaction; здесь нет ввода-вывода и глобального
// состояния.
package cliapp

import (
	"bytes"
	"errors"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/diff"
	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/validator"
	"fstab-edit/pkg/models"
)

// MutationKind определяет одну доменную операцию над записью fstab.
type MutationKind uint8

const (
	// MutationAdd добавляет новую запись.
	MutationAdd MutationKind = iota
	// MutationEdit изменяет отдельные поля существующей записи.
	MutationEdit
	// MutationDelete удаляет узел существующей записи.
	MutationDelete
	// MutationComment отключает активную запись префиксом '#'.
	MutationComment
	// MutationUncomment включает закомментированную запись.
	MutationUncomment
)

// MutationRequest описывает один запрос на подготовку мутации. Заполненность
// полей зависит от Kind: Add использует NewEntry, Edit — Selector и Changes,
// Delete/Comment/Uncomment — только Selector.
type MutationRequest struct {
	Kind     MutationKind
	Selector editor.Selector
	NewEntry editor.NewEntry
	Changes  editor.Changes
}

// MutationPreview описывает результат подготовки мутации без её сохранения.
// Tasks 2–4 используют Changed и Operation для формирования transaction.Plan.
type MutationPreview struct {
	// Changed равен true, когда candidate байтово отличается от original.
	Changed bool
	// Operation фиксирует вид операции для backup metadata.
	Operation backup.Operation
	// Original содержит сериализованные байты исходного документа.
	Original []byte
	// Candidate содержит сериализованные байты после применения операции;
	// slice является независимой копией и не разделяет память с document.
	Candidate []byte
	// Diff содержит unified diff original→candidate; пуст при Changed == false.
	Diff string
	// Report содержит внутренние диагностики candidate документа.
	Report models.Report
}

// operationForKind сопоставляет вид мутации и backup operation. Неизвестный
// вид — ошибка вызывающего кода, а не пользовательских данных.
func operationForKind(kind MutationKind) (backup.Operation, bool) {
	switch kind {
	case MutationAdd:
		return backup.OperationBeforeAdd, true
	case MutationEdit:
		return backup.OperationBeforeEdit, true
	case MutationDelete:
		return backup.OperationBeforeDelete, true
	case MutationComment:
		return backup.OperationBeforeComment, true
	case MutationUncomment:
		return backup.OperationBeforeUncomment, true
	default:
		return "", false
	}
}

// applyMutation применяет ровно одну editor-операцию к candidate-документу.
// Ошибки неоднозначности выделяются отдельно, чтобы сохранить candidate lines.
func applyMutation(document *models.Document, request MutationRequest) *ExitError {
	var err error
	switch request.Kind {
	case MutationAdd:
		_, err = editor.Add(document, request.NewEntry)
	case MutationEdit:
		_, err = editor.Edit(document, request.Selector, request.Changes)
	case MutationDelete:
		_, err = editor.Delete(document, request.Selector)
	case MutationComment:
		_, err = editor.Comment(document, request.Selector)
	case MutationUncomment:
		_, err = editor.Uncomment(document, request.Selector)
	default:
		return ValidationErrorK(nil, "error.validation.unknown_mutation", "неизвестный вид мутации")
	}
	if err == nil {
		return nil
	}
	var ambiguity *editor.AmbiguityError
	if errors.As(err, &ambiguity) {
		return AmbiguityErrorK(err, ambiguity.Lines, "error.validation.selector_ambiguous", "селектор неоднозначен")
	}
	// ErrNotFound, ErrWrongState, ErrInvalidChange, ErrEmptyField и ошибки
	// parser.EncodeField — недопустимые данные или изменение, code 1.
	return ValidationErrorK(err, "error.validation.mutation_not_applied", "мутация не применена")
}

// PrepareMutation строит preview одной мутации, ничего не сохраняя и не
// изменяя переданный document: копия получается повторным разбором сериализации.
// Candidate копируется, diff строится как Unified("<original>", "<candidate>"),
// Report наполняется validator.Validate(candidate). Ошибки операций отдаются
// как *ExitError: неоднозначность — code 8 с Candidates, остальные — code 1.
func PrepareMutation(document *models.Document, request MutationRequest) (*MutationPreview, *ExitError) {
	if document == nil {
		return nil, ValidationErrorK(editor.ErrNilDocument, "error.validation.document_not_set", "документ не задан")
	}
	operation, ok := operationForKind(request.Kind)
	if !ok {
		return nil, ValidationErrorK(nil, "error.validation.unknown_mutation", "неизвестный вид мутации")
	}

	original := append([]byte(nil), document.Serialize()...)
	candidateDocument := parser.Parse(document.Serialize())
	if exit := applyMutation(candidateDocument, request); exit != nil {
		return nil, exit
	}
	candidate := append([]byte(nil), candidateDocument.Serialize()...)

	preview := &MutationPreview{
		Changed:   !bytes.Equal(original, candidate),
		Operation: operation,
		Original:  original,
		Candidate: candidate,
		Report:    validator.Validate(candidateDocument),
	}
	if preview.Changed {
		preview.Diff = diff.Unified("<original>", "<candidate>", original, candidate)
	}
	return preview, nil
}
