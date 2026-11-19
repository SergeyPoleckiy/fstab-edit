// Package editor_test проверяет переключение активности записей без потери форматирования.
package editor_test

import (
	"bytes"
	"errors"
	"testing"

	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// TestCommentPreservesRawAndEnding проверяет добавление префикса перед исходной
// строкой: leading spaces, остальные узлы, CRLF и BaseSHA256 должны сохраниться.
func TestCommentPreservesRawAndEnding(t *testing.T) {
	t.Parallel()

	input := []byte("# header\n  UUID=a /mnt/a ext4 defaults 0 0\r\n# tail\n")
	document := parser.Parse(input)
	base := document.BaseSHA256
	changed, err := editor.Comment(document, editor.ByLine(2))
	if err != nil || !changed {
		t.Fatalf("comment: changed=%v err=%v", changed, err)
	}
	want := []byte("# header\n#   UUID=a /mnt/a ext4 defaults 0 0\r\n# tail\n")
	if !bytes.Equal(document.Serialize(), want) {
		t.Fatalf("comment diff:\ngot  %q\nwant %q", document.Serialize(), want)
	}
	if document.BaseSHA256 != base {
		t.Fatal("comment не должен пересчитывать base hash")
	}
}

// TestUncommentPrefixes проверяет удаление ровно одного '#' и не более одного
// непосредственно следующего space/tab, включая сохранение leading whitespace.
func TestUncommentPrefixes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{
			// После '#' один space служебный, а два исходных leading space обязаны остаться:
			// case обнаруживает TrimSpace и удаление большего числа пробелов.
			name:  "space после hash и исходные leading spaces",
			input: "#   UUID=a /mnt/a ext4 defaults 0 0\r\n",
			want:  "  UUID=a /mnt/a ext4 defaults 0 0\r\n",
		},
		{
			// После '#' удаляется один tab, а следующий за ним tab является частью исходного raw:
			// case обнаруживает удаление всего whitespace вместо одного байта.
			name:  "только один tab после hash",
			input: "\t#\t\tUUID=a /mnt/a ext4 defaults 0 0\n",
			want:  "\t\tUUID=a /mnt/a ext4 defaults 0 0\n",
		},
		{
			// Отсутствие separator после '#' не требует добавления или удаления других байтов:
			// case обнаруживает обязательное ожидание пробела и потерю leading tab.
			name:  "без separator после hash",
			input: "  #UUID=a /mnt/a ext4 defaults 0 0\n",
			want:  "  UUID=a /mnt/a ext4 defaults 0 0\n",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte(test.input))
			base := document.BaseSHA256
			changed, err := editor.Uncomment(document, editor.ByLine(1))
			if err != nil || !changed {
				t.Fatalf("uncomment: changed=%v err=%v", changed, err)
			}
			if got := string(document.Serialize()); got != test.want {
				t.Fatalf("uncomment diff:\ngot  %q\nwant %q", got, test.want)
			}
			if document.BaseSHA256 != base {
				t.Fatal("uncomment не должен пересчитывать base hash")
			}
		})
	}
}

// TestCommentUncommentWrongState проверяет typed errors и отсутствие мутации
// для записи уже в целевом состоянии, а также для обычного comment без Entry.
func TestCommentUncommentWrongState(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input string
		run   func(*models.Document, editor.Selector) (bool, error)
		want  error
	}{
		{
			name:  "Comment отклоняет уже закомментированную запись",
			input: "# UUID=a /mnt/a ext4 defaults 0 0\n",
			run:   editor.Comment,
			want:  editor.ErrWrongState,
		},
		{
			name:  "Uncomment отклоняет активную запись",
			input: "UUID=a /mnt/a ext4 defaults 0 0\n",
			run:   editor.Uncomment,
			want:  editor.ErrWrongState,
		},
		{
			name:  "Comment не меняет обычный comment",
			input: "# просто пояснение\n",
			run:   editor.Comment,
			want:  editor.ErrNotFound,
		},
		{
			name:  "Uncomment не меняет обычный comment",
			input: "# просто пояснение\n",
			run:   editor.Uncomment,
			want:  editor.ErrNotFound,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte(test.input))
			before := document.Serialize()
			base := document.BaseSHA256
			changed, err := test.run(document, editor.ByLine(1))
			if changed || !errors.Is(err, test.want) {
				t.Fatalf("changed=%v err=%v, want error %v", changed, err, test.want)
			}
			assertUnchanged(t, document, before, base)
		})
	}
}

// TestCommentConservativeClassificationErrorIsAtomic проверяет active entry,
// которая после добавления '#' не проходит conservative commented classification.
func TestCommentConservativeClassificationErrorIsAtomic(t *testing.T) {
	t.Parallel()

	input := []byte("UUID=a relative/mount ext4 defaults 0 0\n# untouched\r\n")
	document := parser.Parse(input)
	before := document.Serialize()
	base := document.BaseSHA256
	changed, err := editor.Comment(document, editor.ByLine(1))
	if changed || !errors.Is(err, editor.ErrWrongState) {
		t.Fatalf("changed=%v err=%v, want ErrWrongState", changed, err)
	}
	assertUnchanged(t, document, before, base)
}

// TestCommentUncommentRoundTrip проверяет центральный пример: две операции
// должны восстановить исходные байты, сохранив BaseSHA256 исходного документа.
func TestCommentUncommentRoundTrip(t *testing.T) {
	t.Parallel()

	input := []byte("  UUID=a /mnt/a ext4 defaults 0 0\r\n")
	document := parser.Parse(input)
	base := document.BaseSHA256
	changed, err := editor.Comment(document, editor.ByLine(1))
	if err != nil || !changed || string(document.Serialize()) != "#   UUID=a /mnt/a ext4 defaults 0 0\r\n" {
		t.Fatalf("comment: changed=%v err=%v raw=%q", changed, err, document.Serialize())
	}
	changed, err = editor.Uncomment(document, editor.ByLine(1))
	if err != nil || !changed || !bytes.Equal(document.Serialize(), input) {
		t.Fatalf("uncomment: changed=%v err=%v raw=%q", changed, err, document.Serialize())
	}
	if document.BaseSHA256 != base {
		t.Fatal("round-trip не должен пересчитывать base hash")
	}
}
