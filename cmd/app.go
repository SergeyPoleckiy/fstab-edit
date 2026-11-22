// Package cmd содержит Cobra-команды CLI fstab-edit: флаги, prompt-подтверждения,
// wiring доменного слоя internal/cliapp с сериализаторами internal/output и
// маппинг ошибок на стабильные exit codes. Пакет не хранит mutable globals:
// всё состояние живёт в экземпляре App, а границы ввода-вывода, runner и
// dependencies передаются через AppOptions, что позволяет тестам работать с
// memory buffers, временными файлами и fake runner без реального /etc/fstab,
// findmnt и TTY.
package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/config"
	"fstab-edit/internal/i18n"
	"fstab-edit/internal/logger"
	"fstab-edit/internal/output"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/writer"
	"fstab-edit/pkg/models"
	"fstab-edit/tui"
)

// defaultTarget — production target по умолчанию. Флаг --file переопределяет
// его; справка и версия не читают target вовсе.
const defaultTarget = "/etc/fstab"

// logLevels — допустимые значения флага --log-level. Logger реализуется
// следующим блоком, но значение валидируется уже сейчас, чтобы опечатка
// (--log-level=trace) отклонялась usage error, а не молчаливо игнорировалась.
var logLevels = map[string]struct{}{"debug": {}, "info": {}, "warn": {}, "error": {}}

// languages — допустимые значения флага --lang. Полные каталоги RU/EN
// реализованы в internal/i18n; --lang по умолчанию пуст: язык резолвится
// из окружения и локали (приоритет ТЗ §15: --lang > FSTAB_EDIT_LANG >
// LC_ALL > LANG > ru).
var languages = map[string]struct{}{"ru": {}, "en": {}}

// AppOptions описывает внешние зависимости App. Nil writers заменяются
// io.Discard и пустым reader, nil TTY считается non-TTY, nil Runner —
// production sysutil.Runner. Dependencies необязателен: заданные поля
// перекрывают соответствующие production-зависимости Service и нужны тестам
// для детерминированного моделирования сбоев транзакции.
type AppOptions struct {
	// Stdout принимает обычные данные и machine output (ровно один document).
	Stdout io.Writer
	// Stderr принимает диагностику, diff перед подтверждением и prompts.
	Stderr io.Writer
	// Stdin читается только в интерактивных prompt-подтверждениях.
	Stdin io.Reader
	// Version печатается флагом --version; пустое значение означает "dev".
	Version string
	// Runner запускает findmnt; тесты передают fake CommandRunner.
	Runner sysutil.CommandRunner
	// TTY сообщает, доступен ли интерактивный ввод для подтверждений.
	TTY func() bool
	// Chooser выбирает строку из кандидатов при неоднозначном селекторе;
	// вызывается только при TTY и без --yes.
	Chooser cliapp.Chooser
	// Dependencies перекрывает выбранные production-зависимости Service.
	Dependencies *cliapp.Dependencies
	// Getenv читает переменные окружения при резолве языка (LC_ALL, LANG),
	// sudo-контекста журнала (SUDO_USER, SUDO_UID) и значений config;
	// nil — production os.Getenv. Тесты подменяют для детерминированного
	// выбора языка независимо от локали машины прогона.
	Getenv func(string) string
	// Environ возвращает окружение при резолве языка (FSTAB_EDIT_LANG) и
	// загрузке config (FSTAB_EDIT_*); nil — production os.Environ. Семантика
	// — как у Getenv.
	Environ func() []string
	// Getuid/Geteuid возвращают real/effective UID процесса для security-checks
	// config и контекста событий журнала; nil — production os.Getuid/os.Geteuid.
	Getuid  func() int
	Geteuid func() int
	// Now возвращает текущее время для измерения duration операций и
	// timestamp событий; nil — production time.Now.
	Now func() time.Time
	// DefaultConfigPath задаёт значение флага --config по умолчанию; пустое —
	// production: флаг без default и config ищется по авто-путям system/user.
	// Тесты передают путь к YAML в tempdir, чтобы журнал и настройки не
	// касались системных путей (/var/log, /etc/fstab-edit).
	DefaultConfigPath string
	// TuiLauncher запускает TUI вместо production tui.Run; получает
	// полностью собранные tui.Options. Nil — production tui.Run. Тесты
	// подменяют заглушкой, чтобы проверить сборку options без запуска
	// bubbletea-программы.
	TuiLauncher func(tui.Options) error
}

// App связывает Cobra root command, глобальные флаги и доменный Service.
// Один экземпляр обслуживает один запуск Execute; одновременных вызовов нет.
type App struct {
	options AppOptions
	service *cliapp.Service
	flags   globalFlags
	stdin   *bufio.Reader
	root    *cobra.Command
	// lang — язык, выбранный резолвом в начале Execute: все prompt-строки,
	// диагностика и help-тексты печатаются на нём. Заполняется до
	// root.Execute, поэтому handler'ы читают поле, а не флаг.
	lang string
	// settings — настройки, резолвленные из config/env/CLI в PersistentPreRunE
	// (приоритеты ТЗ §13.1). Заполнены до любого handler'а.
	settings config.Settings
	// log — открытый журнал; nil для read-only команд и --dry-run (ТЗ §14:
	// файловый журнал для них не создаётся). Открывается до mutation, чтобы
	// недоступность журнала блокировала изменение target.
	log *logger.Logger
}

// globalFlags хранит значения persistent-флагов корня; заполняются Cobra
// непосредственно перед запуском handler, поэтому отдельной синхронизации нет.
type globalFlags struct {
	file      string
	output    string
	logLevel  string
	lang      string
	config    string
	backupDir string
	dryRun    bool
	yes       bool
	verbose   bool
	quiet     bool
	offline   bool
}

// NewApp собирает App: применяет defaults опций, строит Service с
// production-зависимостями (с учётом перекрытий) и регистрирует все команды.
func NewApp(options AppOptions) *App {
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	if options.Stdin == nil {
		options.Stdin = strings.NewReader("")
	}
	if options.Getenv == nil {
		options.Getenv = os.Getenv
	}
	if options.Environ == nil {
		options.Environ = os.Environ
	}
	if options.Getuid == nil {
		options.Getuid = os.Getuid
	}
	if options.Geteuid == nil {
		options.Geteuid = os.Geteuid
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.Runner == nil {
		options.Runner = sysutil.NewRunner()
	}
	dependencies := cliapp.ProductionDependencies(options.Runner)
	if options.Dependencies != nil {
		dependencies = mergeDependencies(dependencies, *options.Dependencies)
	}
	app := &App{
		options: options,
		service: cliapp.NewService(dependencies, options.Runner),
	}
	// Единственный buffered reader на stdin: prompt-подтверждения и
	// интерактивный chooser разделяют один буфер, иначе readahead одного
	// reader'а потерял бы ввод другого (scripted «2\ny\n» между prompt'ами).
	// Производственный вызывающий код (main) уже создал *bufio.Reader и
	// передаёт его chooser'у, поэтому повторная обёртка недопустима;
	// прочие reader'ы (тесты) оборачиваются здесь ровно один раз.
	if reader, ok := options.Stdin.(*bufio.Reader); ok {
		app.stdin = reader
	} else {
		app.stdin = bufio.NewReader(options.Stdin)
	}
	app.root = app.buildRootCommand()
	return app
}

// mergeDependencies перекрывает production-зависимости Service только
// непустыми полями override: тест задаёт ровно те границы, которые ему нужны,
// остальные остаются production-реализациями.
func mergeDependencies(base, override cliapp.Dependencies) cliapp.Dependencies {
	if override.ReadSnapshot != nil {
		base.ReadSnapshot = override.ReadSnapshot
	}
	if override.ExecuteTransaction != nil {
		base.ExecuteTransaction = override.ExecuteTransaction
	}
	if override.CreateBackup != nil {
		base.CreateBackup = override.CreateBackup
	}
	if override.SelectBackup != nil {
		base.SelectBackup = override.SelectBackup
	}
	if override.OpenBackup != nil {
		base.OpenBackup = override.OpenBackup
	}
	if override.Replace != nil {
		base.Replace = override.Replace
	}
	if override.VerifyTarget != nil {
		base.VerifyTarget = override.VerifyTarget
	}
	return base
}

// Execute запускает root command с заданными аргументами и возвращает exit
// code процесса. Язык резолвится до построения help-текстов: pre-scan args на
// --lang/--lang= (последнее вхождение), иначе env/локаль; дерево команд
// переводится localize до root.Execute, поэтому --help получает уже
// переведённые Short/usage. Cobra-ошибки (неизвестный флаг, некорректные
// аргументы) отображаются на code 2 с сообщением выбранного языка;
// *cliapp.ExitError сохраняет свой код и публикует сообщение со списком
// кандидатов в stderr.
func (a *App) Execute(args []string) int {
	a.lang = i18n.Resolve(scanLangFlag(args), a.options.Environ(),
		a.options.Getenv("LC_ALL"), a.options.Getenv("LANG"))
	a.localize(a.lang)
	// Запуск без аргументов открывает TUI: пустой argv подменяется на
	// ["tui"] до парсинга. Явные аргументы (--help, --version, подкоманды)
	// подмене не подвержены.
	if len(args) == 0 {
		args = []string{"tui"}
	}
	a.root.SetArgs(args)
	err := a.root.Execute()
	// Журнал закрывается после root.Execute: handler'ы дописали события,
	// файловый дескриптор освобождается штатно. nil-safe: read-only и
	// dry-run команды журнал не открывали.
	if a.log != nil {
		_ = a.log.Close()
	}
	if err == nil {
		return cliapp.ExitSuccess
	}
	var exitErr *cliapp.ExitError
	if errors.As(err, &exitErr) {
		a.printExitError(exitErr)
		return exitErr.Code
	}
	// Ошибки разбора флагов и аргументов Cobra не типизированы доменом;
	// для пользователя это usage error, сообщение переводится по ключу.
	// Writers App — память или потоки процесса; игнорирование ошибки записи
	// здесь допустимо, как и в пакете output.
	_, _ = fmt.Fprintln(a.options.Stderr, i18n.Tf(a.lang, "error.usage.bad_flag", err))
	return cliapp.ExitUsage
}

// scanLangFlag находит последнее вхождение --lang <v> или --lang=<v> в args.
// Невалидное значение не отклоняется здесь: Resolve проигнорирует его и
// перейдёт на следующий уровень (env/локаль), а validateGlobalFlags отклонит
// оставшееся валидным usage error.
func scanLangFlag(args []string) string {
	lang := ""
	for i, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--lang="); ok {
			lang = value
			continue
		}
		if arg == "--lang" && i+1 < len(args) {
			lang = args[i+1]
		}
	}
	return lang
}

// localize переводит Short/Long корня и всех подкоманд и usage-строки
// persistent/локальных флагов из каталога выбранного языка. Ключи:
// help.root.short, help.cmd.<use-префикс>.short, help.flag.<имя>. Ключи,
// которых нет в каталогах, оставляют текущий русский текст (T() вернул бы
// сам ключ) — поэтому замена выполняется только при i18n.Has. Inherited-флаги
// не трогаются: это те же объекты, что persistent-флаги родителя, и они уже
// переведены при обходе корня.
func (a *App) localize(lang string) {
	a.localizeCommand(a.root, lang, "help.root.short")
	for _, command := range a.root.Commands() {
		a.localizeSubtree(command, lang)
	}
}

// localizeSubtree рекурсивно переводит команду и всех её потомков.
func (a *App) localizeSubtree(command *cobra.Command, lang string) {
	a.localizeCommand(command, lang, "help.cmd."+usePrefix(command.Use)+".short")
	for _, child := range command.Commands() {
		a.localizeSubtree(child, lang)
	}
}

// localizeCommand переводит Short, Long (если задан) и usage флагов одной
// команды по ключу shortKey для Short.
func (a *App) localizeCommand(command *cobra.Command, lang, shortKey string) {
	if i18n.Has(shortKey) {
		command.Short = i18n.T(lang, shortKey)
	}
	longKey := strings.Replace(shortKey, ".short", ".long", 1)
	if command.Long != "" && i18n.Has(longKey) {
		command.Long = i18n.T(lang, longKey)
	}
	localizeFlags(command.PersistentFlags(), lang)
	localizeFlags(command.Flags(), lang)
}

// localizeFlags переводит usage-строки флагов набора по ключу
// help.flag.<имя>: имя флага уникально в пределах CLI, поэтому один ключ
// обслуживает и persistent, и локальные флаги всех команд. Дефисы имени
// флага (dry-run) заменяются на подчёркивания — таков формат ключей каталога.
func localizeFlags(flags *pflag.FlagSet, lang string) {
	flags.VisitAll(func(flag *pflag.Flag) {
		key := "help.flag." + strings.ReplaceAll(flag.Name, "-", "_")
		if i18n.Has(key) {
			flag.Usage = i18n.T(lang, key)
		}
	})
}

// usePrefix возвращает префикс Use команды до первого пробела или '<':
// "delete <id>" → "delete", "rollback [backup-id]" → "rollback".
func usePrefix(use string) string {
	if index := strings.IndexAny(use, " <"); index >= 0 {
		return use[:index]
	}
	return use
}

// printExitError публикует диагностику ExitError в stderr: при непустом
// MessageKey сообщение форматируется из каталога выбранного языка (русский
// Message остаётся fallback через Error()), иначе печатается Message как есть.
// При неоднозначности добавляется список candidate lines по ключу
// prompt.candidates. Prompt и diff handler'ы уже напечатаны к этому моменту
// отдельно.
func (a *App) printExitError(exitErr *cliapp.ExitError) {
	message := exitErr.Error()
	if exitErr.MessageKey != "" {
		// Перевод форматируется теми же MessageArgs; сохранённая причина
		// дописывается, чтобы диагностика не теряла исходную ошибку нижнего
		// слоя, как это делает Error().
		message = i18n.Tf(a.lang, exitErr.MessageKey, exitErr.MessageArgs...)
		if exitErr.Cause != nil && exitErr.Cause.Error() != "" {
			message = fmt.Sprintf("%s: %v", message, exitErr.Cause)
		}
	}
	_, _ = fmt.Fprintln(a.options.Stderr, message)
	if len(exitErr.Candidates) > 0 {
		lines := make([]string, len(exitErr.Candidates))
		for i, line := range exitErr.Candidates {
			lines[i] = fmt.Sprintf("%d", line)
		}
		_, _ = fmt.Fprintln(a.options.Stderr, i18n.Tf(a.lang, "prompt.candidates", strings.Join(lines, ", ")))
	}
}

// buildRootCommand собирает корневую команду: persistent-флаги по Global
// Constraints, валидация формата/level/lang и конфликта verbose/quiet до
// любого handler, отключение completion-команды и кастомный шаблон версии.
func (a *App) buildRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "fstab-edit",
		Short: "безопасное редактирование fstab с валидацией, backup и rollback",
		// Ошибки и usage печатает сам App в едином стиле, а не Cobra.
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       a.options.Version,
		// Completion shell-скрипты не входят в контракт CLI первого блока.
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := a.validateGlobalFlags(); err != nil {
				return err
			}
			if exitErr := a.resolveSettings(cmd); exitErr != nil {
				return exitErr
			}
			// Журнал открывается до любого handler'а мутации (ТЗ §14):
			// недоступность журнала блокирует изменение target раньше,
			// чем backup/lock/write начнутся. Dry-run и read-only команды
			// файловый журнал не создают.
			if isMutatingCommand(cmd.Name()) && !a.flags.dryRun {
				if exitErr := a.openLog(); exitErr != nil {
					return exitErr
				}
			}
			return nil
		},
	}
	root.SetVersionTemplate("fstab-edit version {{.Version}}\n")
	// Cobra пишет help/usage/version в потоки App, а не напрямую в os.Stdout:
	// тесты перехватывают вывод целиком через memory buffers.
	root.SetOut(a.options.Stdout)
	root.SetErr(a.options.Stderr)

	flags := root.PersistentFlags()
	flags.StringVar(&a.flags.file, "file", defaultTarget, "путь к редактируемому fstab-файлу")
	flags.BoolVar(&a.flags.dryRun, "dry-run", false, "показать diff без записи, backup и lock")
	flags.BoolVarP(&a.flags.yes, "yes", "y", false, "подтвердить допустимую операцию без prompt")
	flags.StringVarP(&a.flags.output, "output", "o", string(output.FormatTable), "формат вывода: table, json, csv, tsv")
	flags.StringVar(&a.flags.logLevel, "log-level", "info", "уровень журнала: debug, info, warn, error")
	flags.BoolVarP(&a.flags.verbose, "verbose", "v", false, "дополнительная диагностика")
	flags.BoolVarP(&a.flags.quiet, "quiet", "q", false, "подавить обычный stdout (ошибки сохраняются)")
	flags.StringVar(&a.flags.backupDir, "backup-dir", "", "каталог резервных копий (по умолчанию — каталог target)")
	flags.BoolVar(&a.flags.offline, "offline", false, "отключить проверки внешних ресурсов")
	flags.StringVar(&a.flags.lang, "lang", "", "язык сообщений: ru, en (по умолчанию — авто-резолв из окружения)")
	flags.StringVar(&a.flags.config, "config", a.options.DefaultConfigPath, "путь к YAML-конфигурации")

	root.AddCommand(
		a.buildListCommand(),
		a.buildAddCommand(),
		a.buildEditCommand(),
		a.buildDeleteCommand(),
		a.buildCommentCommand(),
		a.buildUncommentCommand(),
		a.buildValidateCommand(),
		a.buildVerifyMountCommand(),
		a.buildBackupCommand(),
		a.buildRollbackCommand(),
		a.buildTuiCommand(),
	)
	return root
}

// validateGlobalFlags отклоняет некорректные комбинации глобальных флагов
// до command handler: формат вывода, уровень журнала, язык и конфликт
// verbose/quiet. Сообщения формируются K-конструкторами с i18n-ключами:
// точка печати (printExitError) переводит их выбранным языком. Пустой --lang
// означает авто-резолв из окружения и допустим; валидируются только явные
// значения.
func (a *App) validateGlobalFlags() error {
	switch output.Format(a.flags.output) {
	case output.FormatTable, output.FormatJSON, output.FormatCSV, output.FormatTSV:
	default:
		return cliapp.UsageErrorK(nil, "error.usage.unknown_output",
			"неизвестный формат вывода %q: допустимы table, json, csv, tsv", a.flags.output)
	}
	if _, ok := logLevels[a.flags.logLevel]; !ok {
		return cliapp.UsageErrorK(nil, "error.usage.bad_log_level",
			"некорректный --log-level %q: допустимы debug, info, warn, error", a.flags.logLevel)
	}
	if a.flags.lang != "" {
		if _, ok := languages[a.flags.lang]; !ok {
			return cliapp.UsageErrorK(nil, "error.usage.bad_lang",
				"некорректный --lang %q: допустимы ru, en", a.flags.lang)
		}
	}
	if a.flags.verbose && a.flags.quiet {
		return cliapp.UsageErrorK(nil, "error.usage.verbose_quiet",
			"флаги --verbose и --quiet несовместимы: выберите один режим вывода")
	}
	return nil
}

// isMutatingCommand сообщает, относится ли команда к мутирующим: именно
// они открывают журнал до выполнения (ТЗ §14). Read-only list/validate/
// verify-mount журнал не создают.
func isMutatingCommand(name string) bool {
	switch name {
	case "add", "edit", "delete", "comment", "uncomment", "rollback", "backup":
		return true
	}
	return false
}

// resolveSettings загружает настройки через config.Load (приоритеты ТЗ
// §13.1: CLI > FSTAB_EDIT_* > user YAML > system YAML > defaults) и вливает
// поверх них явно заданные CLI-флаги. PersistentPreRunE гарантирует вызов
// до любого handler'а; normalize-флаги (file/backup-dir/log-level) делают
// резолвленные значения доступными handler'ам через a.flags без изменения
// их кода. --lang выше env/локали уже обеспечивают pre-scan в Execute,
// config язык не задаёт (ТЗ §13.3). Ошибки маппятся на exit codes: security
// → 3, остальные → 2, сообщения переводятся ключами config.*.
func (a *App) resolveSettings(cmd *cobra.Command) *cliapp.ExitError {
	settings, warnings, err := config.Load(config.LoadOptions{
		ExplicitPath: a.flags.config,
		Environ:      a.options.Environ(),
		EUID:         a.options.Geteuid(),
		LookupUser:   config.LookupUserByUID,
	})
	if err != nil {
		// Отказ валидации пользовательской палитры tui.colors: сообщение
		// собирается детальным i18n-ключом по причине отказа (неизвестная
		// роль / невалидный цвет), а не общим config.invalid — так диагностика
		// полностью локализуется и сразу называет отклонённую пару.
		// Cause не заполняется: иначе printExitError продублировал бы русский
		// fallback Message после локализованного текста.
		var colorErr *config.TUIColorError
		if errors.As(err, &colorErr) {
			// Путь файла-источника из Load (при автозагрузке --config пуст,
			// а реальный путь знает только config.Load); флаг — запасной
			// вариант для гипотетических прямых вызовов валидации.
			path := colorErr.Path
			if path == "" {
				path = a.flags.config
			}
			key := "config.bad_tui_color_role"
			args := []any{colorErr.Role, path, config.TUIColorRoleList()}
			if colorErr.Reason == config.TUIColorInvalidColor {
				key = "config.bad_tui_color_value"
				args = []any{colorErr.Value, colorErr.Role, path}
			}
			return &cliapp.ExitError{
				Code:        cliapp.ExitUsage,
				Message:     colorErr.Error(),
				MessageKey:  key,
				MessageArgs: args,
			}
		}
		var loadErr *config.LoadError
		code := cliapp.ExitUsage
		key := "config.invalid"
		if errors.As(err, &loadErr) && loadErr.Kind == "security" {
			code = cliapp.ExitPermission
			key = "config.security"
		}
		// Структурный ExitError: NewExitErrorK использует ключ и как format,
		// поэтому для ключей с аргументами корректнее собрать Message явно.
		return &cliapp.ExitError{
			Code:        code,
			Message:     fmt.Sprintf("конфигурация %s отклонена", a.flags.config),
			MessageKey:  key,
			MessageArgs: []any{a.flags.config},
			Cause:       err,
		}
	}
	for _, warning := range warnings {
		a.writeStderr("warning: " + warning + "\n")
	}
	if cmd.Flags().Changed("file") {
		settings.FstabPath = a.flags.file
	}
	if cmd.Flags().Changed("backup-dir") {
		settings.BackupDir = a.flags.backupDir
	}
	if cmd.Flags().Changed("log-level") {
		settings.LogLevel = a.flags.logLevel
	}
	if cmd.Flags().Changed("output") {
		settings.Output = a.flags.output
	}
	// Normalize: значения, не заданные CLI, приходят из config/env/defaults.
	// env-значения валидирует config.validateValues, CLI — validateGlobalFlags,
	// поэтому повторная валидация здесь не нужна.
	if !cmd.Flags().Changed("file") {
		a.flags.file = settings.FstabPath
	}
	if !cmd.Flags().Changed("backup-dir") {
		a.flags.backupDir = settings.BackupDir
	}
	if !cmd.Flags().Changed("log-level") {
		a.flags.logLevel = settings.LogLevel
	}
	if !cmd.Flags().Changed("output") && settings.Output != "" {
		a.flags.output = settings.Output
	}
	a.settings = settings
	return nil
}

// openLog открывает файловый журнал для мутирующей команды (ТЗ §14): журнал
// обязан быть доступен до точки изменения target. Sudo-контекст берётся из
// SUDO_USER/SUDO_UID и включается в события, только когда SUDO_UID непустой
// и числовой. Ошибка открытия маппится на code 3 при отказе в правах,
// иначе 6: target к этому моменту ещё не тронут.
func (a *App) openLog() *cliapp.ExitError {
	sudoUID := a.options.Getenv("SUDO_UID")
	sudoUser := ""
	if sudoUID != "" {
		if _, err := strconv.Atoi(sudoUID); err != nil {
			sudoUID = "" // нечисловой SUDO_UID не доверяем
		} else {
			sudoUser = a.options.Getenv("SUDO_USER")
		}
	}
	log, err := logger.Open(logger.Options{
		File:         a.settings.LogFile,
		Level:        logger.Severity(a.settings.LogLevel),
		MaxSizeMB:    a.settings.LogMaxSizeMB,
		Keep:         a.settings.LogKeep,
		RealUID:      a.options.Getuid(),
		EffectiveUID: a.options.Geteuid(),
		SudoUser:     sudoUser,
		SudoUID:      sudoUID,
		Now:          a.options.Now,
	})
	if err != nil {
		code := cliapp.ExitIO
		if errors.Is(err, os.ErrPermission) {
			code = cliapp.ExitPermission
		}
		return &cliapp.ExitError{
			Code:        code,
			Message:     fmt.Sprintf("открыть журнал %s", a.settings.LogFile),
			MessageKey:  "error.log.open",
			MessageArgs: []any{a.settings.LogFile},
			Cause:       err,
		}
	}
	a.log = log
	return nil
}

// logEvent пишет событие в журнал; ошибка записи игнорируется, чтобы сбой
// журнала после успешного replace не мешал обязательному rollback и не
// заменял первичную ошибку команды. Журнал не открыт (nil) — событие
// пропускается молча: read-only и dry-run команды журнал не создают.
func (a *App) logEvent(event logger.Event) {
	if a.log == nil {
		return
	}
	_ = a.log.Log(event)
}

// format возвращает провалидированный формат вывода. PersistentPreRunE
// гарантирует, что неизвестное значение отклонено раньше, поэтому здесь
// преобразование без повторной проверки.
func (a *App) format() output.Format {
	return output.Format(a.flags.output)
}

// writeStdout публикует обычные данные и machine output; --quiet подавляет
// stdout, но не меняет exit code и не скрывает stderr-диагностику.
func (a *App) writeStdout(data []byte) {
	if a.flags.quiet {
		return
	}
	_, _ = a.options.Stdout.Write(data)
}

// writeStderr публикует диагностику и prompts; --quiet её не скрывает.
func (a *App) writeStderr(text string) {
	_, _ = fmt.Fprint(a.options.Stderr, text)
}

// loadDocument читает target и разбирает его документ для подготовки мутации.
// Чтение выполняется здесь, а transaction повторно читает target и сверяет
// SHA-256, поэтому внешнее изменение файла между preview и записью будет
// обнаружено как конфликт (code 4). Содержимое реального /etc/fstab в тестах
// не читается: target всегда временный файл.
func (a *App) loadDocument(target string) (*models.Document, *cliapp.ExitError) {
	snapshot, err := writer.ReadSnapshot(target)
	if err != nil {
		return nil, ioError("error.io.read_target", "прочитать target %s", target, err)
	}
	return parser.Parse(snapshot.Data), nil
}

// ioError оформляет сбой ввода-вывода как ExitError code 6 с i18n-ключом:
// printExitError переведёт сообщение выбранным языком, русский Message
// остаётся fallback для Error() и логов. MessageArgs совпадают с аргументами
// ключа каталога (перевод форматируется теми же значениями).
func ioError(key, format, target string, cause error) *cliapp.ExitError {
	return &cliapp.ExitError{
		Code:        cliapp.ExitIO,
		Message:     "не удалось " + fmt.Sprintf(format, target),
		MessageKey:  key,
		MessageArgs: []any{target},
		Cause:       cause,
	}
}

// interactive сообщает, допускается ли интерактивный prompt: только при TTY
// и без --yes. --yes никогда не включает интерактивный выбор кандидатов.
func (a *App) interactive() bool {
	return !a.flags.yes && a.options.TTY != nil && a.options.TTY()
}

// confirm реализует общий prompt подтверждения [y/N] для мутаций и rollback.
// Diff и warnings печатаются в stderr до вопроса, чтобы машинный stdout
// оставался чистым; переданные warnings уже отфильтрованы по severity
// вызывающим кодом (previewWarnings — единственная точка фильтрации).
// --yes пропускает prompt после валидации preview; non-TTY без --yes
// отклоняется usage error; формы «да» определяются i18n.AcceptYes выбранного
// языка (ru: д/да/y/yes, en: y/yes); пустой ответ, EOF и любая прочая строка
// трактуются как отказ (default No).
func (a *App) confirm(diff string, warnings []models.Diagnostic) (bool, *cliapp.ExitError) {
	if a.flags.yes {
		return true, nil
	}
	if !a.interactive() {
		return false, cliapp.UsageErrorK(nil, "error.usage.no_tty",
			"подтверждение требует TTY: в неинтерактивном режиме используйте --yes")
	}
	if diff != "" {
		a.writeStderr(diff)
		if !strings.HasSuffix(diff, "\n") {
			a.writeStderr("\n")
		}
	}
	for _, diagnostic := range warnings {
		a.writeStderr(fmt.Sprintf("warning: %s\n", diagnostic.Message))
	}
	a.writeStderr(i18n.T(a.lang, "prompt.apply_changes"))
	answer, err := a.stdin.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, nil
	}
	return i18n.AcceptYes(a.lang, answer), nil
}

// previewWarnings — единственная точка фильтрации warning-диагностики
// candidate: именно они показываются перед prompt согласно ТЗ 11.6;
// info-замечения prompt не блокируют и не дублируются. Возвращённый список
// передаётся в confirm как есть, без повторной проверки severity.
func previewWarnings(report models.Report) []models.Diagnostic {
	warnings := make([]models.Diagnostic, 0)
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Severity == models.SeverityWarning {
			warnings = append(warnings, diagnostic)
		}
	}
	return warnings
}

// contextForHandlers возвращает фоновый контекст запуска команды. Таймауты
// внешних утилит задаёт sysutil.Runner, поэтому здесь отдельный deadline не нужен.
func contextForHandlers() context.Context {
	return context.Background()
}
