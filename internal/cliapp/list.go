// Файл list.go описывает view-модель результата команды list: raw/decoded
// представления шести полей записи, состояние active/commented, связанный
// комментарий и диагностики. Типы не выполняют ввода-вывода и не содержат
// mutable globals: это чистые данные, которые service (Task 3) заполнит из
// parser/validator, а internal/output (Task 2) сериализует в schema v1.
package cliapp

import "fstab-edit/pkg/models"

// FieldView описывает одно поле записи fstab в двух представлениях: Raw —
// исходный текст поля из документа, Decoded — decoded значение. Present false
// означает, что поле отсутствует в строке (например, dump/pass у части записей);
// renderers отображают такое поле как missing.
type FieldView struct {
	Raw     string `json:"raw"`
	Decoded string `json:"decoded"`
	Present bool   `json:"present"`
}

// EntryView описывает одну запись документа для вывода команды list. Line —
// one-based номер физической строки; State принимает значения "active" или
// "commented"; Comment — associated comment, связанный с записью комментарий
// без маркера '#'. Diagnostics содержит диагностики именно этой записи и всегда
// non-nil у сформированного service результата.
type EntryView struct {
	Line        int                 `json:"line"`
	State       string              `json:"state"`
	Source      FieldView           `json:"source"`
	MountPoint  FieldView           `json:"mount_point"`
	FSType      FieldView           `json:"fs_type"`
	Options     FieldView           `json:"options"`
	Dump        FieldView           `json:"dump"`
	Pass        FieldView           `json:"pass"`
	Comment     string              `json:"comment,omitempty"`
	Diagnostics []models.Diagnostic `json:"diagnostics"`
}

// ListResult описывает результат команды list: target, записи документа в
// исходном порядке и document-level диагностики, не привязанные к одной записи.
type ListResult struct {
	Target      string              `json:"target"`
	Entries     []EntryView         `json:"entries"`
	Diagnostics []models.Diagnostic `json:"diagnostics"`
}
