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
