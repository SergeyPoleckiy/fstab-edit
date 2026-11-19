// Файл options.go разбирает список опций активной записи, обнаруживает пустые,
// повторные, неизвестные и конфликтующие элементы без раскрытия их значений.
// Здесь же формируются предупреждения и подсказки для swap, системных mount,
// сетевых файловых систем и опций, влияющих на монтирование.
package validator

import (
	"strconv"
	"strings"

	"fstab-edit/pkg/models"
)

// knownExactOptions содержит только общие опции с точным именем. Список не
// претендует на полноту mount(8): неизвестное имя даёт warning, а не error.
var knownExactOptions = map[string]struct{}{
	"defaults": {}, "ro": {}, "rw": {}, "auto": {}, "noauto": {},
	"user": {}, "nouser": {}, "users": {}, "owner": {}, "group": {},
	"exec": {}, "noexec": {}, "suid": {}, "nosuid": {}, "dev": {}, "nodev": {},
	"async": {}, "sync": {}, "dirsync": {},
	"atime": {}, "noatime": {}, "relatime": {}, "norelatime": {},
	"strictatime": {}, "nostrictatime": {}, "diratime": {}, "nodiratime": {},
	"nofail": {}, "_netdev": {}, "bind": {}, "rbind": {}, "remount": {}, "loop": {},
	"mand": {}, "nomand": {}, "acl": {}, "noacl": {}, "discard": {}, "nodiscard": {},
}

// knownValueOptions перечисляет ключи, принимаемые с непустым значением. Сами
// значения не валидируются: среди них могут быть пути и секреты, а проверка
// FS-specific семантики относится к отдельным последующим правилам.
var knownValueOptions = map[string]struct{}{
	"uid": {}, "gid": {}, "mode": {}, "umask": {}, "dmask": {}, "fmask": {},
	"offset": {}, "size": {}, "errors": {}, "credentials": {}, "username": {},
	"password": {}, "workdir": {}, "lowerdir": {}, "upperdir": {},
}

// exclusiveOptionPair описывает два безопасных для вывода имени, которые нельзя
// осмысленно применить одновременно. Порядок среза определяет порядок conflicts.
var exclusiveOptionPairs = []struct {
	first  string
	second string
}{
	{first: "rw", second: "ro"},
	{first: "auto", second: "noauto"},
	{first: "user", second: "nouser"},
	{first: "exec", second: "noexec"},
	{first: "suid", second: "nosuid"},
	{first: "dev", second: "nodev"},
	{first: "async", second: "sync"},
	{first: "atime", second: "noatime"},
	{first: "diratime", second: "nodiratime"},
}

// exclusiveOptionIndex ускоряет распознавание имени без итерации по map при
// создании diagnostics; сами conflicts выводятся по stable slice выше.
var exclusiveOptionIndex = func() map[string]int {
	result := make(map[string]int, len(exclusiveOptionPairs)*2)
	for index, pair := range exclusiveOptionPairs {
		result[pair.first] = index
		result[pair.second] = index
	}
	return result
}()

// exclusiveOptionState накапливает только имена опций одной пары. Отдельные
// признаки сторон позволяют отличить повтор одной опции от настоящего конфликта.
type exclusiveOptionState struct {
	first      string
	last       string
	firstSeen  bool
	secondSeen bool
}

// validateOptions разбирает непустое decoded поле Options без нормализации.
// Raw item и value используются только в памяти для сравнения; диагностики
// содержат позиции и безопасные имена, но не исходные значения options.
func validateOptions(collector *collector, entry *models.Entry) {
	value := entry.Options.Value
	if value == "" {
		return
	}

	seenExact := make(map[string]struct{})
	seenUnknown := make(map[string]struct{})
	lastValues := make(map[string]string)
	pairStates := make([]exclusiveOptionState, len(exclusiveOptionPairs))

	for index, raw := range strings.Split(value, ",") {
		if raw == "" {
			addEmptyOption(collector, entry.Line, index)
			continue
		}

		name, optionValue, hasValue := strings.Cut(raw, "=")
		if name == "" {
			addEmptyOption(collector, entry.Line, index)
			continue
		}

		if _, exists := seenExact[raw]; exists {
			collector.add(models.Diagnostic{
				Code:     "option.duplicate",
				Severity: models.SeverityWarning,
				Line:     entry.Line,
				Field:    "options",
				Message:  "Опция повторяется без изменения значения",
			})
		} else {
			seenExact[raw] = struct{}{}
		}

		_, exactKnown := knownExactOptions[name]
		_, valueKnown := knownValueOptions[name]
		if hasValue {
			if previous, exists := lastValues[name]; exists && previous != optionValue {
				params := map[string]string{"effective": "last"}
				if valueKnown {
					params["key"] = name
				}
				collector.add(models.Diagnostic{
					Code:     "option.conflict",
					Severity: models.SeverityWarning,
					Line:     entry.Line,
					Field:    "options",
					Message:  "Повторный ключ изменил значение; применяется последнее вхождение",
					Params:   params,
				})
			}
			lastValues[name] = optionValue
		}

		if pairIndex, exists := exclusiveOptionIndex[name]; exists && !hasValue {
			pair := exclusiveOptionPairs[pairIndex]
			state := &pairStates[pairIndex]
			if state.first == "" {
				state.first = name
			}
			state.last = name
			switch name {
			case pair.first:
				state.firstSeen = true
			case pair.second:
				state.secondSeen = true
			}
		}

		if exactKnown && !hasValue || valueKnown && hasValue && optionValue != "" {
			continue
		}
		if _, reported := seenUnknown[name]; reported {
			continue
		}
		seenUnknown[name] = struct{}{}
		collector.add(models.Diagnostic{
			Code:     "option.unknown",
			Severity: models.SeverityWarning,
			Line:     entry.Line,
			Field:    "options",
			Message:  "Имя опции отсутствует во встроенном списке или требует непустого значения",
		})
	}

	for index := range exclusiveOptionPairs {
		state := pairStates[index]
		if !state.firstSeen || !state.secondSeen {
			continue
		}
		collector.add(models.Diagnostic{
			Code:     "option.conflict",
			Severity: models.SeverityWarning,
			Line:     entry.Line,
			Field:    "options",
			Message:  "Взаимоисключающие опции заданы вместе; применяется последняя",
			Params: map[string]string{
				"first": state.first, "last": state.last, "effective": state.last,
			},
		})
	}
}

// addEmptyOption сообщает one-based позицию пустого элемента. Это единственные
// структурированные данные, необходимые для локализации такой диагностики.
func addEmptyOption(collector *collector, line, index int) {
	collector.add(models.Diagnostic{
		Code:     "option.empty",
		Severity: models.SeverityError,
		Line:     line,
		Field:    "options",
		Message:  "Список опций содержит пустой элемент или опцию без имени",
		Params:   map[string]string{"index": strconv.Itoa(index + 1)},
	})
}

// validateSpecial добавляет предупреждения и пояснения после разбора options.
// Каждая info-диагностика создаётся не более одного раза на entry, даже если
// один и тот же признак обнаружен одновременно в FS type и нескольких опциях.
func validateSpecial(collector *collector, entry *models.Entry) {
	switch entry.MountPoint.Value {
	case "/", "/boot", "/boot/efi", "/usr":
		collector.add(models.Diagnostic{
			Code:     "mount.system",
			Severity: models.SeverityWarning,
			Line:     entry.Line,
			Field:    "mount_point",
			Value:    entry.MountPoint.Value,
			Message:  "Системная точка монтирования требует особой осторожности",
		})
	}
	if entry.FSType.Value == "swap" {
		collector.add(models.Diagnostic{
			Code:     "swap.review",
			Severity: models.SeverityWarning,
			Line:     entry.Line,
			Field:    "fs_type",
			Message:  "Активная swap-запись требует проверки перед сохранением",
		})
	}

	hasNoAuto := false
	hasNoFail := false
	hasNetdev := isNetworkFS(entry.FSType.Value)
	hasBind := false
	if entry.Options.Value != "" {
		for _, option := range strings.Split(entry.Options.Value, ",") {
			switch option {
			case "noauto":
				hasNoAuto = true
			case "nofail":
				hasNoFail = true
			case "_netdev":
				hasNetdev = true
			case "bind", "rbind":
				hasBind = true
			}
		}
	}

	if hasNoAuto {
		addOptionInfo(collector, entry.Line, "option.noauto", "Запись не монтируется автоматически")
	}
	if hasNoFail {
		addOptionInfo(collector, entry.Line, "option.nofail", "Ошибка монтирования не должна останавливать загрузку")
	}
	if hasNetdev {
		addOptionInfo(collector, entry.Line, "option.netdev", "Запись зависит от готовности сети")
	}
	if hasBind {
		addOptionInfo(collector, entry.Line, "option.bind", "Запись описывает bind-монтирование существующего дерева")
	}
}

// addOptionInfo создаёт пояснение без Value и Params, поскольку исходное поле
// Options может содержать секреты и не должно отражаться в Report.
func addOptionInfo(collector *collector, line int, code, message string) {
	collector.add(models.Diagnostic{
		Code:     code,
		Severity: models.SeverityInfo,
		Line:     line,
		Field:    "options",
		Message:  message,
	})
}

// isNetworkFS перечисляет сетевые типы, которым нужна инициализация сети. Любой
// fuse.* включается по правилу validator, тогда как базовый fuse не включается.
func isNetworkFS(fsType string) bool {
	if strings.HasPrefix(fsType, "fuse.") {
		return true
	}
	switch fsType {
	case "nfs", "nfs4", "cifs", "smbfs", "sshfs":
		return true
	default:
		return false
	}
}
