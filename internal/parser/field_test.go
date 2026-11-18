// Package parser_test содержит проверки кодирования decoded значений полей fstab.
package parser_test

import (
	"errors"
	"testing"

	"fstab-edit/internal/parser"
)

// TestEncodeField проверяет совместимое экранирование separators, backslash
// и начального '#', который иначе изменил бы классификацию строки.
func TestEncodeField(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"LABEL=plain":   "LABEL=plain",
		"LABEL=My Disk": `LABEL=My\040Disk`,
		"LABEL=A\tB":    `LABEL=A\011B`,
		`LABEL=A\B`:     `LABEL=A\134B`,
		"#":             `\043`,
		"метка-диск":    "метка-диск",
	}
	for value, want := range cases {
		got, err := parser.EncodeField(value)
		if err != nil || got != want {
			t.Errorf("EncodeField(%q): got %q, err=%v; want %q", value, got, err, want)
		}
	}
}

// TestEncodeFieldRejectsControls не допускает значения, которые нельзя
// безопасно представить внутри одной физической строки fstab.
func TestEncodeFieldRejectsControls(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"a\x00b", "a\nb", "a\rb"} {
		if _, err := parser.EncodeField(value); !errors.Is(err, parser.ErrInvalidField) {
			t.Errorf("EncodeField(%q): err=%v, want ErrInvalidField", value, err)
		}
	}
}
