// Package parser_test проверяет parser только через его публичный контракт.
// Так тесты не закрепляют внутренние вспомогательные функции реализации.
package parser_test

import (
	"bytes"
	"testing"

	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// TestRoundTrip проверяет главный безопасный инвариант: отсутствие операции
// редактирования не должно изменить ни одного исходного байта.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	cases := map[string][]byte{
		"пустой документ":          {},
		"обычная запись":           []byte("UUID=abc / ext4 defaults 0 1\n"),
		"без завершающего newline": []byte("/dev/sda1 / ext4 defaults 0 1"),
		"CRLF":              []byte("/dev/sda1 / ext4 defaults 0 1\r\n"),
		"смешанные endings": []byte("# comment\r\n\n/dev/sda1 / ext4 defaults 0 1"),
		"пробелы и обычный комментарий": []byte(" \t \n# обычный комментарий\n"),
		"некорректная строка":           []byte("not an fstab entry\n"),
		"invalid UTF-8":                 []byte{0xff, 0xfe, '\n'},
	}

	for name, input := range cases {
		name, input := name, input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := parser.Parse(input)
			if got := document.Serialize(); !bytes.Equal(got, input) {
				t.Fatalf("round-trip изменил байты:\ninput: %q\ngot:   %q", input, got)
			}
		})
	}
}

// TestNodeKinds проверяет отличие обычных комментариев и отключённых записей.
// Обычный текст с несколькими словами не должен становиться записью fstab.
func TestNodeKinds(t *testing.T) {
	t.Parallel()

	input := []byte("# обычный комментарий\n" +
		"UUID=abc / ext4 defaults 0 1\n" +
		"# /dev/sdb1 /mnt/data\\040disk ext4 defaults,noatime 0 2\n" +
		" \t\n" +
		"broken\n")
	document := parser.Parse(input)

	want := []models.NodeKind{
		models.NodeComment,
		models.NodeEntry,
		models.NodeCommentedEntry,
		models.NodeBlank,
		models.NodeInvalid,
	}
	if len(document.Nodes) != len(want) {
		t.Fatalf("количество узлов: got %d, want %d", len(document.Nodes), len(want))
	}
	for index, kind := range want {
		if document.Nodes[index].Kind != kind {
			t.Errorf("узел %d: got kind %v, want %v", index, document.Nodes[index].Kind, kind)
		}
	}
	if document.Nodes[4].ParseError == "" {
		t.Error("для invalid-строки должна сохраняться причина ошибки")
	}
}

// TestEntryFields проверяет декодирование escaped mount point и byte spans.
// Spans должны указывать на исходный raw text, а не на decoded string.
func TestEntryFields(t *testing.T) {
	t.Parallel()

	input := []byte("  LABEL=My\\040Disk\t/mnt/data\\040disk\text4\tdefaults,noatime\t0\t2")
	document := parser.Parse(input)
	if len(document.Nodes) != 1 || document.Nodes[0].Entry == nil {
		t.Fatal("ожидалась одна активная запись")
	}

	entry := document.Nodes[0].Entry
	if entry.Line != 1 || !entry.Active {
		t.Fatalf("неверные line/active: line=%d active=%v", entry.Line, entry.Active)
	}
	if entry.Source.Value != "LABEL=My Disk" {
		t.Errorf("decoded source: %q", entry.Source.Value)
	}
	if entry.MountPoint.Value != "/mnt/data disk" {
		t.Errorf("decoded mount point: %q", entry.MountPoint.Value)
	}
	if entry.Dump == nil || entry.Dump.Value != "0" {
		t.Errorf("dump не разобран: %#v", entry.Dump)
	}
	if entry.Pass == nil || entry.Pass.Value != "2" {
		t.Errorf("pass не разобран: %#v", entry.Pass)
	}

	node := document.Nodes[0]
	for name, field := range map[string]models.Field{
		"source":  entry.Source,
		"mount":   entry.MountPoint,
		"type":    entry.FSType,
		"options": entry.Options,
	} {
		if field.Start < 0 || field.End > len(node.Raw) || field.Start >= field.End {
			t.Fatalf("%s: некорректный span %d..%d", name, field.Start, field.End)
		}
		if got := node.Raw[field.Start:field.End]; got != field.Raw {
			t.Errorf("%s: span содержит %q, raw поля %q", name, got, field.Raw)
		}
	}
}

// TestOptionalFields проверяет, что отсутствующие dump/pass остаются nil,
// а parser не подставляет значения по умолчанию в существующий документ.
func TestOptionalFields(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"/dev/sda1 / ext4 defaults",
		"/dev/sda1 / ext4 defaults 0",
	} {
		document := parser.Parse([]byte(input))
		entry := document.Nodes[0].Entry
		if entry == nil {
			t.Fatalf("запись не разобрана: %q", input)
		}
		if len(input) == len("/dev/sda1 / ext4 defaults") && entry.Dump != nil {
			t.Errorf("dump должен отсутствовать: %q", input)
		}
		if entry.Pass != nil {
			t.Errorf("pass должен отсутствовать: %q", input)
		}
	}
}

// TestCommentedEntrySpans проверяет, что offsets включают исходный префикс '#'.
// Это необходимо для последующей точечной замены поля в полной raw-строке.
func TestCommentedEntrySpans(t *testing.T) {
	t.Parallel()

	document := parser.Parse([]byte("# /dev/sdb1 /mnt/data ext4 defaults 0 0\n"))
	node := document.Nodes[0]
	if node.Kind != models.NodeCommentedEntry || node.Entry == nil {
		t.Fatalf("ожидалась закомментированная запись: %#v", node)
	}
	if node.Entry.Active {
		t.Error("закомментированная запись не должна быть активной")
	}
	if got := node.Raw[node.Entry.Source.Start:node.Entry.Source.End]; got != "/dev/sdb1" {
		t.Errorf("source span: %q", got)
	}
	if node.Entry.Source.Start != 2 {
		t.Errorf("source должен начинаться после '# ': got %d", node.Entry.Source.Start)
	}
}
