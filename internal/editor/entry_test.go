// Package editor_test проверяет in-memory мутации через публичный API editor.
package editor_test

import (
	"bytes"
	"errors"
	"testing"

	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// stringPointer позволяет компактно описывать optional-изменения в table tests.
func stringPointer(value string) *string { return &value }

// assertUnchanged проверяет атомарность ошибки и сохранение исходного base hash.
func assertUnchanged(t *testing.T, document *models.Document, before []byte, base [32]byte) {
	t.Helper()
	if !bytes.Equal(document.Serialize(), before) {
		t.Fatalf("документ изменился после ошибки:\ngot  %q\nwant %q", document.Serialize(), before)
	}
	if document.BaseSHA256 != base {
		t.Fatal("mutation не должна пересчитывать base hash")
	}
}

// TestEditMinimalDiff проверяет замену только raw Options при неодинаковых separators.
func TestEditMinimalDiff(t *testing.T) {
	t.Parallel()

	input := []byte("  UUID=a\t/mnt/a  ext4\tdefaults 0 1\n# untouched\r\n")
	document := parser.Parse(input)
	base := document.BaseSHA256
	changed, err := editor.Edit(document, editor.ByLine(1), editor.Changes{Options: stringPointer("defaults,noatime")})
	if err != nil || !changed {
		t.Fatalf("edit: changed=%v err=%v", changed, err)
	}
	want := []byte("  UUID=a\t/mnt/a  ext4\tdefaults,noatime 0 1\n# untouched\r\n")
	if !bytes.Equal(document.Serialize(), want) {
		t.Fatalf("minimal diff:\ngot  %q\nwant %q", document.Serialize(), want)
	}
	if document.BaseSHA256 != base {
		t.Fatal("mutation не должна пересчитывать base hash")
	}
}

// TestEditNoopPreservesRawEscape проверяет сравнение decoded значений: совпадение
// не должно нормализовать нестандартный octal escape или другие байты документа.
func TestEditNoopPreservesRawEscape(t *testing.T) {
	t.Parallel()

	input := []byte("LABEL=A\\041B /mnt/a ext4 defaults\\054noatime 0 1\r\n")
	document := parser.Parse(input)
	base := document.BaseSHA256
	changed, err := editor.Edit(document, editor.ByLine(1), editor.Changes{Options: stringPointer("defaults,noatime")})
	if err != nil || changed {
		t.Fatalf("no-op: changed=%v err=%v", changed, err)
	}
	assertUnchanged(t, document, input, base)
}

// TestEditOptionalFields проверяет добавление Dump/Pass и удаление suffix-полей
// вместе с separator перед ними, но без удаления trailing whitespace.
func TestEditOptionalFields(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		input   string
		changes editor.Changes
		want    string
	}{
		{
			name:    "Dump добавляется к четырёхполевой записи одним tab",
			input:   "UUID=a /mnt/a ext4 defaults\n",
			changes: editor.Changes{Dump: stringPointer("0")},
			want:    "UUID=a /mnt/a ext4 defaults\t0\n",
		},
		{
			name:    "Dump и Pass добавляются одной операцией в заданном порядке",
			input:   "UUID=a /mnt/a ext4 defaults\n",
			changes: editor.Changes{Dump: stringPointer("1"), Pass: stringPointer("2")},
			want:    "UUID=a /mnt/a ext4 defaults\t1\t2\n",
		},
		{
			name:    "UnsetPass удаляет непрерывный separator и сохраняет trailing whitespace",
			input:   "UUID=a /mnt/a ext4 defaults  0 \t 2  \t\n",
			changes: editor.Changes{UnsetPass: true},
			want:    "UUID=a /mnt/a ext4 defaults  0  \t\n",
		},
		{
			name:    "совместный UnsetDump и UnsetPass удаляет общий suffix",
			input:   "UUID=a /mnt/a ext4 defaults \t 0  2  \t\n",
			changes: editor.Changes{UnsetDump: true, UnsetPass: true},
			want:    "UUID=a /mnt/a ext4 defaults  \t\n",
		},
		{
			name:    "Dump добавляется после замены Options без сдвига исходных spans",
			input:   "  UUID=a\t/mnt/a  ext4\tdefaults   \n",
			changes: editor.Changes{Options: stringPointer("defaults,noatime"), Dump: stringPointer("1")},
			want:    "  UUID=a\t/mnt/a  ext4\tdefaults,noatime\t1   \n",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte(test.input))
			base := document.BaseSHA256
			changed, err := editor.Edit(document, editor.ByLine(1), test.changes)
			if err != nil || !changed {
				t.Fatalf("edit: changed=%v err=%v", changed, err)
			}
			if got := string(document.Serialize()); got != test.want {
				t.Fatalf("optional fields:\ngot  %q\nwant %q", got, test.want)
			}
			if document.BaseSHA256 != base {
				t.Fatal("base hash изменился")
			}
		})
	}
}

// TestEditRejectsInvalidChangesAtomically проверяет typed errors для конфликтов,
// пустых и управляющих значений, нарушенной зависимости Pass от Dump и пустого Changes.
func TestEditRejectsInvalidChangesAtomically(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		input   string
		changes editor.Changes
		want    error
	}{
		{
			name:    "Dump конфликтует с UnsetDump",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Dump: stringPointer("1"), UnsetDump: true},
			want:    editor.ErrInvalidChange,
		},
		{
			name:    "Pass конфликтует с UnsetPass",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Pass: stringPointer("2"), UnsetPass: true},
			want:    editor.ErrInvalidChange,
		},
		{
			name:    "пустое обязательное значение",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Source: stringPointer("")},
			want:    editor.ErrEmptyField,
		},
		{
			name:    "NUL в новом значении",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Source: stringPointer("UUID=\x00bad")},
			want:    parser.ErrInvalidField,
		},
		{
			name:    "newline в новом значении",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Options: stringPointer("defaults\nnoatime")},
			want:    parser.ErrInvalidField,
		},
		{
			name:    "пустой Changes",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{},
			want:    editor.ErrInvalidChange,
		},
		{
			name:    "Pass без итогового Dump",
			input:   "UUID=a /mnt/a ext4 defaults\n",
			changes: editor.Changes{Pass: stringPointer("2")},
			want:    editor.ErrInvalidChange,
		},
		{
			name:    "UnsetDump при оставшемся Pass",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{UnsetDump: true},
			want:    editor.ErrInvalidChange,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte(test.input))
			before := document.Serialize()
			base := document.BaseSHA256
			changed, err := editor.Edit(document, editor.ByLine(1), test.changes)
			if changed || !errors.Is(err, test.want) {
				t.Fatalf("changed=%v err=%v, want error %v", changed, err, test.want)
			}
			assertUnchanged(t, document, before, base)
		})
	}
}

// TestEditRebuildErrorIsAtomic проверяет, что даже ошибка пересборки candidate
// не коммитит частичные изменения в исходный документ.
func TestEditRebuildErrorIsAtomic(t *testing.T) {
	t.Parallel()

	document := parser.Parse([]byte("UUID=a /mnt/a ext4 defaults 0 1\n"))
	document.Nodes[0].Kind = models.NodeInvalid
	before := document.Serialize()
	base := document.BaseSHA256
	changed, err := editor.Edit(document, editor.ByLine(1), editor.Changes{Options: stringPointer("noatime")})
	if changed || !errors.Is(err, editor.ErrWrongState) {
		t.Fatalf("changed=%v err=%v, want ErrWrongState", changed, err)
	}
	assertUnchanged(t, document, before, base)
}

// TestDeletePreservesOtherNodes проверяет удаление ровно выбранной строки,
// сохранение comments/blank lines с endings и пересчёт физических Entry.Line.
func TestDeletePreservesOtherNodes(t *testing.T) {
	t.Parallel()

	input := []byte("# header\r\n\nUUID=a /mnt/a ext4 defaults 0 1\n# UUID=b /mnt/b ext4 defaults 0 0\r\nUUID=c /mnt/c ext4 defaults 0 2")
	document := parser.Parse(input)
	base := document.BaseSHA256
	changed, err := editor.Delete(document, editor.ByLine(3))
	if err != nil || !changed {
		t.Fatalf("delete: changed=%v err=%v", changed, err)
	}
	want := []byte("# header\r\n\n# UUID=b /mnt/b ext4 defaults 0 0\r\nUUID=c /mnt/c ext4 defaults 0 2")
	if !bytes.Equal(document.Serialize(), want) {
		t.Fatalf("delete diff:\ngot  %q\nwant %q", document.Serialize(), want)
	}
	if len(document.Nodes) != 4 || document.Nodes[2].Entry.Line != 3 || document.Nodes[3].Entry.Line != 4 {
		t.Fatalf("Entry.Line не пересчитаны: %#v", document.Nodes)
	}
	if document.BaseSHA256 != base {
		t.Fatal("base hash изменился")
	}
}

// TestMutationNilAndSelectorErrors проверяет ErrNilDocument и отсутствие мутации,
// когда Select завершился ошибкой до создания candidate.
func TestMutationNilAndSelectorErrors(t *testing.T) {
	t.Parallel()

	changes := editor.Changes{Options: stringPointer("noatime")}
	if changed, err := editor.Add(nil, editor.NewEntry{}); changed || !errors.Is(err, editor.ErrNilDocument) {
		t.Fatalf("Add(nil): changed=%v err=%v", changed, err)
	}
	if changed, err := editor.Edit(nil, editor.ByLine(1), changes); changed || !errors.Is(err, editor.ErrNilDocument) {
		t.Fatalf("Edit(nil): changed=%v err=%v", changed, err)
	}
	if changed, err := editor.Delete(nil, editor.ByLine(1)); changed || !errors.Is(err, editor.ErrNilDocument) {
		t.Fatalf("Delete(nil): changed=%v err=%v", changed, err)
	}

	for _, operation := range []struct {
		name string
		run  func(*models.Document) (bool, error)
	}{
		{name: "Edit", run: func(document *models.Document) (bool, error) {
			return editor.Edit(document, editor.ByLine(99), changes)
		}},
		{name: "Delete", run: func(document *models.Document) (bool, error) {
			return editor.Delete(document, editor.ByLine(99))
		}},
	} {
		document := parser.Parse([]byte("UUID=a /mnt/a ext4 defaults 0 1\n"))
		before := document.Serialize()
		base := document.BaseSHA256
		changed, err := operation.run(document)
		if changed || !errors.Is(err, editor.ErrNotFound) {
			t.Errorf("%s: changed=%v err=%v", operation.name, changed, err)
		}
		assertUnchanged(t, document, before, base)
	}
}

// TestAddLineEndings проверяет шесть полей с defaults, выбор преобладающего
// ending, победу LF при равенстве и добавление ending прежней последней строке.
func TestAddLineEndings(t *testing.T) {
	t.Parallel()

	inputEntry := editor.NewEntry{Source: "/dev/sda1", MountPoint: "/mnt/a", FSType: "ext4", Options: "defaults"}
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "пустой документ получает LF",
			input: "",
			want:  "/dev/sda1\t/mnt/a\text4\tdefaults\t0\t0\n",
		},
		{
			name:  "CRLF преобладает",
			input: "# one\r\n# two\r\n# three\n",
			want:  "# one\r\n# two\r\n# three\n/dev/sda1\t/mnt/a\text4\tdefaults\t0\t0\r\n",
		},
		{
			name:  "при равенстве выбирается LF",
			input: "# one\r\n# two\n",
			want:  "# one\r\n# two\n/dev/sda1\t/mnt/a\text4\tdefaults\t0\t0\n",
		},
		{
			name:  "прежний последний node без ending получает выбранный ending",
			input: "# header",
			want:  "# header\n/dev/sda1\t/mnt/a\text4\tdefaults\t0\t0\n",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte(test.input))
			base := document.BaseSHA256
			changed, err := editor.Add(document, inputEntry)
			if err != nil || !changed {
				t.Fatalf("add: changed=%v err=%v", changed, err)
			}
			if got := string(document.Serialize()); got != test.want {
				t.Fatalf("line endings:\ngot  %q\nwant %q", got, test.want)
			}
			if document.BaseSHA256 != base {
				t.Fatal("base hash изменился")
			}
		})
	}
}

// TestAddEscapesFields проверяет octal encoding пробелов, tabs и backslash,
// а также обратное декодирование всех значений новой записи parser-ом.
func TestAddEscapesFields(t *testing.T) {
	t.Parallel()

	document := parser.Parse(nil)
	base := document.BaseSHA256
	input := editor.NewEntry{
		Source:     "LABEL=A B\tC\\D",
		MountPoint: "/mnt/a b\tc\\d",
		FSType:     "ext4",
		Options:    "defaults",
		Dump:       "1",
		Pass:       "2",
	}
	changed, err := editor.Add(document, input)
	if err != nil || !changed {
		t.Fatalf("add: changed=%v err=%v", changed, err)
	}
	want := "LABEL=A\\040B\\011C\\134D\t/mnt/a\\040b\\011c\\134d\text4\tdefaults\t1\t2\n"
	if got := string(document.Serialize()); got != want {
		t.Fatalf("escaping:\ngot  %q\nwant %q", got, want)
	}
	reparsed := parser.Parse(document.Serialize())
	entry := reparsed.Nodes[0].Entry
	if entry == nil || entry.Source.Value != input.Source || entry.MountPoint.Value != input.MountPoint ||
		entry.FSType.Value != input.FSType || entry.Options.Value != input.Options ||
		entry.Dump == nil || entry.Dump.Value != input.Dump || entry.Pass == nil || entry.Pass.Value != input.Pass {
		t.Fatalf("decoded values не совпали: %#v", entry)
	}
	if document.BaseSHA256 != base {
		t.Fatal("base hash изменился")
	}
}

// TestAddRejectsInvalidInputAtomically проверяет четыре обязательных поля и
// запрет управляющих символов без частичного добавления новой записи.
func TestAddRejectsInvalidInputAtomically(t *testing.T) {
	t.Parallel()

	valid := editor.NewEntry{Source: "UUID=a", MountPoint: "/mnt/a", FSType: "ext4", Options: "defaults"}
	for _, test := range []struct {
		name   string
		mutate func(*editor.NewEntry)
		want   error
	}{
		{name: "пустой Source", mutate: func(input *editor.NewEntry) { input.Source = "" }, want: editor.ErrEmptyField},
		{name: "пустой MountPoint", mutate: func(input *editor.NewEntry) { input.MountPoint = "" }, want: editor.ErrEmptyField},
		{name: "пустой FSType", mutate: func(input *editor.NewEntry) { input.FSType = "" }, want: editor.ErrEmptyField},
		{name: "пустой Options", mutate: func(input *editor.NewEntry) { input.Options = "" }, want: editor.ErrEmptyField},
		{name: "NUL", mutate: func(input *editor.NewEntry) { input.Source = "UUID=\x00bad" }, want: parser.ErrInvalidField},
		{name: "newline", mutate: func(input *editor.NewEntry) { input.MountPoint = "/mnt/bad\nvalue" }, want: parser.ErrInvalidField},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := valid
			test.mutate(&input)
			document := parser.Parse([]byte("# untouched\r\n"))
			before := document.Serialize()
			base := document.BaseSHA256
			changed, err := editor.Add(document, input)
			if changed || !errors.Is(err, test.want) {
				t.Fatalf("changed=%v err=%v, want error %v", changed, err, test.want)
			}
			assertUnchanged(t, document, before, base)
		})
	}
}
