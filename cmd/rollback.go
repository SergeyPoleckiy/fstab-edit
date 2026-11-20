// Файл rollback.go реализует команду rollback: восстановление target из
// выбранной резервной копии. Подтверждение строится на read-only dry-run
// проходе Service: он выбирает и проверяет копию, строит diff, но не создаёт
// safety backup и не пишет target. После подтверждения выполняется реальный
// проход, который повторяет selection до создания safety copy (инвариант
// ТЗ 10.5 «выбор копии до safety backup» сохраняется в обоих проходах).
package cmd

import (
	"github.com/spf13/cobra"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/output"
)

// buildRollbackCommand собирает `fstab-edit rollback`. Источник restore ID —
// ровно один: позиционный аргумент или --restore; пустой ID выбирает newest.
// Неоднозначность или отсутствие копии завершают команду до любой записи.
func (a *App) buildRollbackCommand() *cobra.Command {
	var restoreID string
	command := &cobra.Command{
		Use:   "rollback [backup-id]",
		Short: "восстановить target из выбранной резервной копии",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			positional := ""
			if len(args) == 1 {
				positional = args[0]
			}
			if positional != "" && restoreID != "" {
				return cliapp.UsageError(nil,
					"источник restore ID задан дважды: используйте либо позиционный аргумент, либо --restore")
			}
			if positional != "" {
				restoreID = positional
			}
			return a.executeRollback(restoreID)
		},
	}
	command.Flags().StringVar(&restoreID, "restore", "", "restore ID резервной копии (по умолчанию — newest)")
	return command
}

// executeRollback выполняет ручной откат. Read-only проход с DryRun=true
// публикует diff для подтверждения; реальный проход создаёт safety backup
// только после подтверждения. Structured результат публикуется один раз —
// по итогам реального прохода (или единственного dry-run прохода).
func (a *App) executeRollback(restoreID string) error {
	target := a.flags.file
	request := cliapp.RollbackRequest{
		RestoreID: restoreID,
		Directory: a.flags.backupDir,
	}
	// Первый проход всегда read-only: он выбирает и проверяет копию и
	// строит diff для подтверждения, но не создаёт safety backup и не
	// пишет target независимо от флага --dry-run.
	previewRequest := request
	previewRequest.DryRun = true
	preview, exitErr := a.service.Rollback(contextForHandlers(), target, previewRequest)
	if exitErr != nil {
		return exitErr
	}
	if a.flags.dryRun {
		a.renderRollback(preview, target)
		return nil
	}
	proceed, exitErr := a.confirm(preview.Diff, nil)
	if exitErr != nil {
		return exitErr
	}
	if !proceed {
		a.writeStderr("откат отменён пользователем: target не изменён\n")
		return nil
	}
	result, exitErr := a.service.Rollback(contextForHandlers(), target, request)
	a.renderRollback(result, target)
	if exitErr != nil {
		return exitErr
	}
	return nil
}

// renderRollback публикует structured результат rollback на stdout одним
// документом: выбранная и safety backup, dry-run и post verification.
func (a *App) renderRollback(result cliapp.RollbackResult, target string) {
	info := output.RollbackInfo{
		SelectedBackupID: result.Selected.ID,
		SafetyBackupID:   result.SafetyBackup.ID,
		DryRun:           result.DryRun,
	}
	if result.PostVerification.Report.Diagnostics != nil {
		info.PostVerification = result.PostVerification.Report
	}
	data, err := output.RenderRollback(info, a.format())
	if err != nil {
		a.writeStderr("не удалось сформировать вывод результата: " + err.Error() + "\n")
		return
	}
	a.writeStdout(data)
}
