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
