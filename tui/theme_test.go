package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// hasForeground сообщает, задан ли у стиля реальный цвет переднего плана.
// lipgloss v1.0.0 (последняя версия, совместимая с go 1.22) возвращает для
// незаданного цвета NoColor{}, а не nil, поэтому сравниваем не с nil.
func hasForeground(st lipgloss.Style) bool {
	fg := st.GetForeground()
	if fg == nil {
		return false
	}
	_, isNoColor := fg.(lipgloss.NoColor)
	return !isNoColor
}

// TestLoadThemeDarkDefault проверяет, что пустое имя и "dark" дают цветную
// тему по умолчанию (ТЗ §9.6: dark — тема по умолчанию на этапе 2).
func TestLoadThemeDarkDefault(t *testing.T) {
	for _, name := range []string{"", "dark"} {
		th := LoadTheme(name, nil)
		if !hasForeground(th.Title) {
			t.Fatalf("dark: Title должен иметь цвет (name=%q)", name)
		}
	}
}

// TestLoadThemeMonochromeNoColor проверяет, что монохромная тема намеренно
// не задаёт цвет ни в одном семантическом стиле: признаки состояния должны
// читаться без ANSI (ТЗ §9.6).
func TestLoadThemeMonochromeNoColor(t *testing.T) {
	th := LoadTheme("monochrome", nil)
	// Все семантические стили монохромной темы не задают цвет.
	for name, st := range map[string]lipgloss.Style{
		"Title":    th.Title,
		"Header":   th.Header,
		"Selected": th.Selected,
		"Error":    th.Error,
		"Warning":  th.Warning,
		"Info":     th.Info,
		"Muted":    th.Muted,
		"Dirty":    th.Dirty,
	} {
		if hasForeground(st) {
			t.Fatalf("monochrome: %s не должен иметь цвет", name)
		}
	}
}

// TestLoadThemeLightAndHighContrast проверяет, что светлая и high-contrast
// темы собираются из своих палитр (ТЗ §9.6: четыре темы — dark, light,
// high-contrast, monochrome): базовые и Field-стили непустые и задают цвет.
func TestLoadThemeLightAndHighContrast(t *testing.T) {
	for _, name := range []string{"light", "high-contrast"} {
		th := LoadTheme(name, nil)
		for styleName, st := range map[string]lipgloss.Style{
			"Title":        th.Title,
			"Header":       th.Header,
			"Selected":     th.Selected,
			"Info":         th.Info,
			"FieldSource":  th.FieldSource,
			"FieldMount":   th.FieldMount,
			"FieldFSType":  th.FieldFSType,
			"FieldOptions": th.FieldOptions,
		} {
			if !hasForeground(st) {
				t.Fatalf("%s: %s должен иметь цвет", name, styleName)
			}
		}
	}
}

// TestSeverityColorsInAllColoredThemes проверяет, что severity-стили
// диагностик задают цвет во всех трёх цветных темах (ТЗ §5.3): SevError —
// красный, SevWarning — жёлтый/оранжевый, читаемый на фоне темы.
func TestSeverityColorsInAllColoredThemes(t *testing.T) {
	for _, name := range []string{"dark", "light", "high-contrast"} {
		th := LoadTheme(name, nil)
		if !hasForeground(th.SevError) {
			t.Fatalf("%s: SevError должен иметь цвет", name)
		}
		if !hasForeground(th.SevWarning) {
			t.Fatalf("%s: SevWarning должен иметь цвет", name)
		}
	}
}

// TestLoadThemeUnknownFallsBackDark проверяет откат неизвестной темы на dark.
func TestLoadThemeUnknownFallsBackDark(t *testing.T) {
	if !hasForeground(LoadTheme("nonsense", nil).Title) {
		t.Fatal("неизвестная тема должна давать dark")
	}
}

// TestMarksNotColorOnly проверяет текстовые маркеры ошибок и предупреждений:
// ТЗ §9.4 запрещает опираться только на цвет.
func TestMarksNotColorOnly(t *testing.T) {
	th := LoadTheme("monochrome", nil)
	if got := th.MarkError("x"); !strings.Contains(got, "!") {
		t.Fatalf("MarkError должен содержать текстовый маркер: %q", got)
	}
	if got := th.MarkWarning("x"); !strings.Contains(got, "?") {
		t.Fatalf("MarkWarning должен содержать текстовый маркер: %q", got)
	}
}

// TestHighlightStylesColored: в dark-теме Field-стили подсветки полей и
// severity-стили диагностик задают цвет (ТЗ §5.3: подсветка синтаксиса и
// ошибок/предупреждений цветом).
func TestHighlightStylesColored(t *testing.T) {
	th := LoadTheme("dark", nil)
	for name, st := range map[string]lipgloss.Style{
		"FieldSource":  th.FieldSource,
		"FieldMount":   th.FieldMount,
		"FieldFSType":  th.FieldFSType,
		"FieldOptions": th.FieldOptions,
		"SevError":     th.SevError,
		"SevWarning":   th.SevWarning,
	} {
		if !hasForeground(st) {
			t.Fatalf("dark: %s должен иметь цвет подсветки", name)
		}
	}
}

// TestHighlightStylesMonochromeNoColor: в monochrome-версии Field- и
// severity-стили совпадают с базовым стилем (без цвета) — подсветка там
// работает только текстовыми маркерами (ТЗ §9.6).
func TestHighlightStylesMonochromeNoColor(t *testing.T) {
	th := LoadTheme("monochrome", nil)
	for name, st := range map[string]lipgloss.Style{
		"FieldSource":  th.FieldSource,
		"FieldMount":   th.FieldMount,
		"FieldFSType":  th.FieldFSType,
		"FieldOptions": th.FieldOptions,
		"SevError":     th.SevError,
		"SevWarning":   th.SevWarning,
	} {
		if hasForeground(st) {
			t.Fatalf("monochrome: %s не должен иметь цвет", name)
		}
	}
}

// fgColor возвращает foreground стиля как lipgloss.Color: тесты тем знают,
// что стили собираются из lipgloss.Color, а не NoColor/AdaptiveColor.
func fgColor(t *testing.T, st lipgloss.Style) lipgloss.Color {
	t.Helper()
	fg, ok := st.GetForeground().(lipgloss.Color)
	if !ok {
		t.Fatalf("foreground не lipgloss.Color: %T (%v)", st.GetForeground(), st.GetForeground())
	}
	return fg
}

// bgColor — аналог fgColor для фона стиля.
func bgColor(t *testing.T, st lipgloss.Style) lipgloss.Color {
	t.Helper()
	bg, ok := st.GetBackground().(lipgloss.Color)
	if !ok {
		t.Fatalf("background не lipgloss.Color: %T (%v)", st.GetBackground(), st.GetBackground())
	}
	return bg
}

// TestLoadThemeOverridesSingleRole проверяет, что переопределение одной роли
// меняет только её: цвет Title заменяется, остальные стили совпадают с
// базовой темой, а атрибуты Title (bold/underline) сохраняются — пользователь
// меняет цвет, а не собирает стиль с нуля.
func TestLoadThemeOverridesSingleRole(t *testing.T) {
	base := LoadTheme("dark", nil)
	th := LoadTheme("dark", map[string]string{"title": "#00ffff"})
	if got := fgColor(t, th.Title); got != lipgloss.Color("#00ffff") {
		t.Fatalf("Title foreground = %q, want #00ffff", got)
	}
	if !th.Title.GetBold() || !th.Title.GetUnderline() {
		t.Fatal("атрибуты Title (bold/underline) должны сохраниться при переопределении цвета")
	}
	for name, pair := range map[string]struct {
		got, want lipgloss.Style
	}{
		"Header":       {th.Header, base.Header},
		"Selected":     {th.Selected, base.Selected},
		"Muted":        {th.Muted, base.Muted},
		"Info":         {th.Info, base.Info},
		"Dirty":        {th.Dirty, base.Dirty},
		"Error":        {th.Error, base.Error},
		"Warning":      {th.Warning, base.Warning},
		"FieldSource":  {th.FieldSource, base.FieldSource},
		"FieldMount":   {th.FieldMount, base.FieldMount},
		"FieldFSType":  {th.FieldFSType, base.FieldFSType},
		"FieldOptions": {th.FieldOptions, base.FieldOptions},
	} {
		if fgColor(t, pair.got) != fgColor(t, pair.want) {
			t.Fatalf("роль %s изменилась: got %v, want %v (переопределяли только title)", name, fgColor(t, pair.got), fgColor(t, pair.want))
		}
	}
	// muted сохраняет faint поверх нового цвета.
	th = LoadTheme("dark", map[string]string{"muted": "#123456"})
	if !th.Muted.GetFaint() {
		t.Fatal("Muted должен сохранить faint при переопределении цвета")
	}
}

// TestLoadThemeOverridesSelected проверяет частичное переопределение выделения:
// selected_fg/selected_bg меняют свою часть стиля Selected, не заданная часть
// сохраняется от базовой темы (dark: fg "0", bg "39").
func TestLoadThemeOverridesSelected(t *testing.T) {
	base := LoadTheme("dark", nil)

	th := LoadTheme("dark", map[string]string{"selected_fg": "255", "selected_bg": "0"})
	if got := fgColor(t, th.Selected); got != lipgloss.Color("255") {
		t.Fatalf("Selected foreground = %q, want 255", got)
	}
	if got := bgColor(t, th.Selected); got != lipgloss.Color("0") {
		t.Fatalf("Selected background = %q, want 0", got)
	}

	th = LoadTheme("dark", map[string]string{"selected_fg": "#ff00ff"})
	if got := fgColor(t, th.Selected); got != lipgloss.Color("#ff00ff") {
		t.Fatalf("Selected foreground = %q, want #ff00ff", got)
	}
	if got := bgColor(t, th.Selected); got != bgColor(t, base.Selected) {
		t.Fatalf("Selected background = %q, want базовый %q (переопределяли только fg)", got, bgColor(t, base.Selected))
	}
}

// TestLoadThemeOverridesANSINames проверяет резолв имён ANSI-цветов в номера:
// lipgloss/termenv принимает только hex и номера, поэтому "red" должен стать
// "1", "bright_white" — "15", иначе цвет молча не применился бы.
func TestLoadThemeOverridesANSINames(t *testing.T) {
	th := LoadTheme("dark", map[string]string{
		"error":  "red",
		"info":   "bright_white",
		"header": "bright_black",
	})
	if got := fgColor(t, th.Error); got != lipgloss.Color("1") {
		t.Fatalf("Error foreground = %q, want 1 (red)", got)
	}
	if got := fgColor(t, th.Info); got != lipgloss.Color("15") {
		t.Fatalf("Info foreground = %q, want 15 (bright_white)", got)
	}
	if got := fgColor(t, th.Header); got != lipgloss.Color("8") {
		t.Fatalf("Header foreground = %q, want 8 (bright_black)", got)
	}
}

// TestLoadThemeOverridesMonochrome проверяет применение цветов поверх
// монохромной темы: пользователь, задавший tui.colors, явно запросил цвета —
// они добавляются к атрибутам (Reverse выделения и bold заголовка
// сохраняются), а непереопределённые стили остаются без цвета.
func TestLoadThemeOverridesMonochrome(t *testing.T) {
	th := LoadTheme("monochrome", map[string]string{
		"title":       "196",
		"selected_fg": "255",
		"selected_bg": "238",
	})
	if !hasForeground(th.Title) {
		t.Fatal("monochrome + colors: Title должен получить цвет")
	}
	if !th.Title.GetBold() || !th.Title.GetUnderline() {
		t.Fatal("monochrome + colors: атрибуты Title должны сохраниться")
	}
	if got := fgColor(t, th.Selected); got != lipgloss.Color("255") {
		t.Fatalf("Selected foreground = %q, want 255", got)
	}
	if got := bgColor(t, th.Selected); got != lipgloss.Color("238") {
		t.Fatalf("Selected background = %q, want 238", got)
	}
	if !th.Selected.GetReverse() {
		t.Fatal("monochrome + colors: Reverse выделения должен сохраниться")
	}
	// Непереопределённые роли остаются без цвета.
	if hasForeground(th.Header) {
		t.Fatal("monochrome: Header не должен получить цвет без переопределения")
	}
	if hasForeground(th.FieldSource) {
		t.Fatal("monochrome: FieldSource не должен получить цвет без переопределения")
	}
}

// TestLoadThemeOverridesUnknownRoleIgnored проверяет защитный best-effort
// public API: неизвестная роль в переопределениях игнорируется без паники,
// тема совпадает с базовой (формат валидирует internal/config до LoadTheme).
func TestLoadThemeOverridesUnknownRoleIgnored(t *testing.T) {
	base := LoadTheme("dark", nil)
	th := LoadTheme("dark", map[string]string{"nonsense": "#00ffff", "title": "red"})
	if got := fgColor(t, th.Title); got != lipgloss.Color("1") {
		t.Fatalf("Title foreground = %q, want 1 (red)", got)
	}
	if fgColor(t, th.Header) != fgColor(t, base.Header) {
		t.Fatal("неизвестная роль не должна влиять на другие стили")
	}
}

// TestLoadThemeNilOverridesEqualsBuiltin проверяет, что nil- и пустые
// переопределения дают тему, совпадающую со встроенной: вызовы без
// tui.colors (все существующие сценарии) не меняют поведение.
func TestLoadThemeNilOverridesEqualsBuiltin(t *testing.T) {
	for _, name := range []string{"dark", "light", "high-contrast"} {
		builtIn := LoadTheme(name, nil)
		empty := LoadTheme(name, map[string]string{})
		if fgColor(t, builtIn.Title) != fgColor(t, empty.Title) {
			t.Fatalf("%s: пустые переопределения изменили Title", name)
		}
		if fgColor(t, builtIn.Selected) != fgColor(t, empty.Selected) ||
			bgColor(t, builtIn.Selected) != bgColor(t, empty.Selected) {
			t.Fatalf("%s: пустые переопределения изменили Selected", name)
		}
	}
	// Monochrome без переопределений не задаёт цвета ни в одном стиле.
	builtIn := LoadTheme("monochrome", nil)
	empty := LoadTheme("monochrome", map[string]string{})
	for styleName, pair := range map[string]struct {
		builtIn, empty lipgloss.Style
	}{
		"Title":    {builtIn.Title, empty.Title},
		"Selected": {builtIn.Selected, empty.Selected},
		"Header":   {builtIn.Header, empty.Header},
	} {
		if hasForeground(pair.builtIn) != hasForeground(pair.empty) {
			t.Fatalf("monochrome: пустые переопределения изменили %s", styleName)
		}
	}
}
