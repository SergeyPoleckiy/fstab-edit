// Файл fuzz_test.go проверяет устойчивость Validate на произвольных байтах,
// неизменность документа и детерминированность диагностик. Дополнительный тест
// сериализованного Report гарантирует отсутствие секретов из source/options.
package validator_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"fstab-edit/internal/parser"
	"fstab-edit/internal/validator"
	"fstab-edit/pkg/models"
)

// FuzzValidate проверяет инварианты на произвольных байтах: отсутствие мутаций,
// детерминированный Report, заполненные diagnostics и только известные severity.
// Seeds покрывают валидную запись, invalid UTF-8, controls, duplicates и escaped
// comma в options; fuzzing не обращается к файловой системе или /etc/fstab.
func FuzzValidate(f *testing.F) {
	seeds := [][]byte{
		[]byte("/dev/sda1 /mnt/data ext4 defaults 0 0\n"),
		[]byte("/dev/sda1 / ext4 rw,noatime 0 1\n"),
		{0xff, 0xfe, ' ', '/', ' ', 'x', ' ', 'r', 'w', ' ', '0', ' ', '0'},
		[]byte("LABEL=A\x00B /mnt/data ext4 defaults 0 0\n"),
		[]byte("/dev/sda1 /mnt/data ext4 rw,rw 0 0\n/dev/sda1 /mnt/data ext4 rw,ro 0 0\n"),
		[]byte("/dev/sda1 /mnt/data ext4 rw\\054ro,noauto,nofail 0 0\n"),
		[]byte("# /dev/sda1 /mnt/data ext4 defaults 0 0\nbroken\n"),
		[]byte(""),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		document := parser.Parse(input)
		before := append([]byte(nil), document.Serialize()...)
		base := document.BaseSHA256
		report := validator.Validate(document)
		if !bytes.Equal(before, document.Serialize()) || base != document.BaseSHA256 {
			t.Fatal("Validate изменил документ")
		}
		for _, diagnostic := range report.Diagnostics {
			switch diagnostic.Severity {
			case models.SeverityError, models.SeverityWarning, models.SeverityInfo:
			default:
				t.Fatalf("неизвестная severity: %q", diagnostic.Severity)
			}
			if diagnostic.Code == "" || diagnostic.Message == "" {
				t.Fatalf("незаполненная diagnostic: %#v", diagnostic)
			}
		}
		if !reflect.DeepEqual(report, validator.Validate(document)) {
			t.Fatal("Validate не детерминирован")
		}
	})
}

// TestDiagnosticsDoNotLeakSourceAndOptions сериализует весь Report, а не только
// отдельные поля. Malformed source, повторённый unknown key с marker в имени,
// secret value и пустой item заставляют несколько правил работать с секретом.
func TestDiagnosticsDoNotLeakSourceAndOptions(t *testing.T) {
	t.Parallel()

	const marker = "do-not-leak"
	input := "user:" + marker + "@example/share /mnt/data cifs " + marker + "=first," + marker + "=second,password=" + marker + ",rw, 0 0"
	report := validator.Validate(parser.Parse([]byte(input)))
	serialized, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Report не сериализуется: %v", err)
	}
	if strings.Contains(string(serialized), marker) {
		t.Fatalf("Report JSON раскрывает секрет source/options: %s", serialized)
	}
}
