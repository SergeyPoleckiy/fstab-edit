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
		th := LoadTheme(name)
		if !hasForeground(th.Title) {
			t.Fatalf("dark: Title должен иметь цвет (name=%q)", name)
		}
	}
}

// TestLoadThemeMonochromeNoColor проверяет, что монохромная тема намеренно
// не задаёт цвет ни в одном семантическом стиле: признаки состояния должны
// читаться без ANSI (ТЗ §9.6).
func TestLoadThemeMonochromeNoColor(t *testing.T) {
	th := LoadTheme("monochrome")
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
		th := LoadTheme(name)
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
		th := LoadTheme(name)
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
	if !hasForeground(LoadTheme("nonsense").Title) {
		t.Fatal("неизвестная тема должна давать dark")
	}
}

// TestMarksNotColorOnly проверяет текстовые маркеры ошибок и предупреждений:
// ТЗ §9.4 запрещает опираться только на цвет.
func TestMarksNotColorOnly(t *testing.T) {
	th := LoadTheme("monochrome")
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
	th := LoadTheme("dark")
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
	th := LoadTheme("monochrome")
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
