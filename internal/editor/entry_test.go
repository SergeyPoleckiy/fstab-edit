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
			// Отсутствующий Dump должен появиться сразу после Options с одним tab:
			// тест обнаруживает переформатирование исходных separators и добавление неявного Pass.
			name:    "Dump добавляется к четырёхполевой записи одним tab",
			input:   "UUID=a /mnt/a ext4 defaults\n",
			changes: editor.Changes{Dump: stringPointer("0")},
			want:    "UUID=a /mnt/a ext4 defaults\t0\n",
		},
		{
			// Два новых suffix-поля независимы от старых separators, но их порядок фиксирован:
			// тест обнаруживает перестановку Pass/Dump и потерю одного из значений.
			name:    "Dump и Pass добавляются одной операцией в заданном порядке",
			input:   "UUID=a /mnt/a ext4 defaults\n",
			changes: editor.Changes{Dump: stringPointer("1"), Pass: stringPointer("2")},
			want:    "UUID=a /mnt/a ext4 defaults\t1\t2\n",
		},
		{
			// Между Dump и Pass специально использован смешанный separator, а после Pass — whitespace:
			// тест обнаруживает остаточный separator и ошибочное удаление trailing whitespace.
			name:    "UnsetPass удаляет непрерывный separator и сохраняет trailing whitespace",
			input:   "UUID=a /mnt/a ext4 defaults  0 \t 2  \t\n",
			changes: editor.Changes{UnsetPass: true},
			want:    "UUID=a /mnt/a ext4 defaults  0  \t\n",
		},
		{
			// Удаление двух optional-полей должно быть одним suffix-range от Options:
			// тест обнаруживает независимые диапазоны, оставляющие separators или часть Dump.
			name:    "совместный UnsetDump и UnsetPass удаляет общий suffix",
			input:   "UUID=a /mnt/a ext4 defaults \t 0  2  \t\n",
			changes: editor.Changes{UnsetDump: true, UnsetPass: true},
			want:    "UUID=a /mnt/a ext4 defaults  \t\n",
		},
		{
			// Замена Options меняет длину raw-строки до вставки Dump, а trailing whitespace должен остаться в конце:
			// тест обнаруживает применение replacements слева направо и вставку после trailing whitespace.
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
			// Одновременные set и unset не имеют однозначного приоритета:
			// тест обнаруживает молчаливый выбор одной операции и частичное изменение Dump.
			name:    "Dump конфликтует с UnsetDump",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Dump: stringPointer("1"), UnsetDump: true},
			want:    editor.ErrInvalidChange,
		},
		{
			// Для Pass конфликт проверяется отдельно от Dump, поскольку у полей разная зависимость:
			// тест обнаруживает неполную валидацию Changes и частичный commit.
			name:    "Pass конфликтует с UnsetPass",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Pass: stringPointer("2"), UnsetPass: true},
			want:    editor.ErrInvalidChange,
		},
		{
			// Пустой Source нельзя принимать как отсутствие изменения или валидное поле:
			// тест обнаруживает потерю typed ErrEmptyField и появление нечитаемой записи.
			name:    "пустое обязательное значение",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Source: stringPointer("")},
			want:    editor.ErrEmptyField,
		},
		{
			// NUL нельзя представить в поле fstab даже через octal escape:
			// тест обнаруживает обход parser.EncodeField и запись управляющего байта в document.
			name:    "NUL в новом значении",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Source: stringPointer("UUID=\x00bad")},
			want:    parser.ErrInvalidField,
		},
		{
			// Newline внутри Options создал бы дополнительную физическую строку:
			// тест обнаруживает нарушение однострочного формата и частичную мутацию при ошибке кодирования.
			name:    "newline в новом значении",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{Options: stringPointer("defaults\nnoatime")},
			want:    parser.ErrInvalidField,
		},
		{
			// Пустой набор изменений отличается от валидного no-op с равным decoded значением:
			// тест обнаруживает ложный успешный Edit и скрытую нормализацию raw-строки.
			name:    "пустой Changes",
			input:   "UUID=a /mnt/a ext4 defaults 0 1\n",
			changes: editor.Changes{},
			want:    editor.ErrInvalidChange,
		},
		{
			// В пятом raw-поле parser видит Dump, поэтому добавлять Pass без Dump нельзя:
			// тест обнаруживает смену семантики поля и появление записи с неверным optional-набором.
			name:    "Pass без итогового Dump",
			input:   "UUID=a /mnt/a ext4 defaults\n",
			changes: editor.Changes{Pass: stringPointer("2")},
			want:    editor.ErrInvalidChange,
		},
		{
			// После удаления Dump старый Pass сместился бы на его позицию:
			// тест обнаруживает неявное превращение Pass в Dump вместо отклонения противоречивого состояния.
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
		{
			// Edit имеет валидные changes, но не существующий selector:
			// case обнаруживает подготовку replacements или частичный commit после ошибки Select.
			name: "Edit",
			run: func(document *models.Document) (bool, error) {
				return editor.Edit(document, editor.ByLine(99), changes)
			},
		},
		{
			// Delete должен использовать тот же ранний выход при ошибке Select:
			// case обнаруживает удаление узла по невалидному index и изменение BaseSHA256.
			name: "Delete",
			run: func(document *models.Document) (bool, error) {
				return editor.Delete(document, editor.ByLine(99))
			},
		},
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
			// В пустом документе нет подсказки ending, а пустые Dump/Pass означают defaults:
			// тест обнаруживает отсутствие LF, неполный набор полей и потерю значений "0".
			name:  "пустой документ получает LF",
			input: "",
			want:  "/dev/sda1\t/mnt/a\text4\tdefaults\t0\t0\n",
		},
		{
			// Два CRLF против одного LF задают строгое преобладание:
			// тест обнаруживает выбор ending последнего узла вместо подсчёта всего документа.
			name:  "CRLF преобладает",
			input: "# one\r\n# two\r\n# three\n",
			want:  "# one\r\n# two\r\n# three\n/dev/sda1\t/mnt/a\text4\tdefaults\t0\t0\r\n",
		},
		{
			// При равном числе endings контракт выбирает LF, а не CRLF или случайный результат:
			// тест обнаруживает нестрогое сравнение счётчиков и недетерминированный выбор.
			name:  "при равенстве выбирается LF",
			input: "# one\r\n# two\n",
			want:  "# one\r\n# two\n/dev/sda1\t/mnt/a\text4\tdefaults\t0\t0\n",
		},
		{
			// Без добавления ending к прежней последней строке новая запись склеилась бы с комментарием:
			// тест обнаруживает потерю границы узлов и неверный ending у добавленной записи.
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
		// Пустой Source проверяется до подстановки defaults и построения candidate:
		// case обнаруживает неверный typed error и добавление записи с пустым первым полем.
		{name: "пустой Source", mutate: func(input *editor.NewEntry) { input.Source = "" }, want: editor.ErrEmptyField},
		// Пустой MountPoint нельзя принимать за отсутствующее optional-значение:
		// case обнаруживает неполную проверку обязательных полей и частичную мутацию документа.
		{name: "пустой MountPoint", mutate: func(input *editor.NewEntry) { input.MountPoint = "" }, want: editor.ErrEmptyField},
		// FSType проверяется отдельно, чтобы таблица не проходила только за счёт валидации Source:
		// case обнаруживает пропуск третьего обязательного поля и появление нечитаемой записи.
		{name: "пустой FSType", mutate: func(input *editor.NewEntry) { input.FSType = "" }, want: editor.ErrEmptyField},
		// Пустой Options также должен давать ErrEmptyField, несмотря на возможный defaults у Dump/Pass:
		// case обнаруживает непоследовательную валидацию четырёх обязательных значений.
		{name: "пустой Options", mutate: func(input *editor.NewEntry) { input.Options = "" }, want: editor.ErrEmptyField},
		// NUL запрещён контрактом EncodeField и не должен попасть в сериализованный документ:
		// case обнаруживает игнорирование ошибки кодирования и commit ранее подготовленного candidate.
		{name: "NUL", mutate: func(input *editor.NewEntry) { input.Source = "UUID=\x00bad" }, want: parser.ErrInvalidField},
		// Newline в MountPoint разбил бы добавляемую запись на две физические строки:
		// case обнаруживает обход однострочного ограничения и частичное добавление узла при ошибке.
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

// TestAddAssociatedComment проверяет вставку комментария непосредственно перед
// новой записью, разбиение LF на отдельные узлы и выбранный ending документа.
func TestAddAssociatedComment(t *testing.T) {
	t.Parallel()

	input := []byte("# header\r\n# second header\r\n")
	document := parser.Parse(input)
	base := document.BaseSHA256
	comment := "первая строка\nвторая строка"
	changed, err := editor.Add(document, editor.NewEntry{
		Source:     "UUID=a",
		MountPoint: "/mnt/a",
		FSType:     "ext4",
		Options:    "defaults",
		Comment:    &comment,
	})
	if err != nil || !changed {
		t.Fatalf("add: changed=%v err=%v", changed, err)
	}
	want := []byte("# header\r\n# second header\r\n# первая строка\r\n# вторая строка\r\nUUID=a\t/mnt/a\text4\tdefaults\t0\t0\r\n")
	if !bytes.Equal(document.Serialize(), want) {
		t.Fatalf("associated comment:\ngot  %q\nwant %q", document.Serialize(), want)
	}
	if document.BaseSHA256 != base {
		t.Fatal("add не должен пересчитывать base hash")
	}
}

// TestAddCommentSeparatesPreviousLastLine проверяет, что отсутствующий ending
// прежней последней строки добавляется перед comment nodes, а не после них.
func TestAddCommentSeparatesPreviousLastLine(t *testing.T) {
	t.Parallel()

	document := parser.Parse([]byte("# header"))
	comment := "associated"
	changed, err := editor.Add(document, editor.NewEntry{
		Source:     "UUID=a",
		MountPoint: "/mnt/a",
		FSType:     "ext4",
		Options:    "defaults",
		Comment:    &comment,
	})
	if err != nil || !changed {
		t.Fatalf("add: changed=%v err=%v", changed, err)
	}
	want := "# header\n# associated\nUUID=a\t/mnt/a\text4\tdefaults\t0\t0\n"
	if got := string(document.Serialize()); got != want {
		t.Fatalf("add comment diff:\ngot  %q\nwant %q", got, want)
	}
}

// TestEditAssociatedComment проверяет conservative замену непрерывного блока,
// сохранение endings по индексу и вставку нового блока без затрагивания заголовка.
func TestEditAssociatedComment(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		input       string
		comment     string
		want        string
		wantChanged bool
	}{
		{
			// Два существующих ending должны быть переиспользованы по индексу при замене текста:
			// case обнаруживает нормализацию CRLF/LF и замену более широкого диапазона документа.
			name:        "замена multiline блока сохраняет endings",
			input:       "# header\n\n# old one\r\n# old two\nUUID=a /mnt/a ext4 defaults 0 0\n",
			comment:     "new one\nnew two",
			want:        "# header\n\n# new one\r\n# new two\nUUID=a /mnt/a ext4 defaults 0 0\n",
			wantChanged: true,
		},
		{
			// Начальный comment block считается заголовком и не перезаписывается:
			// case обнаруживает потерю заголовка вместо вставки нового associated block перед записью.
			name:        "начальный заголовок не перезаписывается",
			input:       "# header\nUUID=a /mnt/a ext4 defaults 0 0\n",
			comment:     "associated",
			want:        "# header\n# associated\nUUID=a /mnt/a ext4 defaults 0 0\n",
			wantChanged: true,
		},
		{
			// Blank line разрывает связь, поэтому standalone comment перед ней остаётся нетронутым:
			// case обнаруживает сканирование через blank и удаление самостоятельного пояснения.
			name:        "blank разрывает связь со standalone comment",
			input:       "# standalone\n\nUUID=a /mnt/a ext4 defaults 0 0\n",
			comment:     "associated",
			want:        "# standalone\n\n# associated\nUUID=a /mnt/a ext4 defaults 0 0\n",
			wantChanged: true,
		},
		{
			// Совпадающий текст и переиспользованные endings образуют настоящий no-op:
			// case обнаруживает ложный changed и нормализацию mixed endings при пересоздании узлов.
			name:        "одинаковый multiline text является no-op",
			input:       "# header\n\n# same\r\n# text\nUUID=a /mnt/a ext4 defaults 0 0\n",
			comment:     "same\ntext",
			want:        "# header\n\n# same\r\n# text\nUUID=a /mnt/a ext4 defaults 0 0\n",
			wantChanged: false,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte(test.input))
			before := document.Serialize()
			base := document.BaseSHA256
			changed, err := editor.Edit(document, editor.ByLine(len(document.Nodes)), editor.Changes{Comment: &test.comment})
			if err != nil || changed != test.wantChanged {
				t.Fatalf("edit: changed=%v err=%v, want changed=%v", changed, err, test.wantChanged)
			}
			if got := string(document.Serialize()); got != test.want {
				t.Fatalf("edit comment diff:\ngot  %q\nwant %q", got, test.want)
			}
			if !test.wantChanged {
				assertUnchanged(t, document, before, base)
			} else if document.BaseSHA256 != base {
				t.Fatal("edit не должен пересчитывать base hash")
			}
		})
	}
}

// TestEditClearAssociatedComment проверяет удаление только связанного блока
// и no-op для заголовка, standalone comment после blank и записи без комментария.
func TestEditClearAssociatedComment(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		input       string
		line        int
		want        string
		wantChanged bool
	}{
		{
			name:        "связанный блок удаляется",
			input:       "# header\n\n# associated one\r\n# associated two\nUUID=a /mnt/a ext4 defaults 0 0\n",
			line:        5,
			want:        "# header\n\nUUID=a /mnt/a ext4 defaults 0 0\n",
			wantChanged: true,
		},
		{
			name:        "начальный заголовок сохраняется",
			input:       "# header one\n# header two\nUUID=a /mnt/a ext4 defaults 0 0\n",
			line:        3,
			want:        "# header one\n# header two\nUUID=a /mnt/a ext4 defaults 0 0\n",
			wantChanged: false,
		},
		{
			name:        "standalone после blank сохраняется",
			input:       "# standalone\n\nUUID=a /mnt/a ext4 defaults 0 0\n",
			line:        3,
			want:        "# standalone\n\nUUID=a /mnt/a ext4 defaults 0 0\n",
			wantChanged: false,
		},
		{
			name:        "отсутствие связанного блока является no-op",
			input:       "UUID=a /mnt/a ext4 defaults 0 0\n",
			line:        1,
			want:        "UUID=a /mnt/a ext4 defaults 0 0\n",
			wantChanged: false,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte(test.input))
			before := document.Serialize()
			base := document.BaseSHA256
			changed, err := editor.Edit(document, editor.ByLine(test.line), editor.Changes{ClearComment: true})
			if err != nil || changed != test.wantChanged {
				t.Fatalf("clear: changed=%v err=%v, want changed=%v", changed, err, test.wantChanged)
			}
			if got := string(document.Serialize()); got != test.want {
				t.Fatalf("clear diff:\ngot  %q\nwant %q", got, test.want)
			}
			if !test.wantChanged {
				assertUnchanged(t, document, before, base)
			} else if document.BaseSHA256 != base {
				t.Fatal("clear не должен пересчитывать base hash")
			}
		})
	}
}

// TestEditInvalidCommentIsAtomic проверяет конфликт Comment/ClearComment и запрет
// NUL/CR до commit, даже когда одновременно передано валидное изменение поля.
func TestEditInvalidCommentIsAtomic(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		comment *string
		clear   bool
	}{
		{name: "Comment конфликтует с ClearComment", comment: stringPointer("text"), clear: true},
		{name: "NUL в comment", comment: stringPointer("bad\x00text")},
		{name: "CR в comment", comment: stringPointer("bad\rtext")},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte("# header\n\n# old\nUUID=a /mnt/a ext4 defaults 0 0\n"))
			before := document.Serialize()
			base := document.BaseSHA256
			changed, err := editor.Edit(document, editor.ByLine(4), editor.Changes{
				Options:      stringPointer("noatime"),
				Comment:      test.comment,
				ClearComment: test.clear,
			})
			if changed || !errors.Is(err, editor.ErrInvalidChange) {
				t.Fatalf("changed=%v err=%v, want ErrInvalidChange", changed, err)
			}
			assertUnchanged(t, document, before, base)
		})
	}
}

// TestAddInvalidCommentIsAtomic проверяет отклонение NUL/CR в NewEntry.Comment
// до добавления comment nodes или самой записи.
func TestAddInvalidCommentIsAtomic(t *testing.T) {
	t.Parallel()

	for _, comment := range []string{"bad\x00text", "bad\rtext"} {
		document := parser.Parse([]byte("# untouched\n"))
		before := document.Serialize()
		base := document.BaseSHA256
		changed, err := editor.Add(document, editor.NewEntry{
			Source:     "UUID=a",
			MountPoint: "/mnt/a",
			FSType:     "ext4",
			Options:    "defaults",
			Comment:    &comment,
		})
		if changed || !errors.Is(err, editor.ErrInvalidChange) {
			t.Fatalf("comment=%q: changed=%v err=%v, want ErrInvalidChange", comment, changed, err)
		}
		assertUnchanged(t, document, before, base)
	}
}

// TestDeletePreservesAssociatedComment отдельно фиксирует, что Delete удаляет
// только entry node, а непосредственно связанный comment block остаётся в документе.
func TestDeletePreservesAssociatedComment(t *testing.T) {
	t.Parallel()

	input := []byte("# header\n\n# associated one\r\n# associated two\nUUID=a /mnt/a ext4 defaults 0 0\nUUID=b /mnt/b ext4 defaults 0 0\n")
	document := parser.Parse(input)
	base := document.BaseSHA256
	changed, err := editor.Delete(document, editor.ByLine(5))
	if err != nil || !changed {
		t.Fatalf("delete: changed=%v err=%v", changed, err)
	}
	want := []byte("# header\n\n# associated one\r\n# associated two\nUUID=b /mnt/b ext4 defaults 0 0\n")
	if !bytes.Equal(document.Serialize(), want) {
		t.Fatalf("delete diff:\ngot  %q\nwant %q", document.Serialize(), want)
	}
	if document.BaseSHA256 != base {
		t.Fatal("delete не должен пересчитывать base hash")
	}
}
