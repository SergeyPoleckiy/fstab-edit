// Package editor_test содержит fuzz-проверки селекторов и round-trip мутаций.
package editor_test

import (
	"bytes"
	"errors"
	"testing"

	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// FuzzSelect ищет panic, неверные индексы и частичные совпадения на произвольных
// документах. Успешный результат обязан указывать на запись с точным decoded value.
func FuzzSelect(f *testing.F) {
	f.Add([]byte("UUID=a /mnt/a\\040b ext4 defaults 0 0\n"), "/mnt/a b")
	f.Add([]byte("LABEL=My\\040Disk /mnt/disabled ext4 defaults 0 0\n"), "LABEL=My Disk")
	f.Add([]byte("# UUID=a /mnt/a ext4 defaults 0 0\nUUID=a /mnt/a ext4 defaults 0 0\n"), "UUID=a")
	f.Add([]byte{0x00, 0xff, '\n'}, "")

	f.Fuzz(func(t *testing.T, input []byte, value string) {
		document := parser.Parse(input)
		if document == nil {
			t.Fatal("parser вернул nil document")
		}

		selectors := []struct {
			selector editor.Selector
			field    func(*models.Entry) string
		}{
			{selector: editor.ByMountPoint(value), field: func(entry *models.Entry) string {
				return entry.MountPoint.Value
			}},
			{selector: editor.BySource(value), field: func(entry *models.Entry) string {
				return entry.Source.Value
			}},
		}
		for _, test := range selectors {
			index, err := editor.Select(document, test.selector)
			if err != nil {
				if !errors.Is(err, editor.ErrNotFound) &&
					!errors.Is(err, editor.ErrAmbiguous) &&
					!errors.Is(err, editor.ErrInvalidSelector) {
					t.Fatalf("неожиданная ошибка для selector %#v: %v", test.selector, err)
				}
				continue
			}
			if index < 0 || index >= len(document.Nodes) {
				t.Fatalf("index вне документа: index=%d nodes=%d", index, len(document.Nodes))
			}
			entry := document.Nodes[index].Entry
			if entry == nil {
				t.Fatalf("успешный selector указывает на узел без записи: index=%d", index)
			}
			if got := test.field(entry); got != value {
				t.Fatalf("неточное совпадение: got %q, want %q", got, value)
			}
		}
	})
}

// FuzzCommentRoundTrip выбирает первую активную запись и проверяет, что успешный
// Comment → Uncomment по той же physical line восстанавливает исходные байты.
// Ошибка conservative classification допустима, но не должна изменить документ.
func FuzzCommentRoundTrip(f *testing.F) {
	f.Add([]byte("UUID=a /mnt/a ext4 defaults 0 0\n"))
	f.Add([]byte("  LABEL=A\\040B\t/mnt/a  ext4\tdefaults 0 1\r\n# tail\n"))
	f.Add([]byte("UUID=a relative/mount ext4 defaults 0 0\n"))
	f.Add([]byte("# header\nUUID=a /mnt/a ext4 defaults\nUUID=b /mnt/b ext4 defaults 0 2"))

	f.Fuzz(func(t *testing.T, input []byte) {
		document := parser.Parse(input)
		if document == nil {
			t.Fatal("parser вернул nil document")
		}
		line := 0
		for index, node := range document.Nodes {
			if node.Kind == models.NodeEntry {
				line = index + 1
				break
			}
		}
		if line == 0 {
			return
		}

		before := document.Serialize()
		base := document.BaseSHA256
		changed, err := editor.Comment(document, editor.ByLine(line))
		if err != nil {
			if changed {
				t.Fatal("Comment вернул changed=true вместе с ошибкой")
			}
			if !bytes.Equal(document.Serialize(), before) {
				t.Fatalf("Comment изменил документ после ошибки:\ngot  %q\nwant %q", document.Serialize(), before)
			}
			if document.BaseSHA256 != base {
				t.Fatal("Comment изменил base hash после ошибки")
			}
			return
		}
		if !changed {
			t.Fatal("успешный Comment обязан изменить активную запись")
		}

		changed, err = editor.Uncomment(document, editor.ByLine(line))
		if err != nil || !changed {
			t.Fatalf("Uncomment после Comment: changed=%v err=%v", changed, err)
		}
		if !bytes.Equal(document.Serialize(), before) {
			t.Fatalf("round-trip не восстановил документ:\ngot  %q\nwant %q", document.Serialize(), before)
		}
		if document.BaseSHA256 != base {
			t.Fatal("round-trip изменил base hash")
		}
	})
}
