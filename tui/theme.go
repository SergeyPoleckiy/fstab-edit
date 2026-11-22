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
// собираются из палитры общим helper'ом newTheme: чтобы добавить тему,
// достаточно описать ещё одну палитру, не трогая сборку стилей. Пустая строка
// означает «без цвета»: helper собирает стиль только с атрибутами — так
// monochrome сохраняет читаемость маркеров без ANSI-цвета (ТЗ §9.6).
// selectedFg/selectedBg пустые одновременно дают Reverse-выделение
// (совместимость с монохромными терминалами).
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

// lightPalette — палитра светлой темы (ТЗ §9.6: light — одна из четырёх
// обязательных тем). Цвета подобраны под белый/светлый фон терминала:
// текст тёмный, акценты насыщенные, но не яркие (яркие ANSI на белом
// фоне выцветают). Selected собирается контрастной парой «белый текст на
// синем фоне» — на светлом фоне Reverse-выделение почти незаметно, поэтому
// selectedFg/selectedBg заданы оба. Severity-цвета: красный 196 сохранён
// (он читается и на светлом фоне), а жёлтый 220 заменён на оранжевый 166:
// чистый жёлтый на белом неразличим (это же значение используется и для
// семантического warning).
var lightPalette = palette{
	title:       "25",  // синий: заголовки читаются на белом, не «выцветая»
	header:      "238", // тёмно-серый: шапка заметна, но не спорит с заголовком
	selectedFg:  "15",  // белый текст…
	selectedBg:  "25",  // …на синем фоне: контрастная пара для светлого терминала
	err:         "196", // красный: семантическая ошибка интерфейса
	warning:     "166", // оранжевый: жёлтый 220 на белом фоне неразличим
	info:        "235", // почти чёрный: основной читаемый текст
	muted:       "244", // серый: приглушённые подсказки клавиш
	dirty:       "166", // оранжевый: dirty-индикатор "*" заметен на белом
	fieldSource: "25",  // синий: идентификаторы устройств (UUID=/LABEL=/dev/...)
	fieldMount:  "28",  // тёмно-зелёный: точки монтирования (пути) — зелёный читается на белом
	fieldFSType: "166", // оранжевый: типы файловых систем — отличен от зелёного пути и синего source
	fieldOpts:   "240", // серый: списки опций не отвлекают от ключевых полей
	sevError:    "196", // красный: ошибка валидатора (единый с dark)
	sevWarning:  "166", // оранжевый: предупреждение валидатора (жёлтый на белом невидим)
}

// highContrastPalette — палитра темы повышенной контрастности (ТЗ §9.6:
// high-contrast — одна из четырёх обязательных тем). Назначение — читаемость
// при плохом зрении, на проекторе и в ярком помещении: фон терминала считаем
// тёмным, все цвета — яркие ANSI (8–15) либо максимально насыщенные из
// 256-палитры, никаких faint/pastel-оттенков. Selected собирается парой
// «чёрный текст на белом фоне» — чистая инверсия, максимально возможный
// контраст. Severity-цвета: ярко-красный 9 и ярко-жёлтый 11.
var highContrastPalette = palette{
	title:      "12", // bright blue: заголовок самый заметный элемент
	header:     "15", // bright white: шапка таблицы не тускнеет на проекторе
	selectedFg: "0",  // чёрный текст…
	selectedBg: "15", // …на белом фоне: инверсия, максимальный контраст
	err:        "9",  // bright red: семантическая ошибка интерфейса
	warning:    "11", // bright yellow: семантическое предупреждение
	info:       "15", // bright white: информационный текст
	muted:      "8",  // bright black: единственный «приглушённый» оттенок;
	// faint-атрибут Muted уменьшает яркость, поэтому цвет оставлен светлым
	dirty:       "11",  // bright yellow: dirty-индикатор "*" виден издалека
	fieldSource: "14",  // bright cyan: идентификаторы устройств
	fieldMount:  "10",  // bright green: точки монтирования
	fieldFSType: "208", // насыщенный оранжевый (нет bright-варианта в 0–15)
	fieldOpts:   "7",   // bright white/серый: опции читаемы, но не доминируют
	sevError:    "9",   // bright red: ошибка валидатора
	sevWarning:  "11",  // bright yellow: предупреждение валидатора
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

// LoadTheme возвращает тему по имени (ТЗ §9.6: допустимые имена — dark,
// light, high-contrast, monochrome; config-валидация отклоняет прочие имена
// раньше, до вызова) с применёнными поверх неё пользовательскими
// переопределениями ролей палитры (ТЗ v2 этап 4: «пользовательские темы»
// через tui.colors). overrides может быть nil или пустым — тогда результат
// совпадает с встроенной темой. Пустое и неизвестное имя дают dark —
// защитный fallback: public API пакета tui не должен падать, даже если
// валидирующий слой (internal/config) по какой-то причине пропустил
// недопустимое имя; неизвестные роли в overrides игнорируются по той же
// причине (формат валидирует internal/config, здесь — best-effort).
// Переопределения применяются и к monochrome: пользователь, задавший
// tui.colors, явно запросил цвета даже в теме без встроенной палитры —
// цвет добавляется к атрибутам (reverse/bold/underline сохраняются).
func LoadTheme(name string, overrides map[string]string) Theme {
	var th Theme
	switch name {
	case "monochrome":
		th = newTheme(monochromePalette)
		// Предупреждение в monochrome различается подчёркиванием: цвета нет,
		// а иерархия «ошибка bold / предупреждение underline» сохраняется.
		th.Warning = lipgloss.NewStyle().Underline(true)
	case "light":
		th = newTheme(lightPalette)
	case "high-contrast":
		th = newTheme(highContrastPalette)
	default:
		th = newTheme(darkPalette)
	}
	return applyOverrides(th, overrides)
}

// ansiColorNames — резолв имён ANSI-цветов в номера 0–15. Используемая
// библиотека (lipgloss v1 через termenv v0.15) принимает только hex #rrggbb
// и десятичные номера, поэтому имя конвертируется в номер до сборки стиля —
// иначе цвет молча не применился бы. Список зеркалит ansiColorNames пакета
// internal/config (валидация tui.colors): форматы слоёв не расходятся.
var ansiColorNames = map[string]string{
	"black":          "0",
	"red":            "1",
	"green":          "2",
	"yellow":         "3",
	"blue":           "4",
	"magenta":        "5",
	"cyan":           "6",
	"white":          "7",
	"bright_black":   "8",
	"bright_red":     "9",
	"bright_green":   "10",
	"bright_yellow":  "11",
	"bright_blue":    "12",
	"bright_magenta": "13",
	"bright_cyan":    "14",
	"bright_white":   "15",
}

// resolveColorValue преобразует значение роли tui.colors в строку, понятную
// lipgloss: имя ANSI-цвета → номер, hex и номер проходят без изменений.
func resolveColorValue(value string) string {
	if n, ok := ansiColorNames[value]; ok {
		return n
	}
	return value
}

// applyOverrides применяет пользовательские переопределения ролей палитры
// поверх собранной темы. Значение каждой роли — hex #rrggbb, номер ANSI
// 0–255 или имя ANSI-цвета; формат гарантирует internal/config, здесь
// применение best-effort и неизвестные роли игнорируются. Переопределяется
// только цвет: атрибуты стилей (bold/faint/underline/reverse) сохраняются,
// поэтому иерархия и текстовые признаки темы (ТЗ §9.4) не ломаются.
// selected_fg/selected_bg — единственные роли, адресующие не целый стиль,
// а его часть: они меняют Foreground/Background стиля Selected поверх
// собранного (включая Reverse monochrome: цвет добавляется к инверсии).
func applyOverrides(th Theme, overrides map[string]string) Theme {
	for role, value := range overrides {
		c := lipgloss.Color(resolveColorValue(value))
		switch role {
		case "title":
			th.Title = th.Title.Foreground(c)
		case "header":
			th.Header = th.Header.Foreground(c)
		case "selected_fg":
			th.Selected = th.Selected.Foreground(c)
		case "selected_bg":
			th.Selected = th.Selected.Background(c)
		case "muted":
			th.Muted = th.Muted.Foreground(c)
		case "info":
			th.Info = th.Info.Foreground(c)
		case "dirty":
			th.Dirty = th.Dirty.Foreground(c)
		case "error":
			th.Error = th.Error.Foreground(c)
		case "warning":
			th.Warning = th.Warning.Foreground(c)
		case "field_source":
			th.FieldSource = th.FieldSource.Foreground(c)
		case "field_mount":
			th.FieldMount = th.FieldMount.Foreground(c)
		case "field_fstype":
			th.FieldFSType = th.FieldFSType.Foreground(c)
		case "field_options":
			th.FieldOptions = th.FieldOptions.Foreground(c)
		}
		// Неизвестная роль игнорируется: защитный best-effort public API.
	}
	return th
}

// MarkError добавляет текстовый маркер ошибки "!": ТЗ §9.4 запрещает опираться
// только на цвет. Маркер ставится до стилизации, чтобы читаться без ANSI.
func (t Theme) MarkError(s string) string { return t.Error.Render("! " + s) }

// MarkWarning добавляет текстовый маркер предупреждения "?" (см. MarkError).
func (t Theme) MarkWarning(s string) string { return t.Warning.Render("? " + s) }
