// Файл mutation.go реализует команды add, edit, delete, comment и uncomment.
// Все пять проходят единый pipeline: валидация флагов → preview через
// cliapp.PrepareMutation → (необязательный интерактивный выбор при
// неоднозначности) → блокировка при severity=error → no-op → dry-run →
// подтверждение [y/N] → transaction. Порядок гарантирует, что no-op не
// создаёт backup/lock/write, а --yes не обходит validation и ambiguity.
package cmd

import (
	"errors"

	"github.com/spf13/cobra"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/editor"
	"fstab-edit/internal/output"
	"fstab-edit/internal/transaction"
)

// buildAddCommand собирает `fstab-edit add`. Позиционные аргументы не
// принимаются: все поля новой записи задаются явными флагами по ТЗ 11.4.
// Обязательные --device, --mount, --type, --options проверяются по
// Flags().Changed и непустому значению; dump/pass по умолчанию "0".
func (a *App) buildAddCommand() *cobra.Command {
	var device, mount, fsType, options, dump, pass, comment string
	command := &cobra.Command{
		Use:   "add",
		Short: "добавить новую запись fstab",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Проверка в фиксированном порядке: сообщение об ошибке
			// детерминировано и не зависит от порядка обхода map.
			required := []struct {
				name  string
				value string
			}{
				{"--device", device},
				{"--mount", mount},
				{"--type", fsType},
				{"--options", options},
			}
			for _, field := range required {
				if !cmd.Flags().Changed(field.name[2:]) || field.value == "" {
					return cliapp.UsageError(nil, "add требует непустой флаг %s", field.name)
				}
			}
			newEntry := editor.NewEntry{
				Source:     device,
				MountPoint: mount,
				FSType:     fsType,
				Options:    options,
				Dump:       dump,
				Pass:       pass,
			}
			if cmd.Flags().Changed("comment") {
				newEntry.Comment = &comment
			}
			return a.executeMutation(cliapp.MutationRequest{
				Kind:     cliapp.MutationAdd,
				NewEntry: newEntry,
			})
		},
	}
	flags := command.Flags()
	flags.StringVar(&device, "device", "", "источник записи (UUID=, LABEL=, /dev/...)")
	flags.StringVar(&mount, "mount", "", "точка монтирования")
	flags.StringVar(&fsType, "type", "", "тип файловой системы")
	flags.StringVar(&options, "options", "", "список опций монтирования")
	flags.StringVar(&dump, "dump", "0", "поле dump")
	flags.StringVar(&pass, "pass", "0", "поле pass")
	flags.StringVar(&comment, "comment", "", "comment block перед записью")
	return command
}

// buildEditCommand собирает `fstab-edit edit`. Селектор — positional, --line
// или --mount-point; --device здесь изменяет поле источника (ТЗ 11.4).
// Требуется хотя бы один изменяющий флаг; --comment несовместим с
// --clear-comment; --unset-dump/--unset-pass удаляют optional-поля.
func (a *App) buildEditCommand() *cobra.Command {
	var positional string
	var line, mount, device, fsType, options, dump, pass, comment string
	var clearComment, unsetDump, unsetPass bool
	command := &cobra.Command{
		Use:   "edit",
		Short: "изменить выбранные поля записи fstab",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				positional = args[0]
			}
			selector, exitErr := cliapp.SelectorFromValues(positional, line, mount, "", true)
			if exitErr != nil {
				return exitErr
			}
			if cmd.Flags().Changed("comment") && clearComment {
				return cliapp.UsageError(nil, "флаги --comment и --clear-comment несовместимы: выберите один")
			}
			changed := cmd.Flags().Changed("device") || cmd.Flags().Changed("mount") ||
				cmd.Flags().Changed("type") || cmd.Flags().Changed("options") ||
				cmd.Flags().Changed("dump") || cmd.Flags().Changed("pass") ||
				cmd.Flags().Changed("comment") || clearComment || unsetDump || unsetPass
			if !changed {
				return cliapp.UsageError(nil, "edit требует хотя бы один изменяющий флаг: --device, --mount, --type, --options, --dump, --pass, --comment, --clear-comment, --unset-dump, --unset-pass")
			}
			changes := editor.Changes{
				UnsetDump:    unsetDump,
				UnsetPass:    unsetPass,
				ClearComment: clearComment,
			}
			setString := func(name, value string) *string {
				if cmd.Flags().Changed(name) {
					return &value
				}
				return nil
			}
			changes.Source = setString("device", device)
			changes.MountPoint = setString("mount", mount)
			changes.FSType = setString("type", fsType)
			changes.Options = setString("options", options)
			changes.Dump = setString("dump", dump)
			changes.Pass = setString("pass", pass)
			if cmd.Flags().Changed("comment") {
				changes.Comment = &comment
			}
			return a.executeMutation(cliapp.MutationRequest{
				Kind:     cliapp.MutationEdit,
				Selector: selector,
				Changes:  changes,
			})
		},
	}
	flags := command.Flags()
	flags.StringVar(&line, "line", "", "номер физической строки записи")
	flags.StringVar(&mount, "mount-point", "", "полное decoded значение mount point")
	flags.StringVar(&device, "device", "", "новое значение источника записи")
	flags.StringVar(&fsType, "type", "", "новый тип файловой системы")
	flags.StringVar(&options, "options", "", "новый список опций монтирования")
	flags.StringVar(&dump, "dump", "", "новое поле dump")
	flags.StringVar(&pass, "pass", "", "новое поле pass")
	flags.StringVar(&comment, "comment", "", "заменить comment block записи")
	flags.BoolVar(&clearComment, "clear-comment", false, "удалить comment block записи")
	flags.BoolVar(&unsetDump, "unset-dump", false, "удалить optional-поле dump")
	flags.BoolVar(&unsetPass, "unset-pass", false, "удалить optional-поле pass")
	return command
}

// buildDeleteCommand, buildCommentCommand и buildUncommentCommand отличаются
// только видом мутации, поэтому собираются общим helper'ом: селектор —
// positional, --line, --mount-point или --device, ровно один способ.
func (a *App) buildDeleteCommand() *cobra.Command {
	return a.buildSelectorMutationCommand("delete", "удалить запись fstab", cliapp.MutationDelete)
}

func (a *App) buildCommentCommand() *cobra.Command {
	return a.buildSelectorMutationCommand("comment", "закомментировать активную запись fstab", cliapp.MutationComment)
}

func (a *App) buildUncommentCommand() *cobra.Command {
	return a.buildSelectorMutationCommand("uncomment", "раскомментировать запись fstab", cliapp.MutationUncomment)
}

// buildSelectorMutationCommand собирает команду мутации с обязательным
// селектором записи и без собственных полей.
func (a *App) buildSelectorMutationCommand(use, short string, kind cliapp.MutationKind) *cobra.Command {
	var line, mount, device string
	command := &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) == 1 {
				positional = args[0]
			}
			selector, exitErr := cliapp.SelectorFromValues(positional, line, mount, device, true)
			if exitErr != nil {
				return exitErr
			}
			return a.executeMutation(cliapp.MutationRequest{Kind: kind, Selector: selector})
		},
	}
	flags := command.Flags()
	flags.StringVar(&line, "line", "", "номер физической строки записи")
	flags.StringVar(&mount, "mount-point", "", "полное decoded значение mount point")
	flags.StringVar(&device, "device", "", "полное decoded значение источника записи")
	return command
}

// executeMutation прогоняет единый pipeline мутации. Ошибки селектора,
// неоднозначности и валидации возвращаются как *cliapp.ExitError; App.Execute
// превращает их в exit code и stderr-сообщение со списком кандидатов.
func (a *App) executeMutation(request cliapp.MutationRequest) error {
	target := a.flags.file
	document, exitErr := a.loadDocument(target)
	if exitErr != nil {
		return exitErr
	}
	preview, exitErr := cliapp.PrepareMutation(document, request)
	if exitErr != nil && exitErr.Code == cliapp.ExitAmbiguous {
		// При TTY и без --yes пользователь может выбрать одну из candidate
		// lines; --yes намеренно не доходит сюда как интерактивный режим и
		// произвольный кандидат никогда не выбирается.
		if picked, ok := a.chooseCandidate(exitErr.Candidates); ok {
			request.Selector = editor.ByLine(picked)
			preview, exitErr = cliapp.PrepareMutation(document, request)
		}
	}
	if exitErr != nil {
		return exitErr
	}
	return a.finishMutation(target, preview)
}

// chooseCandidate предлагает интерактивный выбор из candidate lines. Возвращает
// выбранную строку только когда режим интерактивен и chooser вернул значение
// из списка кандидатов; иначе (false, nil) — вызывающий код сохраняет code 8.
func (a *App) chooseCandidate(candidates []int) (int, bool) {
	if !a.interactive() || a.options.Chooser == nil || len(candidates) == 0 {
		return 0, false
	}
	picked, err := a.options.Chooser.Choose(candidates)
	if err != nil {
		return 0, false
	}
	for _, candidate := range candidates {
		if candidate == picked {
			return picked, true
		}
	}
	return 0, false
}

// finishMutation завершает pipeline после успешной подготовки preview:
// severity=error блокирует сохранение независимо от --yes, no-op завершается
// без backup/lock/write, dry-run публикует preview, а реальная мутация
// проходит подтверждение и transaction с публикацией structured результата.
func (a *App) finishMutation(target string, preview *cliapp.MutationPreview) error {
	// Валидация candidate строго до подтверждения: --yes её не обходит.
	if preview.Report.HasErrors() {
		a.renderMutation(preview, target, nil)
		return cliapp.ValidationError(nil,
			"кандидат не прошёл внутреннюю проверку (severity=error), изменения не сохранены")
	}
	if !preview.Changed {
		a.renderMutation(preview, target, nil)
		a.writeStderr("изменений нет: target не изменён, backup и lock не создавались\n")
		return nil
	}
	if a.flags.dryRun {
		a.renderMutation(preview, target, nil)
		return nil
	}
	proceed, exitErr := a.confirm(preview.Diff, previewWarnings(preview.Report))
	if exitErr != nil {
		return exitErr
	}
	if !proceed {
		a.writeStderr("операция отменена пользователем: target не изменён\n")
		return nil
	}
	executed, runErr := a.service.RunMutation(contextForHandlers(), target, *preview,
		transaction.Options{Runner: a.options.Runner})
	a.renderMutation(preview, target, &executed)
	if runErr != nil {
		// Код ошибки транзакции имеет приоритет над прочими диагностиками:
		// critical rollback failure (7) никогда не перекрывается результатом.
		var mutationErr *cliapp.ExitError
		if errors.As(runErr, &mutationErr) {
			return mutationErr
		}
		return ioError("сохранить изменения", target, runErr)
	}
	return nil
}

// renderMutation публикует structured результат мутации на stdout одним
// документом; executed не-nil после реальной transaction и добавляет backup_id
// и rollback-результат в schema v1.
func (a *App) renderMutation(preview *cliapp.MutationPreview, target string, executed *transaction.TransactionResult) {
	data, err := output.RenderMutation(preview, target, a.flags.dryRun, executed, a.format())
	if err != nil {
		a.writeStderr("не удалось сформировать вывод результата: " + err.Error() + "\n")
		return
	}
	a.writeStdout(data)
}
