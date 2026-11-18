// Package parser_test содержит fuzz-проверку устойчивости parser.
package parser_test

import (
	"bytes"
	"testing"

	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// FuzzRoundTrip ищет panic и потерю данных на произвольных байтах.
// Parser обязан сохранять даже вход, который не является валидным fstab.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("UUID=abc / ext4 defaults 0 1\n"))
	f.Add([]byte("# /dev/sda1 /mnt/a\\040b ext4 defaults 0 0\r\n"))
	f.Add([]byte{0x00, 0xff, '\n'})

	f.Fuzz(func(t *testing.T, input []byte) {
		document := parser.Parse(input)
		if got := document.Serialize(); !bytes.Equal(got, input) {
			t.Fatalf("round-trip изменил байты: input=%q got=%q", input, got)
		}
		for _, node := range document.Nodes {
			if node.Entry == nil {
				continue
			}
			fields := []models.Field{
				node.Entry.Source, node.Entry.MountPoint,
				node.Entry.FSType, node.Entry.Options,
			}
			if node.Entry.Dump != nil {
				fields = append(fields, *node.Entry.Dump)
			}
			if node.Entry.Pass != nil {
				fields = append(fields, *node.Entry.Pass)
			}
			for _, field := range fields {
				if field.Start < 0 || field.Start >= field.End || field.End > len(node.Raw) {
					t.Fatalf("некорректный span %d..%d для raw %q", field.Start, field.End, node.Raw)
				}
				if node.Raw[field.Start:field.End] != field.Raw {
					t.Fatalf("span не совпадает с raw field: %q", field.Raw)
				}
			}
		}
	})
}
