// Package editor_test содержит fuzz-проверку селекторов decoded значений.
package editor_test

import (
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
