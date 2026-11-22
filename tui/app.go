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
	// ScreenBlockDevices — блочные устройства (клавиша B, ТЗ §5.4, этап 3).
	ScreenBlockDevices
	// ScreenHistory — история backup (клавиша H, Task 6).
	ScreenHistory
	// ScreenLog — просмотр журнала (клавиша L, Task 5).
	ScreenLog
	// ScreenHelp — помощь (клавиша F1 или ?, Task 5).
	ScreenHelp
	// ScreenConfirmReload — модальное подтверждение перезагрузки target (F5
	// при несохранённых изменениях; default No, ТЗ §15).
	ScreenConfirmReload
	// ScreenConfirmQuit — модальное подтверждение выхода при несохранённых
	// изменениях (q/Ctrl+Q/Ctrl+C/F10; default No, ТЗ §9.2 и §15).
	ScreenConfirmQuit
)

// Options — параметры запуска TUI (все уже резолвлены cmd-слоем).
type Options struct {
	Target string // target fstab (default /etc/fstab, --file переопределяет)
	Lang   string // резолвленный язык ru|en
	Theme  string // tui.theme из config: "dark" (default) | "monochrome"
	Backup BackupSettings
	Log    LogSettings
	Runner sysutil.CommandRunner // findmnt; тесты подменяют
	Deps   *Deps                 // nil → ProductionDeps
}

// BackupSettings — dir/prefix/keep из config (пустой dir = каталог target).
type BackupSettings struct {
	Dir    string
	Prefix string
	Keep   int
}

// LogSettings — file/level/max_size_mb/keep из config.
type LogSettings struct {
	File      string
	Level     string
	MaxSizeMB int
	Keep      int
}

// model — bubbletea-модель TUI. Все поля unexported; тесты читают состояние
// через View() и ScreenNow().
type model struct {
	opts     Options
	theme    Theme
	screen   Screen
	document *models.Document // nil при ошибке загрузки
	loadErr  error
	cursor   int // выбранная строка таблицы (индекс записи)
	status   string
	// session несохранённые изменения и undo-стек; nil при ошибке загрузки
	// (loadErr-экран мутации недоступны, но доступ к полю защищён nil-check).
	session  *sessionState
	lastSave saveOutcome // итог последней попытки сохранения (ScreenSaveResult)
	// form — состояние формы добавления/редактирования записи; актуально,
	// пока screen == ScreenForm (Task 4).
	form formModel
	// mounts/logEvents/diff — данные вспомогательных экранов (Task 5):
	// заполняются синхронно в момент входа на экран клавишами M/L/D.
	mounts    mountsModel
	logEvents logModel
	diff      diffModel
	// blockdev — данные экрана блочных устройств (клавиша B, этап 3):
	// заполняются синхронно в момент входа на экран.
	blockdev blockdevModel
	// history — данные экрана истории backup (клавиша H, Task 6):
	// заполняются синхронно в момент входа на экран.
	history historyModel
	// severityByLine — кэш наихудшей severity диагностики валидатора по
	// физической строке документа (0/отсутствие — диагностик нет; info не
	// кэшируется). Пересчитывается rebuildDiagnostics на load/mutation/undo/
	// reload; рендер таблицы только читает, валидатор в View не вызывается.
	severityByLine map[int]models.Severity
}

// deps возвращает границы отказа модели: явно заданные через Options.Deps
// (тесты) или production-набор, материализованный лениво. NewModel не
// сохраняет production-набор в Options, чтобы zero-value model оставался
// рабочим в тестах Task 1/2.
func (m *model) deps() *Deps {
	if m.opts.Deps != nil {
		return m.opts.Deps
	}
	d := ProductionDeps(loggerOptions(m.opts))
	m.opts.Deps = &d
	return m.opts.Deps
}

// NewModel загружает target и собирает начальное состояние. Ошибка загрузки
// не паникует: показывается статусная строка с предложением F5.
func NewModel(opts Options) tea.Model {
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
	// Кэш диагностик считается сразу после загрузки: первая отрисовка таблицы
	// уже показывает маркеры "!"/"?" проблемных строк.
	m.rebuildDiagnostics()
	return m
}

// loggerOptions собирает logger.Options из резолвленных настроек TUI.
func loggerOptions(opts Options) logger.Options {
	return logger.Options{
		File: opts.Log.File, Level: logger.Severity(opts.Log.Level),
		MaxSizeMB: opts.Log.MaxSizeMB, Keep: opts.Log.Keep,
		RealUID: os.Getuid(), EffectiveUID: os.Geteuid(),
		SudoUser: sudoUser(), SudoUID: sudoUID(), // из env, числовой SUDO_UID
	}
}

// sudoUser читает SUDO_USER из окружения процесса; пустое значение означает
// запуск без sudo. Чтение env остаётся прямым: tui.Options намеренно не
// несёт getenv-инъекции, cmd-слой резолвит окружение для собственных нужд.
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
// Esc возвращает на таблицу; выход при несохранённых изменениях идёт через
// модальное подтверждение ScreenConfirmQuit (ТЗ §9.2).
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.screen {
		case ScreenTable:
			return m.updateTable(msg)
		case ScreenForm:
			return m.updateForm(msg)
		case ScreenConfirmReload:
			return m.updateConfirmReload(msg.String())
		case ScreenConfirmQuit:
			return m.updateConfirmQuit(msg.String())
		case ScreenMounts, ScreenLog, ScreenDiff:
			return m.updateScrollable(msg)
		case ScreenBlockDevices:
			return m.updateBlockDevices(msg)
		case ScreenHistory:
			return m.updateHistory(msg)
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
	case ScreenForm:
		return m.viewForm()
	case ScreenSaveResult:
		return m.viewSaveResult()
	case ScreenDiff:
		return m.viewDiff()
	case ScreenMounts:
		return m.viewMounts()
	case ScreenBlockDevices:
		return m.viewBlockDevices()
	case ScreenHistory:
		return m.viewHistory()
	case ScreenLog:
		return m.viewLog()
	case ScreenHelp:
		return m.viewHelp()
	case ScreenConfirmReload:
		return m.theme.Warning.Render(Tr(m.opts.Lang, "confirm_reload")) + "\n"
	case ScreenConfirmQuit:
		return m.theme.Warning.Render(Tr(m.opts.Lang, "confirm_quit")) + "\n"
	default:
		return Tr(m.opts.Lang, "screen_placeholder")
	}
}

// ScreenNow возвращает активный экран (для тестов).
func (m model) ScreenNow() Screen { return m.screen }

// Run запускает bubbletea-программу. Вызывается из cmd-слоя.
func Run(opts Options) error {
	_, err := tea.NewProgram(NewModel(opts), tea.WithAltScreen()).Run()
	return err
}
