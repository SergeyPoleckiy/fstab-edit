// Файл backup.go реализует команду backup: явное создание резервной копии
// target без его изменения и без mutation prompt (ТЗ 10.4).
package cmd

import (
	"github.com/spf13/cobra"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/output"
)

// buildBackupCommand собирает `fstab-edit backup`. Dry-run не выполняет
// никакого I/O и лишь публикует структуру результата с dry_run=true.
func (a *App) buildBackupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "backup",
		Short: "создать ручную резервную копию target без его изменения",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, exitErr := a.service.Backup(a.flags.file, cliapp.BackupRequest{
				Directory: a.flags.backupDir,
				DryRun:    a.flags.dryRun,
			})
			if exitErr != nil {
				return exitErr
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
