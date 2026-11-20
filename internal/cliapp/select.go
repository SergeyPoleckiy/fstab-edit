// Файл select.go разрешает CLI-селекторы в индексы узлов документа.
// Логика интерпретации значений (positional, --line, --mount, --source)
// сосредоточена здесь, а точный поиск и неоднозначность делегируются editor.Select.
package cliapp

import (
	"errors"
	"strconv"

	"fstab-edit/internal/editor"
	"fstab-edit/pkg/models"
)

// SelectorFromValues собирает editor.Selector из CLI-значений. positional,
// line, mount и source взаимоисключающи: задано ровно ноль или больше одного
// способа для команды, требующей селектор (required == true), — usage error
// с code 2. Для необязательного селектора (required == false) ноль способов
// допустим и возвращает zero Selector.
func SelectorFromValues(positional, line, mount, source string, required bool) (editor.Selector, *ExitError) {
	ways := 0
	for _, value := range []string{positional, line, mount, source} {
		if value != "" {
			ways++
		}
	}
	switch {
	case ways == 0 && required:
		return editor.Selector{}, UsageError(nil, "требуется селектор: позиционный аргумент, --line, --mount или --source")
	case ways > 1:
		return editor.Selector{}, UsageError(nil, "селектор задан несколькими способами: используйте только один из позиционного аргумента, --line, --mount, --source")
	}
	switch {
	case positional != "":
		return positionalSelector(positional), nil
	case line != "":
		parsed, err := strconv.Atoi(line)
		if err != nil || parsed < 1 {
			return editor.Selector{}, UsageError(nil, "--line требует положительный номер физической строки, получено %q", line)
		}
		return editor.ByLine(parsed), nil
	case mount != "":
		return editor.ByMountPoint(mount), nil
	case source != "":
		return editor.BySource(source), nil
	default:
		return editor.Selector{}, nil
	}
}

// positionalSelector интерпретирует позиционный аргумент: число превращается
// в селектор строки, любое другое значение — в полное decoded значение
// mount point без усечения и перекодирования.
func positionalSelector(value string) editor.Selector {
	if line, err := strconv.Atoi(value); err == nil && line >= 1 {
		return editor.ByLine(line)
	}
	return editor.ByMountPoint(value)
}

// Chooser выбирает одну строку из списка кандидатов в интерактивном режиме.
// Возвращаемое значение — one-based номер физической строки из candidates.
type Chooser interface {
	Choose(candidates []int) (int, error)
}

// Resolve выбирает zero-based индекс узла document по selector. Однозначное
// совпадение возвращается без chooser. Неоднозначность без интерактива
// (interactive == false) или без chooser даёт ExitError code 8 со списком
// candidate lines; chooser при этом не вызывается. В интерактивном режиме
// chooser выбирает одну из candidate lines; посторонний выбор снова даёт
// code 8. Остальные ошибки селектора (not found, invalid) дают code 1.
func Resolve(document *models.Document, selector editor.Selector, chooser Chooser, interactive bool) (int, *ExitError) {
	index, err := editor.Select(document, selector)
	if err == nil {
		return index, nil
	}
	var ambiguity *editor.AmbiguityError
	if errors.As(err, &ambiguity) {
		if !interactive || chooser == nil {
			return -1, AmbiguityError(err, ambiguity.Lines,
				"селектор неоднозначен, уточните его или выберите строку")
		}
		picked, chooseErr := chooser.Choose(append([]int(nil), ambiguity.Lines...))
		if chooseErr != nil {
			return -1, AmbiguityError(chooseErr, ambiguity.Lines,
				"не удалось выбрать строку из кандидатов")
		}
		for _, line := range ambiguity.Lines {
			if line == picked {
				return line - 1, nil
			}
		}
		return -1, AmbiguityError(nil, ambiguity.Lines,
			"выбранная строка %d не входит в список кандидатов", picked)
	}
	return -1, ValidationError(err, "запись не выбрана")
}
