// Package editor_test проверяет селекторы только через публичный API editor.
package editor_test

import (
	"errors"
	"reflect"
	"testing"

	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// selectorDocument содержит комментарии и blank-строки, чтобы физический
// номер строки нельзя было случайно заменить порядковым номером записи.
func selectorDocument() *models.Document {
	return parser.Parse([]byte("# header\n\nUUID=a /mnt/a\\040b ext4 defaults 0 0\n# LABEL=x /mnt/disabled ext4 defaults 0 0\nUUID=c /mnt/a\\040b xfs defaults 0 0\n"))
}

// TestSelectByLine проверяет zero-based index активной и отключённой записи,
// а также отличие отсутствующей записи от некорректного номера строки.
func TestSelectByLine(t *testing.T) {
	t.Parallel()

	document := selectorDocument()
	for _, test := range []struct {
		name  string
		line  int
		index int
		want  error
	}{
		{name: "активная запись", line: 3, index: 2},
		{name: "закомментированная запись", line: 4, index: 3},
		{name: "обычный комментарий", line: 1, want: editor.ErrNotFound},
		{name: "пустая строка", line: 2, want: editor.ErrNotFound},
		{name: "за пределами документа", line: 6, want: editor.ErrNotFound},
		{name: "нулевая строка", line: 0, want: editor.ErrInvalidSelector},
		{name: "отрицательная строка", line: -1, want: editor.ErrInvalidSelector},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			index, err := editor.Select(document, editor.ByLine(test.line))
			if !errors.Is(err, test.want) {
				t.Fatalf("Select(ByLine(%d)): err=%v, want %v", test.line, err, test.want)
			}
			if test.want == nil && index != test.index {
				t.Fatalf("Select(ByLine(%d)): index=%d, want %d", test.line, index, test.index)
			}
			if test.want != nil && index != -1 {
				t.Fatalf("при ошибке index=%d, want -1", index)
			}
		})
	}
}

// TestSelectDecodedValues проверяет полное сравнение decoded source и mount,
// а не поиск по escaped raw text или по подстроке.
func TestSelectDecodedValues(t *testing.T) {
	t.Parallel()

	document := selectorDocument()
	index, err := editor.Select(document, editor.BySource("UUID=a"))
	if err != nil || index != 2 {
		t.Fatalf("source selector: index=%d err=%v", index, err)
	}
	index, err = editor.Select(document, editor.ByMountPoint("/mnt/disabled"))
	if err != nil || index != 3 {
		t.Fatalf("закомментированная запись: index=%d err=%v", index, err)
	}
	if _, err = editor.Select(document, editor.ByMountPoint("/mnt/a")); !errors.Is(err, editor.ErrNotFound) {
		t.Fatalf("substring не должен совпадать: %v", err)
	}
	if _, err = editor.Select(document, editor.BySource("UUID=missing")); !errors.Is(err, editor.ErrNotFound) {
		t.Fatalf("отсутствующий source: %v", err)
	}

	escaped := parser.Parse([]byte("LABEL=My\\040Disk /mnt/data\\040disk ext4 defaults 0 0\n"))
	for _, test := range []struct {
		name     string
		selector editor.Selector
	}{
		{name: "decoded source", selector: editor.BySource("LABEL=My Disk")},
		{name: "decoded mount", selector: editor.ByMountPoint("/mnt/data disk")},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			index, err := editor.Select(escaped, test.selector)
			if err != nil || index != 0 {
				t.Fatalf("decoded selector: index=%d err=%v", index, err)
			}
		})
	}
}

// TestSelectAmbiguity проверяет кандидатов для одинаковых mount и source.
// Lines содержит физические one-based номера, пригодные для вывода в CLI.
func TestSelectAmbiguity(t *testing.T) {
	t.Parallel()

	document := selectorDocument()
	_, err := editor.Select(document, editor.ByMountPoint("/mnt/a b"))
	var ambiguity *editor.AmbiguityError
	if !errors.As(err, &ambiguity) || !errors.Is(err, editor.ErrAmbiguous) {
		t.Fatalf("ожидалась ambiguity, got %v", err)
	}
	if !reflect.DeepEqual(ambiguity.Lines, []int{3, 5}) {
		t.Fatalf("candidate lines: %v", ambiguity.Lines)
	}
	if !reflect.DeepEqual(ambiguity.Selector, editor.ByMountPoint("/mnt/a b")) {
		t.Fatalf("selector в ошибке: %#v", ambiguity.Selector)
	}

	duplicateSource := parser.Parse([]byte("UUID=same /mnt/a ext4 defaults 0 0\n# UUID=same /mnt/b ext4 defaults 0 0\n"))
	_, err = editor.Select(duplicateSource, editor.BySource("UUID=same"))
	ambiguity = nil
	if !errors.As(err, &ambiguity) || !errors.Is(err, editor.ErrAmbiguous) {
		t.Fatalf("ожидалась ambiguity source, got %v", err)
	}
	if !reflect.DeepEqual(ambiguity.Lines, []int{1, 2}) {
		t.Fatalf("source candidate lines: %v", ambiguity.Lines)
	}
}

// TestSelectInvalidSelectors проверяет ошибки до обхода документа.
func TestSelectInvalidSelectors(t *testing.T) {
	t.Parallel()

	document := selectorDocument()
	if index, err := editor.Select(nil, editor.ByLine(1)); index != -1 || !errors.Is(err, editor.ErrNilDocument) {
		t.Fatalf("nil document: index=%d err=%v", index, err)
	}
	for _, selector := range []editor.Selector{
		editor.ByMountPoint(""),
		editor.BySource(""),
		{Kind: editor.SelectorKind(255), Value: "UUID=a"},
	} {
		if index, err := editor.Select(document, selector); index != -1 || !errors.Is(err, editor.ErrInvalidSelector) {
			t.Errorf("selector %#v: index=%d err=%v", selector, index, err)
		}
	}
}
