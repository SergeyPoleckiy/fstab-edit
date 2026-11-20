// Package diff формирует line-based unified diff для CLI preview мутаций.
// Он не обращается к файлам и не изменяет переданные byte slices: входные
// данные копируются в строки ещё до построения LCS-таблицы.
package diff

import (
	"fmt"
	"strings"
)

// contextLines задаёт, сколько неизменённых строк окружает каждый hunk.
const contextLines = 3

// sourceLine хранит текст одной строки без line ending и признак завершающего
// перевода строки у исходной последовательности байт.
type sourceLine struct {
	text    string
	newline bool
}

// splitLines разбивает bytes на строки, сохраняя признак final newline.
// Пустой вход даёт пустой slice; завершающий newline не порождает лишнюю строку.
func splitLines(data []byte) []sourceLine {
	text := string(data)
	if text == "" {
		return nil
	}
	var lines []sourceLine
	for len(text) > 0 {
		index := strings.IndexByte(text, '\n')
		if index < 0 {
			lines = append(lines, sourceLine{text: text, newline: false})
			break
		}
		lines = append(lines, sourceLine{text: text[:index], newline: true})
		text = text[index+1:]
	}
	return lines
}

// opKind классифицирует одну строку в последовательности различий.
type opKind uint8

const (
	opKeep opKind = iota
	opDelete
	opAdd
)

// op связывает вид строки с её содержимым и признаком newline.
type op struct {
	kind   opKind
	line   sourceLine
	origNo int // zero-based номер строки в original; для opAdd равен -1
	candNo int // zero-based номер строки в candidate; для opDelete равен -1
}

// lcsOps вычисляет LCS по текстам строк и возвращает последовательность
// keep/delete/add операций, приводящую original к candidate.
func lcsOps(original, candidate []sourceLine) []op {
	rows, cols := len(original), len(candidate)
	// table[i][j] — длина LCS для original[i:] и candidate[j:].
	table := make([][]int, rows+1)
	for i := range table {
		table[i] = make([]int, cols+1)
	}
	for i := rows - 1; i >= 0; i-- {
		for j := cols - 1; j >= 0; j-- {
			if original[i].text == candidate[j].text {
				table[i][j] = table[i+1][j+1] + 1
			} else if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < rows && j < cols {
		switch {
		case original[i].text == candidate[j].text:
			ops = append(ops, op{kind: opKeep, line: original[i], origNo: i, candNo: j})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, op{kind: opDelete, line: original[i], origNo: i})
			i++
		default:
			ops = append(ops, op{kind: opAdd, line: candidate[j], candNo: j})
			j++
		}
	}
	for ; i < rows; i++ {
		ops = append(ops, op{kind: opDelete, line: original[i], origNo: i})
	}
	for ; j < cols; j++ {
		ops = append(ops, op{kind: opAdd, line: candidate[j], candNo: j})
	}
	return ops
}

// hunkRange вычисляет заголовок одной стороны hunk: start — one-based номер
// первой строки этой стороны (при count == 0 — номер предыдущей строки),
// count — количество строк этой стороны в hunk.
func hunkRange(numbers []int, count int) (start int) {
	if count == 0 {
		// Пустая сторона адресует предыдущую строку, за которой произошла
		// правка; у пустого файла такой строки нет и start равен нулю.
		if len(numbers) == 0 {
			return 0
		}
		return numbers[0]
	}
	return numbers[0] + 1
}

// appendLine пишет одну diff-строку и маркер отсутствия финального newline.
func appendLine(builder *strings.Builder, prefix byte, line sourceLine) {
	builder.WriteByte(prefix)
	builder.WriteString(line.text)
	builder.WriteByte('\n')
	if !line.newline {
		builder.WriteString("\\ No newline at end of file\n")
	}
}

// Unified возвращает unified diff original→candidate с line-based LCS и тремя
// строками context вокруг каждой changed range. Формат заголовков файлов —
// `--- originalName` и `+++ candidateName`, формат hunk — `@@ -l,n +l,n @@`.
// При побайтово равных входах возвращается пустая строка. Inputs не мутируются.
func Unified(originalName, candidateName string, original, candidate []byte) string {
	if string(original) == string(candidate) {
		return ""
	}
	originalLines := splitLines(original)
	candidateLines := splitLines(candidate)
	ops := lcsOps(originalLines, candidateLines)

	var builder strings.Builder
	builder.WriteString("--- " + originalName + "\n")
	builder.WriteString("+++ " + candidateName + "\n")

	// Каждый hunk начинается с первой изменённой строки, расширенной влево
	// на contextLines keep-операций, и заканчивается после contextLines
	// keep-операций справа; соседние hunk-и сливаются при пересечении контекста.
	position := 0
	for position < len(ops) {
		if ops[position].kind == opKeep {
			position++
			continue
		}
		start := position
		keepLeft := 0
		for start > 0 && ops[start-1].kind == opKeep && keepLeft < contextLines {
			start--
			keepLeft++
		}
		end := position
		lastChange := position
		keepRight := 0
		for end < len(ops) {
			if ops[end].kind != opKeep {
				lastChange = end
				keepRight = 0
				end++
				continue
			}
			keepRight++
			end++
			if keepRight > contextLines {
				break
			}
		}
		end = lastChange + 1
		for keep := 0; keep < contextLines && end < len(ops) && ops[end].kind == opKeep; keep++ {
			end++
		}
		writeHunk(&builder, ops[start:end])
		position = end
	}
	return builder.String()
}

// writeHunk формирует один hunk вместе с заголовком диапазонов строк.
func writeHunk(builder *strings.Builder, hunk []op) {
	origCount, candCount := 0, 0
	var origNumbers, candNumbers []int
	for _, item := range hunk {
		switch item.kind {
		case opDelete:
			origCount++
			origNumbers = append(origNumbers, item.origNo)
		case opAdd:
			candCount++
			candNumbers = append(candNumbers, item.candNo)
		default:
			origCount++
			candCount++
			origNumbers = append(origNumbers, item.origNo)
			candNumbers = append(candNumbers, item.candNo)
		}
	}
	origStart := hunkRange(origNumbers, origCount)
	candStart := hunkRange(candNumbers, candCount)
	fmt.Fprintf(builder, "@@ -%d,%d +%d,%d @@\n", origStart, origCount, candStart, candCount)
	for _, item := range hunk {
		switch item.kind {
		case opDelete:
			appendLine(builder, '-', item.line)
		case opAdd:
			appendLine(builder, '+', item.line)
		default:
			appendLine(builder, ' ', item.line)
		}
	}
}
