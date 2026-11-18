# План реализации — селекторы и изменения fstab в памяти

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** реализовать однозначный выбор записей и операции add/edit/delete/comment/uncomment, которые меняют только разрешённые байты документа в памяти и не обращаются к файловой системе.

**Architecture:** новый пакет `internal/editor` использует публичные модели и `internal/parser`, но не входит в parser и не зависит от будущего CLI. Каждая операция выполняется над копией документа; после успешного разбора изменённых строк и проверки результата копия атомарно заменяет содержимое исходной структуры. `BaseSHA256` не пересчитывается: он продолжает идентифицировать загруженную с диска версию, а `Serialize()` формирует candidate.

**Tech Stack:** Go 1.22+, стандартная библиотека, `go test`, fuzz, race, `go vet`, `golangci-lint`; новых внешних зависимостей нет.

## Global Constraints

- Рабочая область выполнения: `/home/sergey/projects/fstab-editor/work/.worktrees/stage-1-mutations`.
- Модуль `fstab-edit`, минимальная версия Go `1.22.0`.
- Все создаваемые и изменяемые файлы сопровождаются максимально содержательными русскими комментариями согласно `docs/stage-1-tz.md`, раздел 4.3.
- Не читать и не изменять реальный `/etc/fstab`; этот план вообще не выполняет файловые операции с fstab.
- Сравнение mount point и source выполняется полностью по decoded value; substring и symlink resolution не используются.
- Физические строки нумеруются с 1, включая blank/comment/invalid nodes.
- Несколько совпадений возвращают typed ambiguity error; произвольный выбор запрещён.
- Мутации не изменяют `Document.BaseSHA256` и не выполняют запись, backup, lock или внешние команды.
- Обязательные поля новой записи разделяются табуляцией; новые dump/pass по умолчанию равны `0` и `0`.
- Для новой строки выбирается преобладающий ending; LF выигрывает при равенстве и используется при отсутствии endings.
- Delete удаляет только узел записи: даже связанный комментарий остаётся, потому что ТЗ требует не изменять остальные строки.
- Начальный непрерывный блок комментариев документа считается заголовком и никогда не редактируется и не удаляется как связанный. Пустая строка разрывает связь.
- Этот план не создаёт CLI, validator, writer, backup, config или TUI.

---

## Структура файлов

- `internal/parser/document_test.go` — дополнительные regression tests для escapes, commented entries и base hash.
- `internal/parser/field.go` — безопасное кодирование новых decoded значений в fstab raw fields.
- `internal/parser/field_test.go` — unit tests кодирования и ошибок управляющих символов.
- `internal/parser/fuzz_test.go` — spans и decode/encode fuzz-инварианты.
- `internal/editor/selector.go` — типы селекторов, поиск и структурированные ошибки выбора.
- `internal/editor/selector_test.go` — line/mount/source, ambiguity и decoded matching.
- `internal/editor/mutation.go` — общие типы изменений, копирование, пересборка node и commit.
- `internal/editor/entry.go` — add/edit/delete и минимальные field replacements.
- `internal/editor/entry_test.go` — golden-подобные проверки байтов, optional fields, no-op и atomicity.
- `internal/editor/comment.go` — comment/uncomment и консервативные связанные комментарии.
- `internal/editor/comment_test.go` — префиксы, заголовки, standalone/associated comments.
- `internal/editor/fuzz_test.go` — выбор, comment/uncomment и отсутствие частичных мутаций.
- `README.md` — актуальный статус уже созданного parser/editor и корректные команды проверки.

### Task 1: Закрыть замечания parser review

**Files:**
- Modify: `internal/parser/document_test.go`
- Modify: `internal/parser/fuzz_test.go`

**Interfaces:**
- Consumes: `parser.Parse([]byte) *models.Document`, `(*models.Document).Serialize() []byte`, поля `Field` и `Document.BaseSHA256`.
- Produces: regression tests; публичный API не меняется.

- [ ] **Step 1: Добавить прямые tests для base hash, escapes и conservative classification**

Добавить в `internal/parser/document_test.go` следующие самостоятельные тесты, сохранив существующие:

```go
// TestBaseSHA256 фиксирует хеш именно исходных байтов: последующие мутации
// обязаны оставить эту версию для обнаружения внешнего изменения файла.
func TestBaseSHA256(t *testing.T) {
	t.Parallel()

	input := []byte("UUID=abc / ext4 defaults 0 1\r\n")
	document := parser.Parse(input)
	want := sha256.Sum256(input)
	if document.BaseSHA256 != want {
		t.Fatalf("base hash: got %x, want %x", document.BaseSHA256, want)
	}
}

// TestAdditionalEscapes проверяет обязательные libmount escapes, общий
// трёхзначный octal и буквальное сохранение unknown/incomplete backslash.
func TestAdditionalEscapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		raw   string
		value string
	}{
		{name: "табуляция", raw: `LABEL=A\011B`, value: "LABEL=A\tB"},
		{name: "backslash", raw: `LABEL=A\134B`, value: `LABEL=A\B`},
		{name: "другой octal", raw: `LABEL=A\041B`, value: "LABEL=A!B"},
		{name: "unknown escape", raw: `LABEL=A\qB`, value: `LABEL=A\qB`},
		{name: "incomplete escape", raw: `LABEL=A\04`, value: `LABEL=A\04`},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := parser.Parse([]byte(test.raw + " / ext4 defaults 0 0"))
			entry := document.Nodes[0].Entry
			if entry == nil || entry.Source.Value != test.value || entry.Source.Raw != test.raw {
				t.Fatalf("escape decode: entry=%#v, want raw=%q value=%q", entry, test.raw, test.value)
			}
		})
	}
}

// TestImplausibleCommentedEntries гарантирует, что обычный комментарий
// с относительным mount или нечисловыми dump/pass не станет записью.
func TestImplausibleCommentedEntries(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"# /dev/sda1 relative ext4 defaults 0 0",
		"# /dev/sda1 /mnt/data ext4 defaults no 0",
		"# /dev/sda1 /mnt/data ext4 defaults 0 no",
	} {
		document := parser.Parse([]byte(input))
		if document.Nodes[0].Kind != models.NodeComment || document.Nodes[0].Entry != nil {
			t.Errorf("строка ошибочно признана записью: %q: %#v", input, document.Nodes[0])
		}
	}
}
```

Добавить `crypto/sha256` в imports. Это characterization tests существующего поведения, поэтому они могут сразу пройти; их назначение — закрыть пропуски предыдущего review.

- [ ] **Step 2: Расширить fuzz проверку spans**

В `FuzzRoundTrip` после round-trip перебрать все `NodeEntry` и `NodeCommentedEntry`, включая optional fields:

```go
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
```

Добавить import `fstab-edit/pkg/models`.

- [ ] **Step 3: Запустить обычные и fuzz tests**

Run: `go test ./internal/parser -v`
Expected: PASS, включая новые subtests.

Run: `go test ./internal/parser -run=FuzzRoundTrip -fuzz=FuzzRoundTrip -fuzztime=3s`
Expected: PASS без panic и нарушения spans.

- [ ] **Step 4: Commit**

```bash
git add internal/parser/document_test.go internal/parser/fuzz_test.go
git commit -m "test: close parser review gaps"
```

### Task 2: Кодирование новых значений полей

**Files:**
- Create: `internal/parser/field.go`
- Create: `internal/parser/field_test.go`
- Modify: `internal/parser/fuzz_test.go`

**Interfaces:**
- Consumes: существующий `parser.Parse`.
- Produces: `var parser.ErrInvalidField`, `func parser.EncodeField(value string) (string, error)`; editor передаёт decoded пользовательское значение и получает raw fstab field.

- [ ] **Step 1: Написать failing tests**

Создать `internal/parser/field_test.go` с package comment и таблицей:

```go
package parser_test

import (
	"errors"
	"testing"

	"fstab-edit/internal/parser"
)

// TestEncodeField проверяет совместимое экранирование именно тех байтов,
// которые иначе изменили бы разбиение полей или значение backslash.
func TestEncodeField(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"LABEL=plain":     "LABEL=plain",
		"LABEL=My Disk":   `LABEL=My\040Disk`,
		"LABEL=A\tB":      `LABEL=A\011B`,
		`LABEL=A\B`:       `LABEL=A\134B`,
		"метка-диск":       "метка-диск",
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
```

Run: `go test ./internal/parser -run=TestEncodeField -v`
Expected: FAIL, потому что `EncodeField` и `ErrInvalidField` ещё не определены.

- [ ] **Step 2: Реализовать EncodeField**

Создать `internal/parser/field.go`:

```go
// Package parser содержит преобразования между decoded значениями и raw
// представлением fstab. Функции не обращаются к файлам и внешним утилитам.
package parser

import (
	"errors"
	"strings"
)

// ErrInvalidField означает, что decoded значение нельзя поместить в одно
// поле fstab без потери данных или создания дополнительной физической строки.
var ErrInvalidField = errors.New("поле содержит недопустимый управляющий символ")

// EncodeField экранирует separators и backslash трёхзначными octal escapes.
// NUL, LF и CR отклоняются: NUL запрещён ТЗ, а line endings создали бы новую
// строку. Остальные байты, включая UTF-8, сохраняются без нормализации.
func EncodeField(value string) (string, error) {
	var encoded strings.Builder
	encoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case ' ':
			encoded.WriteString(`\040`)
		case '\t':
			encoded.WriteString(`\011`)
		case '\\':
			encoded.WriteString(`\134`)
		case 0, '\n', '\r':
			return "", ErrInvalidField
		default:
			encoded.WriteByte(value[index])
		}
	}
	return encoded.String(), nil
}
```

- [ ] **Step 3: Добавить decode/encode fuzz-инвариант**

Добавить в `internal/parser/fuzz_test.go`:

```go
// FuzzEncodeField проверяет, что каждое допустимое непустое decoded значение
// после кодирования разбирается обратно в то же самое значение Source.
func FuzzEncodeField(f *testing.F) {
	f.Add("LABEL=My Disk")
	f.Add("a\\b\tc")
	f.Add("a\x00b")
	f.Fuzz(func(t *testing.T, value string) {
		encoded, err := parser.EncodeField(value)
		if err != nil {
			return
		}
		if encoded == "" {
			return
		}
		document := parser.Parse([]byte(encoded + " / ext4 defaults 0 0"))
		if len(document.Nodes) != 1 || document.Nodes[0].Entry == nil {
			t.Fatalf("encoded field не разобран: value=%q encoded=%q", value, encoded)
		}
		if got := document.Nodes[0].Entry.Source.Value; got != value {
			t.Fatalf("decode/encode: got %q, want %q", got, value)
		}
	})
}
```

- [ ] **Step 4: Проверить и закоммитить**

Run: `go test ./internal/parser -v`
Expected: PASS.

Run: `go test ./internal/parser -run=FuzzEncodeField -fuzz=FuzzEncodeField -fuzztime=3s`
Expected: PASS.

```bash
git add internal/parser/field.go internal/parser/field_test.go internal/parser/fuzz_test.go
git commit -m "feat: encode fstab field values"
```

### Task 3: Однозначные селекторы записей

**Files:**
- Create: `internal/editor/selector.go`
- Create: `internal/editor/selector_test.go`
- Create: `internal/editor/fuzz_test.go`

**Interfaces:**
- Consumes: `models.Document`, `models.NodeEntry`, `models.NodeCommentedEntry`, decoded `Entry.MountPoint.Value` и `Entry.Source.Value`.
- Produces:
  - `type SelectorKind uint8`;
  - `SelectorLine`, `SelectorMountPoint`, `SelectorSource`;
  - `type Selector struct { Kind SelectorKind; Line int; Value string }`;
  - `ByLine(line int) Selector`, `ByMountPoint(value string) Selector`, `BySource(value string) Selector`;
  - `Select(document *models.Document, selector Selector) (int, error)` — zero-based node index;
  - `ErrNilDocument`, `ErrInvalidSelector`, `ErrNotFound`, `ErrAmbiguous`;
  - `type AmbiguityError struct { Selector Selector; Lines []int }`, поддерживающий `errors.Is(err, ErrAmbiguous)`.

- [ ] **Step 1: Написать failing selector tests**

Создать тесты с документом, где blank и comments влияют на physical line, escaped mount/source сравниваются decoded, а одинаковые mount/source дают ambiguity. Основные asserts:

```go
document := parser.Parse([]byte("# header\n\nUUID=a /mnt/a\\040b ext4 defaults 0 0\n# LABEL=x /mnt/disabled ext4 defaults 0 0\nUUID=c /mnt/a\\040b xfs defaults 0 0\n"))

index, err := editor.Select(document, editor.ByLine(3))
if err != nil || index != 2 {
	t.Fatalf("line selector: index=%d err=%v", index, err)
}
index, err = editor.Select(document, editor.BySource("UUID=a"))
if err != nil || index != 2 {
	t.Fatalf("source selector: index=%d err=%v", index, err)
}
_, err = editor.Select(document, editor.ByMountPoint("/mnt/a b"))
var ambiguity *editor.AmbiguityError
if !errors.As(err, &ambiguity) || !errors.Is(err, editor.ErrAmbiguous) {
	t.Fatalf("ожидалась ambiguity, got %v", err)
}
if !reflect.DeepEqual(ambiguity.Lines, []int{3, 5}) {
	t.Fatalf("candidate lines: %v", ambiguity.Lines)
}
if _, err = editor.Select(document, editor.ByMountPoint("/mnt/a")); !errors.Is(err, editor.ErrNotFound) {
	t.Fatalf("substring не должен совпадать: %v", err)
}
if _, err = editor.Select(document, editor.ByLine(1)); !errors.Is(err, editor.ErrNotFound) {
	t.Fatalf("comment line не является записью: %v", err)
}
```

Отдельно проверить выбор закомментированной записи по `/mnt/disabled`, нулевую/отрицательную line, пустой Value, неизвестный `SelectorKind`, nil document и отсутствие source.

Run: `go test ./internal/editor -v`
Expected: FAIL, пакет и API ещё не существуют.

- [ ] **Step 2: Реализовать selector.go**

Использовать следующую реализацию, добавив русские комментарии ко всем типам, константам, функциям и веткам разрешения неоднозначности:

```go
package editor

import (
	"errors"
	"fmt"

	"fstab-edit/pkg/models"
)

var (
	ErrNilDocument     = errors.New("документ не задан")
	ErrInvalidSelector = errors.New("некорректный селектор")
	ErrNotFound        = errors.New("запись не найдена")
	ErrAmbiguous       = errors.New("селектор соответствует нескольким записям")
)

type SelectorKind uint8

const (
	SelectorLine SelectorKind = iota
	SelectorMountPoint
	SelectorSource
)

type Selector struct {
	Kind  SelectorKind
	Line  int
	Value string
}

func ByLine(line int) Selector { return Selector{Kind: SelectorLine, Line: line} }
func ByMountPoint(value string) Selector {
	return Selector{Kind: SelectorMountPoint, Value: value}
}
func BySource(value string) Selector {
	return Selector{Kind: SelectorSource, Value: value}
}

type AmbiguityError struct {
	Selector Selector
	Lines    []int
}

func (e *AmbiguityError) Error() string {
	return fmt.Sprintf("селектор соответствует строкам %v", e.Lines)
}
func (e *AmbiguityError) Unwrap() error { return ErrAmbiguous }

func Select(document *models.Document, selector Selector) (int, error) {
	if document == nil {
		return -1, ErrNilDocument
	}
	if selector.Kind == SelectorLine {
		if selector.Line < 1 {
			return -1, ErrInvalidSelector
		}
		if selector.Line > len(document.Nodes) {
			return -1, ErrNotFound
		}
		index := selector.Line - 1
		if document.Nodes[index].Entry == nil {
			return -1, ErrNotFound
		}
		return index, nil
	}
	if selector.Value == "" || (selector.Kind != SelectorMountPoint && selector.Kind != SelectorSource) {
		return -1, ErrInvalidSelector
	}

	match := -1
	var lines []int
	for index, node := range document.Nodes {
		if node.Kind != models.NodeEntry && node.Kind != models.NodeCommentedEntry {
			continue
		}
		value := node.Entry.Source.Value
		if selector.Kind == SelectorMountPoint {
			value = node.Entry.MountPoint.Value
		}
		if value != selector.Value {
			continue
		}
		match = index
		lines = append(lines, index+1)
	}
	if len(lines) == 0 {
		return -1, ErrNotFound
	}
	if len(lines) > 1 {
		return -1, &AmbiguityError{Selector: selector, Lines: lines}
	}
	return match, nil
}
```

- [ ] **Step 3: Добавить fuzz выбора по decoded selectors**

В `internal/editor/fuzz_test.go` добавить seed с escaped mount/source. Fuzz function вызывает оба селектора на произвольных `input []byte` и `value string`; допускает `ErrNotFound`, `ErrAmbiguous`, `ErrInvalidSelector`, но при успехе проверяет диапазон index и полное равенство соответствующего decoded value. Nil и panic недопустимы.

- [ ] **Step 4: Проверить и закоммитить**

Run: `go test ./internal/editor -v`
Expected: PASS.

Run: `go test ./internal/editor -run=FuzzSelect -fuzz=FuzzSelect -fuzztime=3s`
Expected: PASS.

```bash
git add internal/editor/selector.go internal/editor/selector_test.go internal/editor/fuzz_test.go
git commit -m "feat: resolve fstab entry selectors"
```

### Task 4: Add/edit/delete с минимальным diff

**Files:**
- Create: `internal/editor/mutation.go`
- Create: `internal/editor/entry.go`
- Create: `internal/editor/entry_test.go`

**Interfaces:**
- Consumes: `Select`, `parser.EncodeField`, `parser.Parse`, модели документа.
- Produces:
  - `ErrWrongState`, `ErrInvalidChange`, `ErrEmptyField`;
  - `type NewEntry struct { Source, MountPoint, FSType, Options, Dump, Pass string }`;
  - `type Changes struct { Source, MountPoint, FSType, Options, Dump, Pass *string; UnsetDump, UnsetPass bool }`;
  - `Add(document *models.Document, input NewEntry) (bool, error)`;
  - `Edit(document *models.Document, selector Selector, changes Changes) (bool, error)`;
  - `Delete(document *models.Document, selector Selector) (bool, error)`.
- Bool означает фактическое изменение сериализованных байтов; false с nil error — no-op.

- [ ] **Step 1: Написать failing tests минимального diff и no-op**

Создать table-driven tests с русскими комментариями к каждому сценарию:

1. `Edit` меняет только Options в строке с неодинаковыми separators: `  UUID=a\t/mnt/a  ext4\tdefaults 0 1\n` → меняется только raw `defaults`; hash остаётся исходным.
2. Передача decoded значения, равного текущему, возвращает `(false, nil)` и побайтово сохраняет документ, включая нестандартный raw escape.
3. Добавление Dump к четырёхполевой записи добавляет только `\t0`; Pass без Dump отклоняется. Добавление Dump и Pass в одной операции допустимо.
4. `UnsetPass` удаляет separator и только Pass. `UnsetDump` при оставшемся Pass отклоняется; совместные `UnsetDump` и `UnsetPass` удаляют оба suffix-поля.
5. Конфликт `Dump` + `UnsetDump`, `Pass` + `UnsetPass`, пустое обязательное значение, NUL/newline и пустой `Changes{}` возвращают typed error и не меняют документ.
6. `Delete` удаляет ровно выбранную строку, сохраняет comments/blank lines, их endings и пересчитывает `Entry.Line`.
7. Nil document возвращает `ErrNilDocument`; ошибка selector не вызывает mutation.

Пример центрального assert:

```go
input := []byte("  UUID=a\t/mnt/a  ext4\tdefaults 0 1\n# untouched\r\n")
document := parser.Parse(input)
base := document.BaseSHA256
options := "defaults,noatime"
changed, err := editor.Edit(document, editor.ByLine(1), editor.Changes{Options: &options})
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
```

Run: `go test ./internal/editor -run='Test(Add|Edit|Delete)' -v`
Expected: FAIL, API ещё не реализован.

- [ ] **Step 2: Создать общий mutation.go**

Реализовать копирование и commit по следующему коду, снабдив его полными русскими комментариями:

```go
package editor

import (
	"bytes"
	"errors"
	"fmt"

	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

var (
	ErrWrongState    = errors.New("операция не соответствует состоянию записи")
	ErrInvalidChange = errors.New("некорректный набор изменений")
	ErrEmptyField    = errors.New("значение поля не может быть пустым")
)

func cloneDocument(document *models.Document) *models.Document {
	clone := *document
	clone.Nodes = append([]models.Node(nil), document.Nodes...)
	return &clone
}

func commitDocument(document, candidate *models.Document) bool {
	if bytes.Equal(document.Serialize(), candidate.Serialize()) {
		return false
	}
	for index := range candidate.Nodes {
		if candidate.Nodes[index].Entry != nil {
			candidate.Nodes[index].Entry.Line = index + 1
		}
	}
	*document = *candidate
	return true
}

func rebuildNode(raw, ending string, line int, want models.NodeKind) (models.Node, error) {
	parsed := parser.Parse([]byte(raw))
	if len(parsed.Nodes) != 1 || parsed.Nodes[0].Kind != want || parsed.Nodes[0].Entry == nil {
		return models.Node{}, fmt.Errorf("%w: изменённая строка не распознана", ErrWrongState)
	}
	node := parsed.Nodes[0]
	node.Ending = ending
	node.Entry.Line = line
	return node, nil
}

func dominantEnding(document *models.Document) string {
	lf, crlf := 0, 0
	for _, node := range document.Nodes {
		switch node.Ending {
		case "\n":
			lf++
		case "\r\n":
			crlf++
		}
	}
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}
```

Shallow copy nodes здесь безопасен только потому, что изменённые nodes заменяются целиком через `rebuildNode`, а существующие `Entry` не мутируются. Не изменять `Entry` через указатель до `commitDocument`.

- [ ] **Step 3: Реализовать field replacements и Add/Edit/Delete**

В `entry.go` определить публичные структуры точно по Interfaces. Для Edit использовать внутреннюю замену:

```go
type replacement struct {
	start int
	end   int
	raw   string
}

func replaceRanges(raw string, replacements []replacement) string {
	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start > replacements[j].start
	})
	for _, item := range replacements {
		raw = raw[:item.start] + item.raw + raw[item.end:]
	}
	return raw
}
```

Реализация обязана соблюдать следующий исполняемый алгоритм:

1. Вызвать `Select` до копирования; проверить node и `Entry != nil`.
2. Проверить конфликты set/unset и наличие хотя бы одного flag. Для каждого non-nil field value пустая строка даёт `ErrEmptyField`.
3. Если decoded new value равно `Field.Value`, replacement не нужен. Иначе вызвать `parser.EncodeField` и использовать исходные `Start/End`.
4. Сначала вычислить итоговые присутствия Dump/Pass. Итоговый Pass без Dump запрещён `ErrInvalidChange`; никакие неявные defaults в существующую запись не добавляются.
5. Удаляемое optional-поле удаляется вместе с непрерывным separator непосредственно перед ним. При удалении Dump и Pass удаляется один общий suffix; trailing whitespace после поля сохраняется.
6. Отсутствующее Dump/Pass добавляется через `\t` после применения replacements. Одновременно переданные Dump и Pass добавляются в порядке Dump, Pass.
7. Все replacements сортируются по убыванию offsets, поэтому исходные spans остаются корректными. Изменённая строка пересобирается через `rebuildNode` с исходным `NodeKind`; ошибка не коммитится.
8. `Add` кодирует все шесть значений, подставляя пустые Dump/Pass как `"0"`; четыре обязательных пустых значения отклоняются. Поля соединяются `\t`.
9. `Add` выбирает `dominantEnding`; если последний существующий node не имеет ending, ему назначается выбранный ending. Новый node также получает выбранный ending.
10. `Delete` удаляет только `candidate.Nodes[index]`; comments не удаляются.

Ключевые функции оформить следующим образом, без файловых операций:

```go
func Add(document *models.Document, input NewEntry) (bool, error) {
	if document == nil {
		return false, ErrNilDocument
	}
	if input.Dump == "" {
		input.Dump = "0"
	}
	if input.Pass == "" {
		input.Pass = "0"
	}
	values := []string{input.Source, input.MountPoint, input.FSType, input.Options, input.Dump, input.Pass}
	encoded := make([]string, len(values))
	for index, value := range values {
		if value == "" {
			return false, ErrEmptyField
		}
		raw, err := parser.EncodeField(value)
		if err != nil {
			return false, err
		}
		encoded[index] = raw
	}
	candidate := cloneDocument(document)
	ending := dominantEnding(document)
	if len(candidate.Nodes) > 0 && candidate.Nodes[len(candidate.Nodes)-1].Ending == "" {
		candidate.Nodes[len(candidate.Nodes)-1].Ending = ending
	}
	node, err := rebuildNode(strings.Join(encoded, "\t"), ending, len(candidate.Nodes)+1, models.NodeEntry)
	if err != nil {
		return false, err
	}
	candidate.Nodes = append(candidate.Nodes, node)
	return commitDocument(document, candidate), nil
}

func Delete(document *models.Document, selector Selector) (bool, error) {
	index, err := Select(document, selector)
	if err != nil {
		return false, err
	}
	candidate := cloneDocument(document)
	candidate.Nodes = append(candidate.Nodes[:index], candidate.Nodes[index+1:]...)
	return commitDocument(document, candidate), nil
}
```

`Edit` оформить как отдельную функцию с теми же guard clauses; вынести проверку и подготовку replacements в неэкспортируемые функции, если файл становится слишком большим. Все вспомогательные функции и неочевидные диапазоны подробно комментируются.

- [ ] **Step 4: Добавить Add tests для line endings и escaping**

Проверить:

- пустой документ → одна запись с шестью полями и LF;
- CRLF преобладает → новая запись CRLF;
- равное число LF/CRLF → новая запись LF;
- существующий last node без ending получает один ending перед новой записью;
- source/mount с пробелом, табуляцией и backslash кодируются и затем parser возвращает исходные decoded values;
- `BaseSHA256` не меняется.

Run: `go test ./internal/editor -v`
Expected: PASS.

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/editor/mutation.go internal/editor/entry.go internal/editor/entry_test.go
git commit -m "feat: mutate fstab entries in memory"
```

### Task 5: Comment/uncomment, связанные комментарии и README

**Files:**
- Modify: `internal/editor/mutation.go`
- Modify: `internal/editor/entry.go`
- Create: `internal/editor/comment.go`
- Create: `internal/editor/comment_test.go`
- Modify: `internal/editor/entry_test.go`
- Modify: `internal/editor/fuzz_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: общие `cloneDocument`, `commitDocument`, `rebuildNode`, `dominantEnding`, `Edit`.
- Produces:
  - `Comment(document *models.Document, selector Selector) (bool, error)`;
  - `Uncomment(document *models.Document, selector Selector) (bool, error)`;
  - дополнительное поле `NewEntry.Comment *string`;
  - дополнительные поля `Changes.Comment *string` и `Changes.ClearComment bool`.

- [ ] **Step 1: Написать failing tests comment/uncomment**

Проверить русскими описанными subtests:

- активная строка с исходными leading spaces становится `# ` + исходный Raw; ending и остальные nodes сохраняются;
- uncomment удаляет `#` и не более одного следующего space/tab, сохраняя остальные leading spaces;
- comment требует `NodeEntry`, uncomment — `NodeCommentedEntry`; wrong state и обычный comment не изменяют документ;
- если изменённая строка не проходит conservative classification, Comment возвращает ошибку и оставляет весь документ неизменным;
- round-trip Comment → Uncomment для исходно активной корректной записи восстанавливает исходные байты.

Центральный пример:

```go
input := []byte("  UUID=a /mnt/a ext4 defaults 0 0\r\n")
document := parser.Parse(input)
changed, err := editor.Comment(document, editor.ByLine(1))
if err != nil || !changed || string(document.Serialize()) != "#   UUID=a /mnt/a ext4 defaults 0 0\r\n" {
	t.Fatalf("comment: changed=%v err=%v raw=%q", changed, err, document.Serialize())
}
changed, err = editor.Uncomment(document, editor.ByLine(1))
if err != nil || !changed || !bytes.Equal(document.Serialize(), input) {
	t.Fatalf("uncomment: changed=%v err=%v raw=%q", changed, err, document.Serialize())
}
```

Run: `go test ./internal/editor -run='Test(Comment|Uncomment)' -v`
Expected: FAIL, функции ещё не определены.

- [ ] **Step 2: Реализовать comment.go**

Для Comment создать копию, заменить Raw на `"# "+Raw` и вызвать `rebuildNode(..., models.NodeCommentedEntry)`. Для Uncomment найти первый non-space `#`, удалить его и максимум один непосредственно следующий space/tab, затем пересобрать как `NodeEntry`. Не использовать `TrimSpace`, потому что он уничтожил бы исходное форматирование.

Функции возвращают ошибку до commit при несоответствии типа или неудачном reparse. `BaseSHA256` наследуется через clone.

- [ ] **Step 3: Написать failing tests связанных комментариев**

Добавить `Comment *string` в NewEntry и `Comment *string`, `ClearComment bool` в Changes. Проверить:

1. Add с `Comment` вставляет `# <text>` непосредственно перед новой записью, включая multiline text (`\n` → отдельные `# ` lines), с выбранным ending документа.
2. Edit с Comment заменяет только непрерывный NodeComment block непосредственно перед записью; blank line разрывает связь.
3. Начальный блок документа — заголовок: ClearComment его не удаляет; Comment не перезаписывает его.
4. Standalone comment после blank не затрагивается.
5. ClearComment удаляет только associated block и возвращает false, если связанного блока нет.
6. Comment + ClearComment даёт `ErrInvalidChange`; NUL и CR в тексте отклоняются до mutation; LF разделяет строки.
7. Delete по-прежнему сохраняет associated block.
8. Одинаковый comment text — no-op.

- [ ] **Step 4: Реализовать conservative associated comments**

Использовать диапазон `[start, entryIndex)`, где start вычисляется так:

```go
func associatedCommentRange(document *models.Document, entryIndex int) (int, int) {
	start := entryIndex
	for start > 0 && document.Nodes[start-1].Kind == models.NodeComment {
		start--
	}
	if start == 0 {
		return entryIndex, entryIndex
	}
	return start, entryIndex
}
```

Пояснить в комментарии, что блок с индексом 0 неотличим от заголовка и потому намеренно сохраняется. Это консервативное ограничение распространяется и на comment первой записи: новый блок добавляется, существующий начальный не перезаписывается и не удаляется.

Создание comment nodes:

```go
func makeCommentNodes(text, ending string) ([]models.Node, error) {
	if strings.ContainsAny(text, "\x00\r") {
		return nil, ErrInvalidChange
	}
	lines := strings.Split(text, "\n")
	nodes := make([]models.Node, len(lines))
	for index, line := range lines {
		nodes[index] = models.Node{Kind: models.NodeComment, Raw: "# " + line, Ending: ending}
	}
	return nodes, nil
}
```

При замене существующего associated block переиспользовать endings по индексу, чтобы одинаковый текст давал no-op; для дополнительных lines использовать `dominantEnding`. Add вставляет comment nodes перед новым entry. Edit применяет field changes и comment changes к candidate, но коммит выполняет один раз. Расширить проверку `Changes{}` учётом Comment/ClearComment. Никакие ошибки не должны оставить частично изменённый исходный document.

- [ ] **Step 5: Добавить fuzz comment/uncomment и проверить весь пакет**

Fuzz выбирает первую активную запись, вызывает Comment; при успехе вызывает Uncomment по той же physical line и проверяет побайтовое восстановление исходного документа. При ошибке Comment проверяет, что исходные байты не изменились. Не делать предположений, что syntactically active запись обязательно пройдет conservative commented classification.

Run: `go test ./internal/editor -v`
Expected: PASS.

Run: `go test ./internal/editor -run=FuzzCommentRoundTrip -fuzz=FuzzCommentRoundTrip -fuzztime=3s`
Expected: PASS.

- [ ] **Step 6: Обновить README**

Заменить устаревшее «Go-модуль и исходный код пока не созданы» на фактический статус: реализованы model, lossless parser, selectors и in-memory mutations; CLI и исполняемый файл ещё не созданы. В структуру добавить `internal/editor`. Удалить инструкции `go mod init`, `go get` и сборку несуществующего корневого main; оставить проверки:

```bash
go test ./... -v
go test ./... -cover
go test -race ./...
go vet ./...
golangci-lint run
```

Явно указать, что операции пока работают только с документом в памяти и не изменяют `/etc/fstab`.

- [ ] **Step 7: Полная проверка и commit**

Run: `gofmt -w internal/parser internal/editor pkg/models && go test ./... && go vet ./... && go test -race ./... && golangci-lint run`
Expected: все команды PASS, lint без findings.

Run: `go test ./... -cover`
Expected: PASS; записать фактическое покрытие новых пакетов для итогового отчёта, не объявляя завершённым общий stage-1 target на основании одного блока.

```bash
git add internal/editor README.md
git commit -m "feat: preserve entry comments during mutations"
```

## Финальная приёмка плана

1. `git diff main...HEAD --stat` содержит только parser tests/encoding, editor и README.
2. `go test ./...`, `go vet ./...`, `go test -race ./...`, `golangci-lint run` проходят на итоговом HEAD.
3. Отдельно запускаются четыре short fuzz target: `FuzzRoundTrip`, `FuzzEncodeField`, `FuzzSelect`, `FuzzCommentRoundTrip`.
4. Review подтверждает: BaseSHA256 неизменен, ошибки не коммитят частичные изменения, Delete не удаляет comments, заголовок не редактируется, optional Pass никогда не остаётся без Dump.
5. После merge в `main` повторно выполнить `go test ./...` и проверить чистый `git status`.
