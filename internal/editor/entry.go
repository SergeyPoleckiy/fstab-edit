package editor

import (
	"sort"
	"strings"

	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// NewEntry содержит decoded значения всех полей новой записи fstab.
// Пустые Dump и Pass заменяются стандартным значением "0".
type NewEntry struct {
	Source     string
	MountPoint string
	FSType     string
	Options    string
	Dump       string
	Pass       string
}

// Changes описывает только явно запрошенные изменения записи. Nil означает, что
// поле сохраняется; UnsetDump и UnsetPass удаляют соответствующее optional-поле.
type Changes struct {
	Source     *string
	MountPoint *string
	FSType     *string
	Options    *string
	Dump       *string
	Pass       *string
	UnsetDump  bool
	UnsetPass  bool
}

// replacement задаёт замену byte range в исходной Node.Raw. Start == End
// обозначает вставку, что позволяет добавить optional-поля перед trailing whitespace.
type replacement struct {
	start int
	end   int
	raw   string
}

// replaceRanges применяет replacements справа налево, поэтому записанные в них
// исходные offsets остаются корректными после каждой предыдущей замены.
func replaceRanges(raw string, replacements []replacement) string {
	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start > replacements[j].start
	})
	for _, item := range replacements {
		raw = raw[:item.start] + item.raw + raw[item.end:]
	}
	return raw
}

// Add кодирует decoded поля и добавляет новую запись только в память. Сначала
// готовится независимый candidate; ошибка кодирования или разбора ничего не коммитит.
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
	// Новая физическая строка должна быть отделена от прежнего последнего узла,
	// даже если исходный документ заканчивался без newline.
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

// Edit точечно изменяет выбранную запись, сохраняя separators, escapes, comments
// и endings. Все проверки и построение candidate выполняются до единственного commit.
func Edit(document *models.Document, selector Selector, changes Changes) (bool, error) {
	index, err := Select(document, selector)
	if err != nil {
		return false, err
	}
	node := document.Nodes[index]
	entry := node.Entry
	if entry == nil || (node.Kind != models.NodeEntry && node.Kind != models.NodeCommentedEntry) {
		return false, ErrWrongState
	}
	if err := validateChanges(changes); err != nil {
		return false, err
	}

	// Итоговые присутствия вычисляются до replacements: Pass никогда не должен
	// остаться без Dump, а отсутствующие поля не получают неявных defaults.
	hasDump := (entry.Dump != nil && !changes.UnsetDump) || changes.Dump != nil
	hasPass := (entry.Pass != nil && !changes.UnsetPass) || changes.Pass != nil
	if hasPass && !hasDump {
		return false, ErrInvalidChange
	}

	var replacements []replacement
	mandatory := []struct {
		value *string
		field models.Field
	}{
		{changes.Source, entry.Source},
		{changes.MountPoint, entry.MountPoint},
		{changes.FSType, entry.FSType},
		{changes.Options, entry.Options},
	}
	for _, item := range mandatory {
		replacement, err := fieldReplacement(item.value, item.field)
		if err != nil {
			return false, err
		}
		if replacement != nil {
			replacements = append(replacements, *replacement)
		}
	}
	if entry.Dump != nil {
		replacement, err := fieldReplacement(changes.Dump, *entry.Dump)
		if err != nil {
			return false, err
		}
		if replacement != nil {
			replacements = append(replacements, *replacement)
		}
	}
	if entry.Pass != nil {
		replacement, err := fieldReplacement(changes.Pass, *entry.Pass)
		if err != nil {
			return false, err
		}
		if replacement != nil {
			replacements = append(replacements, *replacement)
		}
	}

	// Удаление начинается сразу после предыдущего поля, поэтому вместе с полем
	// исчезает весь непрерывный separator перед ним. Whitespace после поля остаётся.
	if changes.UnsetPass && entry.Pass != nil {
		start := entry.Options.End
		if entry.Dump != nil {
			start = entry.Dump.End
		}
		if changes.UnsetDump && entry.Dump != nil {
			start = entry.Options.End
		}
		replacements = append(replacements, replacement{start: start, end: entry.Pass.End})
	} else if changes.UnsetDump && entry.Dump != nil {
		replacements = append(replacements, replacement{start: entry.Options.End, end: entry.Dump.End})
	}

	// Отсутствующие optional-поля собираются одним suffix в порядке Dump, Pass.
	// Вставка на End последнего существующего поля сохраняет trailing whitespace после него.
	lastField := entry.Options
	if entry.Dump != nil && !changes.UnsetDump {
		lastField = *entry.Dump
	}
	var addition strings.Builder
	if entry.Dump == nil && changes.Dump != nil {
		raw, err := parser.EncodeField(*changes.Dump)
		if err != nil {
			return false, err
		}
		addition.WriteByte('\t')
		addition.WriteString(raw)
	}
	if entry.Pass == nil && changes.Pass != nil {
		raw, err := parser.EncodeField(*changes.Pass)
		if err != nil {
			return false, err
		}
		addition.WriteByte('\t')
		addition.WriteString(raw)
	}
	if addition.Len() > 0 {
		replacements = append(replacements, replacement{
			start: lastField.End,
			end:   lastField.End,
			raw:   addition.String(),
		})
	}

	candidate := cloneDocument(document)
	changedNode, err := rebuildNode(replaceRanges(node.Raw, replacements), node.Ending, index+1, node.Kind)
	if err != nil {
		return false, err
	}
	candidate.Nodes[index] = changedNode
	return commitDocument(document, candidate), nil
}

// validateChanges отклоняет противоречивые set/unset, пустой набор изменений
// и пустые значения всех явно переданных полей до кодирования и копирования.
func validateChanges(changes Changes) error {
	if (changes.Dump != nil && changes.UnsetDump) || (changes.Pass != nil && changes.UnsetPass) {
		return ErrInvalidChange
	}
	values := []*string{
		changes.Source,
		changes.MountPoint,
		changes.FSType,
		changes.Options,
		changes.Dump,
		changes.Pass,
	}
	hasChange := changes.UnsetDump || changes.UnsetPass
	for _, value := range values {
		if value == nil {
			continue
		}
		hasChange = true
		if *value == "" {
			return ErrEmptyField
		}
	}
	if !hasChange {
		return ErrInvalidChange
	}
	return nil
}

// fieldReplacement сравнивает decoded значения, чтобы no-op сохранял исходный raw
// escape. Для действительно нового значения используются только исходные Start/End.
func fieldReplacement(value *string, field models.Field) (*replacement, error) {
	if value == nil || *value == field.Value {
		return nil, nil
	}
	raw, err := parser.EncodeField(*value)
	if err != nil {
		return nil, err
	}
	return &replacement{start: field.Start, end: field.End, raw: raw}, nil
}

// Delete удаляет только выбранный узел записи. Comments, blank lines и их endings
// остаются в candidate в исходном порядке, а Entry.Line пересчитывается при commit.
func Delete(document *models.Document, selector Selector) (bool, error) {
	index, err := Select(document, selector)
	if err != nil {
		return false, err
	}
	candidate := cloneDocument(document)
	candidate.Nodes = append(candidate.Nodes[:index], candidate.Nodes[index+1:]...)
	return commitDocument(document, candidate), nil
}
