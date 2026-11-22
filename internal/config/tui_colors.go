// Файл tui_colors.go — валидация пользовательской палитры TUI (ключ
// tui.colors, ТЗ v2 этап 4: «пользовательские темы»). Палитра — это
// переопределения семантических ролей цвета поверх встроенной темы
// tui.theme. Задача пакета config — отклонить неизвестную роль или цвет
// невалидного формата до старта TUI: отказ конфигурации, exit code 2.
// Применение переопределений к стилям — ответственность tui.LoadTheme.
package config

import (
	"fmt"
	"slices"
	"strconv"
)

// knownTUIColorRoles — множество семантических ролей палитры TUI,
// допустимых в tui.colors. Список зеркалит структуру palette и маппинг
// applyOverrides в tui/theme.go: новая роль палитры обязана появиться там
// и здесь одновременно. Роли severity-стилей диагностик (sev_error/
// sev_warning) намеренно недоступны: их цвета зафиксированы ТЗ §5.3
// (красный/жёлтый) и пользователем не переопределяются.
var knownTUIColorRoles = map[string]struct{}{
	"title":         {}, // заголовки экранов
	"header":        {}, // шапка таблицы
	"selected_fg":   {}, // цвет текста выделенной строки
	"selected_bg":   {}, // фон выделенной строки
	"muted":         {}, // приглушённый текст (подсказки клавиш)
	"info":          {}, // информационный текст
	"dirty":         {}, // dirty-индикатор "*" в заголовке
	"error":         {}, // семантическая ошибка интерфейса
	"warning":       {}, // семантическое предупреждение интерфейса
	"field_source":  {}, // поле source записи (подсветка синтаксиса)
	"field_mount":   {}, // поле mount_point записи
	"field_fstype":  {}, // поле fs_type записи
	"field_options": {}, // поле options записи
}

// ansiColorNames — имена ANSI-цветов (базовые 0–7 и bright-варианты 8–15),
// допустимые в tui.colors. Используемая библиотека терминальных цветов
// (lipgloss v1 через termenv v0.15) принимает только hex #rrggbb и номера
// 0–255, поэтому имена резолвятся в номера при применении (resolveColorValue
// в tui/theme.go); этот список — источник истины для валидации формата.
// Регистр строгий: только lowercase, «Red»/«RED» невалидны.
var ansiColorNames = map[string]int{
	"black":          0,
	"red":            1,
	"green":          2,
	"yellow":         3,
	"blue":           4,
	"magenta":        5,
	"cyan":           6,
	"white":          7,
	"bright_black":   8,
	"bright_red":     9,
	"bright_green":   10,
	"bright_yellow":  11,
	"bright_blue":    12,
	"bright_magenta": 13,
	"bright_cyan":    14,
	"bright_white":   15,
}

// TUIColorReason — причина отказа валидации одной пары роль→цвет из
// tui.colors. Значение маппится на i18n-ключ сообщения в cmd-слое:
// unknown_role → config.bad_tui_color_role, invalid_color →
// config.bad_tui_color_value (каталоги RU/EN обязаны содержать оба).
type TUIColorReason string

const (
	// TUIColorUnknownRole — роль не входит в knownTUIColorRoles.
	TUIColorUnknownRole TUIColorReason = "unknown_role"
	// TUIColorInvalidColor — формат цвета не входит в допустимые:
	// hex #rrggbb, номер ANSI 0–255 или имя из ansiColorNames.
	TUIColorInvalidColor TUIColorReason = "invalid_color"
)

// TUIColorError — отказ валидации пары роль→цвет из tui.colors. Отдельный
// тип позволяет cmd-слою отличить этот отказ от прочих сбоев конфигурации
// и выбрать локализованное сообщение по Reason; Error() держит русский
// текст как fallback для логов и для ExitError.Message без ключа.
type TUIColorError struct {
	Role   string         // отклонённая роль
	Value  string         // отклонённое значение цвета
	Reason TUIColorReason // unknown_role | invalid_color
}

// Error возвращает русское диагностическое сообщение (fallback; точка
// печати cmd переводит его ключами по Reason).
func (e *TUIColorError) Error() string {
	switch e.Reason {
	case TUIColorUnknownRole:
		return fmt.Sprintf("неизвестная роль цвета tui.colors %q: допустимые роли: %s",
			e.Role, TUIColorRoleList())
	case TUIColorInvalidColor:
		return fmt.Sprintf(
			"некорректный цвет tui.colors %q для роли %q: допустимы hex #rrggbb, номер ANSI 0–255 или имя ANSI-цвета",
			e.Value, e.Role)
	default:
		return fmt.Sprintf("некорректный tui.colors: роль %q, значение %q", e.Role, e.Value)
	}
}

// TUIColorRoleList возвращает допустимые роли через запятую в стабильном
// (отсортированном) порядке — для сообщений об ошибках и документации.
func TUIColorRoleList() string {
	roles := make([]string, 0, len(knownTUIColorRoles))
	for role := range knownTUIColorRoles {
		roles = append(roles, role)
	}
	slices.Sort(roles)
	return joinComma(roles)
}

// joinComma склеивает строки через запятую с пробелом (strings.Join
// вынесен сюда, чтобы сообщение об ошибке и его EN-перевод в i18n собирали
// список одинаково).
func joinComma(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ", "
		}
		out += item
	}
	return out
}

// validateTUIColors отклоняет неизвестные роли и цвета невалидного формата
// в резолвленной палитре. Проверка идёт по алфавиту отсортированных ролей,
// чтобы при нескольких ошибках сообщение было детерминированным. Пустая или
// nil-мапа (colors не задан ни на одном уровне) — валидна.
func validateTUIColors(colors map[string]string) error {
	if len(colors) == 0 {
		return nil
	}
	roles := make([]string, 0, len(colors))
	for role := range colors {
		roles = append(roles, role)
	}
	slices.Sort(roles)
	for _, role := range roles {
		if _, ok := knownTUIColorRoles[role]; !ok {
			return &TUIColorError{Role: role, Value: colors[role], Reason: TUIColorUnknownRole}
		}
		if !validTUIColor(colors[role]) {
			return &TUIColorError{Role: role, Value: colors[role], Reason: TUIColorInvalidColor}
		}
	}
	return nil
}

// validTUIColor проверяет формат одного значения цвета. Допустимы три
// формата: hex #rrggbb (ровно 7 символов, 6 hex-цифр любого регистра),
// номер ANSI 0–255 (только ASCII-цифры, без знака и пробелов) и имя из
// ansiColorNames (строго lowercase). Частичные форматы (#rgb, 5 hex-цифр,
// «256», «gray», «Bright_Red») отклоняются — иначе lipgloss молча не
// отрендерил бы цвет (termenv вернул бы nil для непонятной строки).
func validTUIColor(value string) bool {
	// Hex #rrggbb: '#' и ровно шесть hex-цифр.
	if len(value) == 7 && value[0] == '#' {
		for _, c := range value[1:] {
			if !isHexDigit(c) {
				return false
			}
		}
		return true
	}
	// Номер ANSI: непустая строка из десятичных цифр, значение 0–255.
	if isDigits(value) {
		n, err := strconv.Atoi(value)
		return err == nil && n <= 255
	}
	// Имя ANSI-цвета (bright-варианты через подчёркивание, lowercase).
	_, ok := ansiColorNames[value]
	return ok
}

// isHexDigit сообщает, является ли руна hex-цифрой (0-9, a-f, A-F).
func isHexDigit(c rune) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// isDigits сообщает, непуста ли строка и состоит ли она целиком из
// ASCII-цифр (пробелы, знак '+'/'-' и прочее отклоняются).
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
