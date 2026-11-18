# План реализации — побайтово сохраняющий parser fstab

> **Для agentic workers:** обязательный субнавык: используйте `superpowers:subagent-driven-development` (рекомендуется) или `superpowers:executing-plans` для выполнения плана задача за задачей. Шаги отмечаются через `- [ ]`.

**Цель:** создать Go-модуль и parser, который разбирает активные и закомментированные записи fstab, сохраняет исходные байты, позиции полей и escape-декодирование без каких-либо изменений системного `/etc/fstab`.

**Архитектура:** публичные модели находятся в `pkg/models`, а алгоритмы разбора — в `internal/parser`. Документ хранит каждый исходный content line и line ending отдельно; serializer просто собирает сохранённые байты, поэтому no-op round-trip не зависит от успешности разбора отдельной записи.

**Технологии:** Go 1.22+, стандартная библиотека, `go test`, fuzz-тестирование; внешние зависимости в этом плане не нужны.

## Global Constraints

- Рабочая область: `/home/sergey/projects/fstab-editor/work/.worktrees/stage-1`.
- Модуль: `fstab-edit`; минимальная декларируемая версия Go: `1.22.0`.
- Все комментарии в создаваемых файлах — максимально содержательные и на русском языке.
- Идентификаторы и стабильные machine-readable значения остаются английскими.
- Parser не обращается к `/etc/fstab`, внешним командам, root или системной конфигурации.
- `Parse` сохраняет LF, CRLF, смешанные endings и отсутствие final newline.
- Некорректная активная строка становится `NodeInvalid`, а не удаляется и не превращается в комментарий.
- Закомментированная запись распознаётся консервативно: кроме 4–6 полей требуется правдоподобный mount point и числовые optional dump/pass.
- `Serialize` без мутаций обязан возвращать исходные байты для любых входных данных, включая invalid UTF-8.
- Позиции `Field.Start` и `Field.End` — byte offsets относительно `Node.Raw`, с `End` exclusive.
- Этот план не добавляет CLI, запись файлов, валидацию опций, backup или TUI.

---

## Структура файлов

- `go.mod` — объявляет модуль и минимальную версию Go; внешних зависимостей пока нет.
- `pkg/models/document.go` — описывает документ, узлы, записи, поля и serializer.
- `internal/parser/document.go` — разбивает вход на строки, классифицирует узлы, разбирает поля и escapes.
- `internal/parser/document_test.go` — проверяет round-trip, классификацию, spans, optional-поля и ошибки.
- `internal/parser/fuzz_test.go` — проверяет отсутствие panic и побайтовый round-trip на произвольных байтах.

### Task 1: Go-модуль и публичная модель документа

**Files:**
- Create: `go.mod`
- Create: `pkg/models/document.go`
- Test: `internal/parser/document_test.go`

**Interfaces:**
- Consumes: отсутствуют; это первый компонент.
- Produces: `models.Document`, `models.Node`, `models.Entry`, `models.Field`, `models.NodeKind`, константы `NodeBlank`, `NodeComment`, `NodeEntry`, `NodeCommentedEntry`, `NodeInvalid`, метод `(*Document).Serialize() []byte`.

- [ ] **Step 1: Создать Go-модуль**

Создать `go.mod`:

```go
// Модуль локальной разработки fstab-edit.
// Внешние зависимости появятся в задачах CLI и конфигурации.
module fstab-edit

// Минимальная версия языка зафиксирована техническим заданием.
go 1.22.0
```

Run: `go mod edit -fmt && cat go.mod`
Expected: файл содержит `module fstab-edit` и `go 1.22.0`.

- [ ] **Step 2: Создать публичные модели**

Создать `pkg/models/document.go`:

```go
// Package models содержит типы данных, которыми обмениваются parser,
// валидатор, транзакционный writer и пользовательские интерфейсы.
package models

// NodeKind определяет структурную роль одной физической строки документа.
// Значения являются машиночитаемыми и не зависят от выбранной локали.
type NodeKind uint8

const (
	// NodeBlank обозначает строку, содержащую только пробелы или табуляции.
	NodeBlank NodeKind = iota
	// NodeComment обозначает обычный комментарий, не являющийся записью fstab.
	NodeComment
	// NodeEntry обозначает активную запись с четырьмя—шестью полями.
	NodeEntry
	// NodeCommentedEntry обозначает отключённую запись с префиксом '#'.
	NodeCommentedEntry
	// NodeInvalid обозначает активную строку, которую нельзя разобрать как запись.
	NodeInvalid
)

// Field хранит одновременно исходное и декодированное представление поля.
// Start и End указывают byte offsets в Node.Raw и позволяют позднее заменить
// только это поле, не переформатируя остальную строку.
type Field struct {
	Raw    string
	Value  string
	Start  int
	End    int
}

// Entry описывает активную или закомментированную запись fstab.
// Dump и Pass являются указателями, поскольку эти optional-поля могут
// отсутствовать; nil не подменяется значением по умолчанию при разборе.
type Entry struct {
	Line       int
	Active     bool
	Source     Field
	MountPoint Field
	FSType     Field
	Options    Field
	Dump       *Field
	Pass       *Field
}

// Node сохраняет исходное содержимое физической строки без line ending.
// Raw и Ending вместе обеспечивают побайтовый round-trip, а Entry заполнено
// только для NodeEntry и NodeCommentedEntry.
type Node struct {
	Kind       NodeKind
	Raw        string
	Ending     string
	Entry      *Entry
	ParseError string
}

// Document представляет fstab как упорядоченный набор физических строк.
// BaseSHA256 фиксирует исходную версию и позднее защитит от перезаписи
// изменений, сделанных другой программой после загрузки документа.
type Document struct {
	Nodes      []Node
	BaseSHA256 [32]byte
}

// Serialize собирает сохранённые content lines и их исходные endings.
// Метод не нормализует данные и поэтому безопасен даже для некорректных строк.
// Возвращается новый slice, чтобы вызывающий код не изменял внутренние строки.
func (d *Document) Serialize() []byte {
	if d == nil {
		return nil
	}

	size := 0
	for _, node := range d.Nodes {
		size += len(node.Raw) + len(node.Ending)
	}

	result := make([]byte, 0, size)
	for _, node := range d.Nodes {
		result = append(result, node.Raw...)
		result = append(result, node.Ending...)
	}
	return result
}
```

Run: `gofmt -w pkg/models/document.go && go test ./pkg/models`
Expected: PASS с сообщением `[no test files]`; код компилируется.

- [ ] **Step 3: Commit модели**

```bash
git add go.mod pkg/models/document.go
git commit -m "feat: add fstab document model"
```

Expected: создаётся коммит с моделью; `git status --short` пуст.

### Task 2: Parser и побайтовый round-trip

**Files:**
- Create: `internal/parser/document_test.go`
- Create: `internal/parser/document.go`
- Test: `internal/parser/fuzz_test.go`

**Interfaces:**
- Consumes: модели из Task 1.
- Produces: `parser.Parse(data []byte) *models.Document`; для последующих задач каждая запись содержит physical line, raw/decoded fields и spans.

- [ ] **Step 1: Написать failing tests**

Создать `internal/parser/document_test.go`:

```go
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
		"пустой документ":             {},
		"обычная запись":              []byte("UUID=abc / ext4 defaults 0 1\n"),
		"без завершающего newline":     []byte("/dev/sda1 / ext4 defaults 0 1"),
		"CRLF":                        []byte("/dev/sda1 / ext4 defaults 0 1\r\n"),
		"смешанные endings":            []byte("# comment\r\n\n/dev/sda1 / ext4 defaults 0 1"),
		"пробелы и обычный комментарий": []byte(" \t \n# обычный комментарий\n"),
		"некорректная строка":          []byte("not an fstab entry\n"),
		"invalid UTF-8":               []byte{0xff, 0xfe, '\n'},
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
		"source": entry.Source,
		"mount":  entry.MountPoint,
		"type":   entry.FSType,
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
```

Создать `internal/parser/fuzz_test.go`:

```go
// Package parser_test содержит fuzz-проверку устойчивости parser.
package parser_test

import (
	"bytes"
	"testing"

	"fstab-edit/internal/parser"
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
	})
}
```

- [ ] **Step 2: Запустить tests и подтвердить RED**

Run: `go test ./internal/parser -v`
Expected: FAIL до создания `internal/parser/document.go`, потому что отсутствует `parser.Parse`.

- [ ] **Step 3: Реализовать parser**

Создать `internal/parser/document.go`:

```go
// Package parser разбирает fstab без обращения к системным файлам.
// Все решения принимаются только по переданным байтам; исходный raw text
// сохраняется независимо от того, удалось ли распознать отдельную запись.
package parser

import (
	"crypto/sha256"
	"strconv"
	"strings"

	"fstab-edit/pkg/models"
)

// fieldSpan описывает найденное поле до создания публичной модели.
// Offset суммируется с позицией поля, когда разбирается текст после '#'.
type fieldSpan struct {
	start int
	end   int
}

// Parse создаёт документ и вычисляет SHA-256 исходной версии.
// Функция не возвращает глобальную ошибку: даже повреждённые строки должны
// остаться доступными пользователю для просмотра и последующего исправления.
func Parse(data []byte) *models.Document {
	document := &models.Document{BaseSHA256: sha256.Sum256(data)}

	// Пустой вход не создаёт искусственную пустую строку. Это позволяет
	// Serialize вернуть пустой slice вместо одного лишнего newline.
	for offset, lineNumber := 0, 1; offset < len(data); lineNumber++ {
		content, ending, consumed := takeLine(data[offset:])
		node := parseLine(string(content), lineNumber)
		node.Ending = ending
		document.Nodes = append(document.Nodes, node)
		offset += consumed
	}

	return document
}

// takeLine отделяет содержимое одной строки от её исходного ending.
// Поддерживаются LF и CRLF; одиночный CR остаётся частью содержимого.
// consumed показывает, сколько исходных байтов заняла строка вместе с ending.
func takeLine(data []byte) (content []byte, ending string, consumed int) {
	for index, value := range data {
		if value != '\n' {
			continue
		}

		contentEnd := index
		ending = "\n"
		if index > 0 && data[index-1] == '\r' {
			contentEnd--
			ending = "\r\n"
		}
		return data[:contentEnd], ending, index + 1
	}

	// Последняя строка может не иметь newline; это не повод добавлять его.
	return data, "", len(data)
}

// parseLine классифицирует content line, не меняя его исходный текст.
// LineNumber передаётся в запись для стабильного физического селектора CLI.
func parseLine(raw string, lineNumber int) models.Node {
	node := models.Node{Kind: models.NodeBlank, Raw: raw}
	if isBlank(raw) {
		return node
	}

	first := firstNonSpace(raw)
	if first >= 0 && raw[first] == '#' {
		return parseComment(raw, first, lineNumber)
	}

	entry, err := parseEntry(raw, 0, lineNumber, true)
	if err != nil {
		node.Kind = models.NodeInvalid
		node.ParseError = err.Error()
		return node
	}

	node.Kind = models.NodeEntry
	node.Entry = entry
	return node
}

// parseComment отличает обычный комментарий от закомментированной записи.
// После '#' допускаются исходные отступы; offsets вычисляются в полной строке.
func parseComment(raw string, hash int, lineNumber int) models.Node {
	node := models.Node{Kind: models.NodeComment, Raw: raw}

	candidateStart := hash + 1
	for candidateStart < len(raw) && isSpace(raw[candidateStart]) {
		candidateStart++
	}
	if candidateStart == len(raw) {
		return node
	}

	entry, err := parseEntry(raw[candidateStart:], candidateStart, lineNumber, false)
	if err != nil || !plausibleCommentedEntry(entry) {
		return node
	}

	node.Kind = models.NodeCommentedEntry
	node.Entry = entry
	return node
}

// parseEntry разбирает четыре обязательных и два optional-поля.
// offset прибавляется к spans, чтобы они всегда ссылались на полный Node.Raw.
func parseEntry(raw string, offset, lineNumber int, active bool) (*models.Entry, error) {
	spans := splitFields(raw)
	if len(spans) < 4 || len(spans) > 6 {
		return nil, strconv.ErrSyntax
	}

	fields := make([]models.Field, len(spans))
	for index, span := range spans {
		rawValue := raw[span.start:span.end]
		fields[index] = models.Field{
			Raw:   rawValue,
			Value: decodeField(rawValue),
			Start: offset + span.start,
			End:   offset + span.end,
		}
	}

	entry := &models.Entry{
		Line:       lineNumber,
		Active:     active,
		Source:     fields[0],
		MountPoint: fields[1],
		FSType:     fields[2],
		Options:    fields[3],
	}
	if len(fields) >= 5 {
		dump := fields[4]
		entry.Dump = &dump
	}
	if len(fields) == 6 {
		pass := fields[5]
		entry.Pass = &pass
	}
	return entry, nil
}

// splitFields возвращает byte ranges, разделённые пробелами и табуляциями.
// Сами separators не входят в поле и остаются доступными через исходный Raw.
func splitFields(raw string) []fieldSpan {
	var fields []fieldSpan
	for index := 0; index < len(raw); {
		for index < len(raw) && isSpace(raw[index]) {
			index++
		}
		if index == len(raw) {
			break
		}

		start := index
		for index < len(raw) && !isSpace(raw[index]) {
			index++
		}
		fields = append(fields, fieldSpan{start: start, end: index})
	}
	return fields
}

// decodeField раскрывает трёхзначные octal escapes, принимаемые libmount.
// Неизвестный или неполный backslash сохраняется буквально: parser не должен
// молча потерять исходное значение даже при ошибке последующей валидации.
func decodeField(raw string) string {
	if !strings.Contains(raw, "\\") {
		return raw
	}

	var decoded strings.Builder
	decoded.Grow(len(raw))
	for index := 0; index < len(raw); {
		if raw[index] != '\\' || index+3 >= len(raw) {
			decoded.WriteByte(raw[index])
			index++
			continue
		}

		value, err := strconv.ParseUint(raw[index+1:index+4], 8, 8)
		if err != nil {
			decoded.WriteByte(raw[index])
			index++
			continue
		}
		decoded.WriteByte(byte(value))
		index += 4
	}
	return decoded.String()
}

// plausibleCommentedEntry уменьшает риск превращения обычной фразы в запись.
// Полная семантическая проверка относится к validator и здесь не выполняется.
func plausibleCommentedEntry(entry *models.Entry) bool {
	mount := entry.MountPoint.Value
	if mount != "none" && !strings.HasPrefix(mount, "/") {
		return false
	}
	return validOptionalNumber(entry.Dump) && validOptionalNumber(entry.Pass)
}

// validOptionalNumber проверяет optional dump/pass только для классификации
// закомментированной записи. Диапазоны и рекомендации проверит validator.
func validOptionalNumber(field *models.Field) bool {
	if field == nil {
		return true
	}
	if field.Value == "" {
		return false
	}
	for _, digit := range field.Value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// isBlank определяет строку без видимых символов. Другие управляющие байты
// не считаются whitespace, чтобы они не исчезли из диагностируемого содержимого.
func isBlank(raw string) bool {
	for index := 0; index < len(raw); index++ {
		if !isSpace(raw[index]) {
			return false
		}
	}
	return true
}

// firstNonSpace возвращает первый значимый byte или -1 для blank-строки.
func firstNonSpace(raw string) int {
	for index := 0; index < len(raw); index++ {
		if !isSpace(raw[index]) {
			return index
		}
	}
	return -1
}

// isSpace выделяет ровно separators fstab, а не все Unicode whitespace.
func isSpace(value byte) bool {
	return value == ' ' || value == '\t'
}
```

Run: `gofmt -w internal/parser/document.go internal/parser/document_test.go internal/parser/fuzz_test.go`

- [ ] **Step 4: Запустить unit tests и подтвердить GREEN**

Run: `go test ./... -v`
Expected: все tests PASS; `TestRoundTrip`, `TestNodeKinds`, `TestEntryFields`, `TestOptionalFields`, `TestCommentedEntrySpans` выполняются успешно.

- [ ] **Step 5: Запустить fuzz smoke test**

Run: `go test ./internal/parser -run=FuzzRoundTrip -fuzz=FuzzRoundTrip -fuzztime=3s`
Expected: fuzz выполняется не менее трёх секунд и завершается PASS без crash/panic.

Если появляется `testdata/fuzz` с новым crashing input, не удалять его: это минимизированный regression fixture. Остановиться и исправить parser до повторного запуска.

- [ ] **Step 6: Запустить общий quality gate**

Run:

```bash
go vet ./...
go test -race ./...
golangci-lint run
```

Expected: все команды завершаются кодом 0. Для отсутствующего конфигурационного файла используются default rules установленного `golangci-lint`; если линтер требует project config, конфигурация создаётся отдельной задачей и не подменяет результат tests/vet.

- [ ] **Step 7: Commit parser**

```bash
git add internal/parser
git commit -m "feat: add lossless fstab parser"
git status --short
```

Expected: создаётся коммит; `git status --short` не выводит неучтённые исходные файлы. Crash fixtures, если появились, должны быть добавлены в тот же коммит после исправления.

---

## Проверка покрытия спецификации

Этот план закрывает из раздела 5 первого этапа:

- пустые, обычные comment, active, commented и invalid lines;
- 4–6 полей и optional dump/pass без подстановки defaults;
- raw/decoded values и byte spans;
- `\040`, `\011`, `\134` и общий octal decode;
- LF, CRLF, mixed endings и no final newline;
- no-op byte equality и base SHA-256;
- консервативное распознавание commented entries;
- golden-подобные table cases, round-trip и fuzz.

Следующие планы должны отдельно закрыть serializer mutations, associated comments, validator, sysutil, writer, backup, CLI и остальные требования первого этапа; этот план не объявляет их реализованными.
