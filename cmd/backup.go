// Файл backup.go реализует команду backup: явное создание резервной копии
// target без его изменения и без mutation prompt (ТЗ 10.4).
package cmd

import (
	"github.com/spf13/cobra"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/logger"
	"fstab-edit/internal/output"
)

// buildBackupCommand собирает `fstab-edit backup`. Dry-run не выполняет
// никакого I/O и лишь публикует структуру результата с dry_run=true; журнал
// для dry-run не открывается (PersistentPreRunE), поэтому событие пишется
// только реальному созданию копии.
func (a *App) buildBackupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "backup",
		Short: "создать ручную резервную копию target без его изменения",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			start := a.options.Now()
			result, exitErr := a.service.Backup(a.flags.file, cliapp.BackupRequest{
				Directory: a.flags.backupDir,
				Prefix:    a.settings.BackupPrefix,
				DryRun:    a.flags.dryRun,
			})
			duration := a.options.Now().Sub(start)
			if exitErr != nil {
				a.logEvent(logger.Event{
					Severity:   logger.SeverityError,
					Operation:  logger.OperationBackup,
					Target:     a.flags.file,
					Result:     logger.ResultError,
					ErrorCode:  exitErr.Code,
					DurationMs: duration.Milliseconds(),
				})
				return exitErr
			}
			if !result.DryRun {
				a.logEvent(logger.Event{
					Severity:   logger.SeverityInfo,
					Operation:  logger.OperationBackup,
					Target:     a.flags.file,
					BackupID:   result.Backup.ID,
					Result:     logger.ResultSuccess,
					DurationMs: duration.Milliseconds(),
				})
			}
			data, err := output.RenderBackup(result.Backup, result.DryRun, a.format())
			if err != nil {
				return cliappUsageFromRender(err)
			}
			a.writeStdout(data)
			return nil
		},
	}
}
