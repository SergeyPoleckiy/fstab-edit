// Package tui содержит интерактивный интерфейс fstab-edit на bubbletea
// (этап 2 ТЗ). Пакет зависит от internal/* (прямой импорт), обратных
// зависимостей нет: бизнес-логика живёт в ядре, TUI — тонкий слой
// отображения и навигации.
package tui

import "github.com/charmbracelet/lipgloss"

// Theme — семантические стили приложения. Тема не задаёт шрифты глобально,
// а отдаёт стили по ролям; monochrome-вариант намеренно без цвета, потому
// что ТЗ §9.6 требует текстовые признаки ошибок во всех темах.
type Theme struct {
	Title    lipgloss.Style
	Header   lipgloss.Style
	Selected lipgloss.Style
	Error    lipgloss.Style
	Warning  lipgloss.Style
	Info     lipgloss.Style
	Muted    lipgloss.Style
	Dirty    lipgloss.Style
	// FieldSource/FieldMount/FieldFSType/FieldOptions подсвечивают поля записи
	// в таблице и предпросмотре формы разными цветами (ТЗ §5.3 «подсветка
	// синтаксиса»). В monochrome-версии совпадают с базовым стилем: подсветка
	// там работает только текстовыми маркерами.
	FieldSource  lipgloss.Style
	FieldMount   lipgloss.Style
	FieldFSType  lipgloss.Style
	FieldOptions lipgloss.Style
	// SevError/SevWarning — цвета severity диагностик валидатора: красный для
	// ошибки, жёлтый для предупреждения (ТЗ §5.3). Отдельны от Error/Warning:
	// те — семантика интерфейса (статусы, подтверждения), эти — данные отчёта.
	SevError   lipgloss.Style
	SevWarning lipgloss.Style
}

// palette — числовые ANSI-цвета семантических ролей одной темы. Все темы
// собираются из палитры общим helper'ом newTheme: чтобы добавить темы
// light/high-contrast (Task 2), достаточно описать ещё одну палитру, не
// трогая сборку стилей. Пустая строка означает «без цвета»: helper собирает
// стиль только с атрибутами — так monochrome сохраняет читаемость маркеров
// без ANSI-цвета (ТЗ §9.6). selectedFg/selectedBg пустые одновременно дают
// Reverse-выделение (совместимость с монохромными терминалами).
type palette struct {
	title       string // заголовки экранов
	header      string // шапка таблицы
	selectedFg  string // цвет текста выделенной строки
	selectedBg  string // фон выделенной строки
	err         string // семантическая ошибка интерфейса
	warning     string // семантическое предупреждение интерфейса
	info        string // информационный текст
	muted       string // приглушённый текст (подсказки клавиш)
	dirty       string // dirty-индикатор "*" в заголовке
	fieldSource string // поле source записи
	fieldMount  string // поле mount_point записи
	fieldFSType string // поле fs_type записи
	fieldOpts   string // поле options записи
	sevError    string // severity диагностик: ошибка (красный)
	sevWarning  string // severity диагностик: предупреждение (жёлтый)
}

// darkPalette — палитра темы по умолчанию (ТЗ §9.6: dark — тема по умолчанию).
// Цвета подобраны под тёмный фон терминала; severity-цвета совпадают с
// общепринятым красным/жёлтым (196/220 в 256-цветной палитре xterm).
var darkPalette = palette{
	title:       "39",
	header:      "244",
	selectedFg:  "0",
	selectedBg:  "39",
	err:         "196",
	warning:     "214",
	info:        "255",
	muted:       "240",
	dirty:       "220",
	fieldSource: "81",  // cyan: идентификаторы устройств (UUID=/LABEL=/dev/...)
	fieldMount:  "114", // зелёный: точки монтирования (пути)
	fieldFSType: "215", // оранжевый: типы файловых систем
	fieldOpts:   "250", // светло-серый: списки опций
	sevError:    "196", // красный: ошибка валидатора
	sevWarning:  "220", // жёлтый: предупреждение валидатора
}

// monochromePalette — палитра без единого цвета (все роли пустые). Тема
// собирается с атрибутами вместо цвета: bold/underline/reverse кодируют
// иерархию, а состояния читаются текстовыми маркерами (+/-/!/?/*).
var monochromePalette = palette{}

// fg собирает стиль с передним планом цвета роли; пустая роль палитры даёт
// стиль без цвета. Foreground(lipgloss.Color("")) не подходит: пустая строка
// превращается в Color("") с «цветом», а не в NoColor, и монохромная тема
// начинала бы излучать ANSI (тест hasForeground ловит именно это).
func fg(role string) lipgloss.Style {
	st := lipgloss.NewStyle()
	if role != "" {
		st = st.Foreground(lipgloss.Color(role))
	}
	return st
}

// newTheme собирает Theme из палитры. Атрибуты семантических ролей общие для
// всех тем (bold для ошибок, faint для muted): они кодируют иерархию, а не
// цвет, поэтому живут здесь, а не в палитре. Цвет применяется через fg только
// если роль в палитре непустая; selected без цветов даёт Reverse.
func newTheme(p palette) Theme {
	th := Theme{
		Title:        fg(p.title).Bold(true).Underline(true),
		Header:       fg(p.header).Bold(true),
		Error:        fg(p.err).Bold(true),
		Warning:      fg(p.warning),
		Info:         fg(p.info),
		Muted:        fg(p.muted).Faint(true),
		Dirty:        fg(p.dirty).Bold(true),
		FieldSource:  fg(p.fieldSource),
		FieldMount:   fg(p.fieldMount),
		FieldFSType:  fg(p.fieldFSType),
		FieldOptions: fg(p.fieldOpts),
		SevError:     fg(p.sevError).Bold(true),
		SevWarning:   fg(p.sevWarning),
	}
	switch {
	case p.selectedFg != "" && p.selectedBg != "":
		th.Selected = lipgloss.NewStyle().Foreground(lipgloss.Color(p.selectedFg)).Background(lipgloss.Color(p.selectedBg))
	case p.selectedBg != "":
		th.Selected = lipgloss.NewStyle().Background(lipgloss.Color(p.selectedBg))
	default:
		// Монохромный вариант выделения: инверсия вместо цветовой пары.
		th.Selected = lipgloss.NewStyle().Reverse(true)
	}
	return th
}

// LoadTheme возвращает тему по имени. Пустое и неизвестное имя дают dark
// (ТЗ §9.6: dark — тема по умолчанию).
func LoadTheme(name string) Theme {
	switch name {
	case "monochrome":
		th := newTheme(monochromePalette)
		// Предупреждение в monochrome различается подчёркиванием: цвета нет,
		// а иерархия «ошибка bold / предупреждение underline» сохраняется.
		th.Warning = lipgloss.NewStyle().Underline(true)
		return th
	default:
		return newTheme(darkPalette)
	}
}

// MarkError добавляет текстовый маркер ошибки "!": ТЗ §9.4 запрещает опираться
// только на цвет. Маркер ставится до стилизации, чтобы читаться без ANSI.
func (t Theme) MarkError(s string) string { return t.Error.Render("! " + s) }

// MarkWarning добавляет текстовый маркер предупреждения "?" (см. MarkError).
func (t Theme) MarkWarning(s string) string { return t.Warning.Render("? " + s) }
