package tui

import (
	"os"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/logger"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/sysutil"
	"fstab-edit/pkg/models"
)

// Screen перечисляет экраны приложения.
type Screen int

const (
	// ScreenTable — главная таблица записей (стартовый экран).
	ScreenTable Screen = iota
	// ScreenForm — добавление/редактирование записи (Task 4).
	ScreenForm
	// ScreenDiff — просмотр diff перед сохранением (Task 5).
	ScreenDiff
	// ScreenSaveResult — итог сохранения, включая rollback (Task 3).
	ScreenSaveResult
	// ScreenConflict — конфликт версии: diff + перезагрузка (Task 6).
	ScreenConflict
	// ScreenMounts — статус монтирований (клавиша M, Task 5).
	ScreenMounts
	// ScreenHistory — история backup (клавиша H, Task 6).
	ScreenHistory
	// ScreenLog — просмотр журнала (клавиша L, Task 5).
	ScreenLog
	// ScreenHelp — помощь (клавиша F1 или ?, Task 5).
	ScreenHelp
)

// options — параметры запуска TUI (все уже резолвлены cmd-слоем).
type options struct {
	Target string // target fstab (default /etc/fstab, --file переопределяет)
	Lang   string // резолвленный язык ru|en
	Theme  string // tui.theme из config: "dark" (default) | "monochrome"
	Backup backupSettings
	Log    logSettings
	Runner sysutil.CommandRunner // findmnt; тесты подменяют
	Deps   *Deps                 // nil → ProductionDeps
}

// backupSettings — dir/prefix/keep из config (пустой dir = каталог target).
type backupSettings struct {
	Dir    string
	Prefix string
	Keep   int
}

// logSettings — file/level/max_size_mb/keep из config.
type logSettings struct {
	File      string
	Level     string
	MaxSizeMB int
	Keep      int
}

// model — bubbletea-модель TUI. Все поля unexported; тесты читают состояние
// через View() и ScreenNow().
type model struct {
	opts     options
	theme    Theme
	screen   Screen
	document *models.Document // nil при ошибке загрузки
	loadErr  error
	cursor   int // выбранная строка таблицы (индекс записи)
	status   string
	// session несохранённые изменения и undo-стек; nil при ошибке загрузки
	// (loadErr-экран мутации недоступны, но доступ к полю защищён nil-check).
	session *sessionState
}

// NewModel загружает target и собирает начальное состояние. Ошибка загрузки
// не паникует: показывается статусная строка с предложением F5.
func NewModel(opts options) tea.Model {
	m := model{opts: opts, theme: LoadTheme(opts.Theme), screen: ScreenTable}
	deps := opts.Deps
	if deps == nil {
		d := ProductionDeps(loggerOptions(opts))
		deps = &d
	}
	snapshot, err := deps.ReadSnapshot(opts.Target)
	if err != nil {
		m.loadErr = err
		return m
	}
	m.document = parser.Parse(snapshot.Data)
	m.session = &sessionState{original: append([]byte(nil), snapshot.Data...)}
	return m
}

// loggerOptions собирает logger.Options из резолвленных настроек TUI.
func loggerOptions(opts options) logger.Options {
	return logger.Options{
		File: opts.Log.File, Level: logger.Severity(opts.Log.Level),
		MaxSizeMB: opts.Log.MaxSizeMB, Keep: opts.Log.Keep,
		RealUID: os.Getuid(), EffectiveUID: os.Geteuid(),
		SudoUser: sudoUser(), SudoUID: sudoUID(), // из env, числовой SUDO_UID
	}
}

// sudoUser читает SUDO_USER из окружения; пустое значение означает запуск
// без sudo. Инъекция окружения появится в Task 7 — пока прямое чтение env.
func sudoUser() string { return os.Getenv("SUDO_USER") }

// sudoUID читает SUDO_UID из окружения; значение включается в журнал только
// если оно непустое и успешно разбирается как целое число.
func sudoUID() string {
	raw := os.Getenv("SUDO_UID")
	if raw == "" {
		return ""
	}
	if _, err := strconv.Atoi(raw); err != nil {
		return ""
	}
	return raw
}

// Init bubbletea: нет фоновых команд на старте.
func (m model) Init() tea.Cmd { return nil }

// Update обрабатывает сообщения. Навигация общая для всех экранов:
// Esc возвращает на таблицу, выход — с подтверждением при dirty (Task 3
// расширит confirmQuit; здесь — заглушка без dirty-логики).
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.screen {
		case ScreenTable:
			return m.updateTable(msg)
		default:
			if msg.String() == "esc" {
				m.screen = ScreenTable
				return m, nil
			}
			return m, nil
		}
	}
	return m, nil
}

// View рендерит активный экран.
func (m model) View() string {
	if m.loadErr != nil {
		return Trf(m.opts.Lang, "load_error", m.opts.Target, m.loadErr.Error()) + "\n\n" +
			Tr(m.opts.Lang, "keys_help_hint") + "\n"
	}
	switch m.screen {
	case ScreenTable:
		return m.viewTable()
	default:
		return Tr(m.opts.Lang, "screen_placeholder")
	}
}

// ScreenNow возвращает активный экран (для тестов).
func (m model) ScreenNow() Screen { return m.screen }

// Run запускает bubbletea-программу. Вызывается из cmd-слоя.
func Run(opts options) error {
	_, err := tea.NewProgram(NewModel(opts), tea.WithAltScreen()).Run()
	return err
}
