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
	"strings"

	"github.com/spf13/cobra"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/output"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/writer"
	"fstab-edit/pkg/models"
)

// defaultTarget — production target по умолчанию. Флаг --file переопределяет
// его; справка и версия не читают target вовсе.
const defaultTarget = "/etc/fstab"

// logLevels — допустимые значения флага --log-level. Logger реализуется
// следующим блоком, но значение валидируется уже сейчас, чтобы опечатка
// (--log-level=trace) отклонялась usage error, а не молчаливо игнорировалась.
var logLevels = map[string]struct{}{"debug": {}, "info": {}, "warn": {}, "error": {}}

// languages — допустимые значения флага --lang. Полные каталоги RU/EN
// реализуются следующим блоком; текущие сообщения — на русском.
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
}

// App связывает Cobra root command, глобальные флаги и доменный Service.
// Один экземпляр обслуживает один запуск Execute; одновременных вызовов нет.
type App struct {
	options AppOptions
	service *cliapp.Service
	flags   globalFlags
	stdin   *bufio.Reader
	root    *cobra.Command
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
		stdin:   bufio.NewReader(options.Stdin),
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
// code процесса. Cobra-ошибки (неизвестный флаг, некорректные аргументы)
// отображаются на code 2 с русским сообщением; *cliapp.ExitError сохраняет
// свой код и публикует сообщение со списком кандидатов в stderr.
func (a *App) Execute(args []string) int {
	a.root.SetArgs(args)
	err := a.root.Execute()
	if err == nil {
		return cliapp.ExitSuccess
	}
	var exitErr *cliapp.ExitError
	if errors.As(err, &exitErr) {
		a.printExitError(exitErr)
		return exitErr.Code
	}
	// Ошибки разбора флагов и аргументов Cobra не типизированы доменом;
	// для пользователя это usage error, сообщение сохраняется как есть.
	// Writers App — память или потоки процесса; игнорирование ошибки записи
	// здесь допустимо, как и в пакете output.
	_, _ = fmt.Fprintf(a.options.Stderr, "некорректный флаг или аргументы: %v\n", err)
	return cliapp.ExitUsage
}

// printExitError публикует диагностику ExitError в stderr: сообщение с
// сохранённой причиной и, при неоднозначности, список candidate lines.
// Prompt и diff handler'ы уже напечатаны к этому моменту отдельно.
func (a *App) printExitError(exitErr *cliapp.ExitError) {
	_, _ = fmt.Fprintln(a.options.Stderr, exitErr.Error())
	if len(exitErr.Candidates) > 0 {
		lines := make([]string, len(exitErr.Candidates))
		for i, line := range exitErr.Candidates {
			lines[i] = fmt.Sprintf("%d", line)
		}
		_, _ = fmt.Fprintf(a.options.Stderr, "кандидаты: строки %s\n", strings.Join(lines, ", "))
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
			return a.validateGlobalFlags()
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
	flags.StringVar(&a.flags.lang, "lang", "ru", "язык сообщений: ru, en")
	flags.StringVar(&a.flags.config, "config", "", "путь к YAML-конфигурации")

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
	)
	return root
}

// validateGlobalFlags отклоняет некорректные комбинации глобальных флагов
// до command handler: формат вывода, уровень журнала, язык и конфликт
// verbose/quiet. Logger, config loader и полные переводы — следующий блок,
// поэтому --log-level/--lang/--config здесь только валидируются.
func (a *App) validateGlobalFlags() error {
	switch output.Format(a.flags.output) {
	case output.FormatTable, output.FormatJSON, output.FormatCSV, output.FormatTSV:
	default:
		return cliapp.UsageError(nil, "неизвестный формат вывода %q: допустимы table, json, csv, tsv", a.flags.output)
	}
	if _, ok := logLevels[a.flags.logLevel]; !ok {
		return cliapp.UsageError(nil, "некорректный --log-level %q: допустимы debug, info, warn, error", a.flags.logLevel)
	}
	if _, ok := languages[a.flags.lang]; !ok {
		return cliapp.UsageError(nil, "некорректный --lang %q: допустимы ru, en", a.flags.lang)
	}
	if a.flags.verbose && a.flags.quiet {
		return cliapp.UsageError(nil, "флаги --verbose и --quiet несовместимы: выберите один режим вывода")
	}
	return nil
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
		return nil, ioError("прочитать target", target, err)
	}
	return parser.Parse(snapshot.Data), nil
}

// ioError оформляет сбой ввода-вывода как ExitError code 6 с сохранённой
// причиной для логирования и разбора через errors.Is/As.
func ioError(action, target string, cause error) *cliapp.ExitError {
	return &cliapp.ExitError{
		Code:    cliapp.ExitIO,
		Message: fmt.Sprintf("не удалось %s %s", action, target),
		Cause:   cause,
	}
}

// interactive сообщает, допускается ли интерактивный prompt: только при TTY
// и без --yes. --yes никогда не включает интерактивный выбор кандидатов.
func (a *App) interactive() bool {
	return !a.flags.yes && a.options.TTY != nil && a.options.TTY()
}

// confirm реализует общий prompt подтверждения [y/N] для мутаций и rollback.
// Diff и warnings печатаются в stderr до вопроса при любом режиме, чтобы
// машинный stdout оставался чистым. --yes пропускает prompt после валидации
// preview; non-TTY без --yes отклоняется usage error; пустой ответ, EOF и
// любое значение кроме y/yes трактуются как отказ (default No).
func (a *App) confirm(diff string, warnings []models.Diagnostic) (bool, *cliapp.ExitError) {
	if a.flags.yes {
		return true, nil
	}
	if !a.interactive() {
		return false, cliapp.UsageError(nil,
			"подтверждение требует TTY: в неинтерактивном режиме используйте --yes")
	}
	if diff != "" {
		a.writeStderr(diff)
		if !strings.HasSuffix(diff, "\n") {
			a.writeStderr("\n")
		}
	}
	for _, diagnostic := range warnings {
		if diagnostic.Severity == models.SeverityWarning {
			a.writeStderr(fmt.Sprintf("warning: %s\n", diagnostic.Message))
		}
	}
	a.writeStderr("Применить изменения? [y/N] ")
	answer, err := a.stdin.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, nil
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// previewWarnings отбирает warning-диагностики candidate: именно они показываются
// перед prompt согласно ТЗ 11.6; info-замечения prompt не блокируют и не дублируются.
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
