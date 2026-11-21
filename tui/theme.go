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
}

// LoadTheme возвращает тему по имени. Пустое и неизвестное имя дают dark
// (ТЗ §9.6: dark — тема по умолчанию на этапе 2).
func LoadTheme(name string) Theme {
	if name == "monochrome" {
		return Theme{
			Title:    lipgloss.NewStyle().Bold(true).Underline(true),
			Header:   lipgloss.NewStyle().Bold(true),
			Selected: lipgloss.NewStyle().Reverse(true),
			Error:    lipgloss.NewStyle().Bold(true),
			Warning:  lipgloss.NewStyle().Underline(true),
			Info:     lipgloss.NewStyle(),
			Muted:    lipgloss.NewStyle().Faint(true),
			Dirty:    lipgloss.NewStyle().Bold(true),
		}
	}
	return Theme{
		Title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")),
		Header:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244")),
		Selected: lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39")),
		Error:    lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		Warning:  lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		Info:     lipgloss.NewStyle().Foreground(lipgloss.Color("255")),
		Muted:    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Dirty:    lipgloss.NewStyle().Foreground(lipgloss.Color("220")),
	}
}

// MarkError добавляет текстовый маркер ошибки "!": ТЗ §9.4 запрещает опираться
// только на цвет. Маркер ставится до стилизации, чтобы читаться без ANSI.
func (t Theme) MarkError(s string) string { return t.Error.Render("! " + s) }

// MarkWarning добавляет текстовый маркер предупреждения "?" (см. MarkError).
func (t Theme) MarkWarning(s string) string { return t.Warning.Render("? " + s) }
