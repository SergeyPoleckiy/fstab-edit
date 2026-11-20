// Файл validate.go реализует команды validate и verify-mount: проверку target
// внутренним валидатором (и внешним findmnt, если не --offline) и чистую
// статическую проверку findmnt. Обе команды read-only: target не изменяется.
package cmd

import (
	"github.com/spf13/cobra"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/output"
	"fstab-edit/pkg/models"
)

// cliappUsageFromRender оформляет сбой сериализации вывода как usage error:
// формат уже прошёл валидацию, поэтому ошибка здесь означает некорректные
// данные результата, а не опечатку пользователя; code 2 сохраняет класс
// «команда не выполнена из-за входных данных CLI».
func cliappUsageFromRender(cause error) *cliapp.ExitError {
	return cliapp.UsageError(cause, "сформировать вывод результата")
}

// buildValidateCommand собирает `fstab-edit validate`. Диагностики являются
// основным результатом команды и публикуются на stdout в выбранном формате;
// exit code 0 допускает warnings, severity=error даёт code 1 через ExitError
// от Service. Offline-режим не вызывает runner вовсе.
func (a *App) buildValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "проверить документ fstab внутренним валидатором и findmnt",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, exitErr := a.service.Validate(contextForHandlers(), a.flags.file, a.flags.offline)
			data, err := output.RenderDiagnostics(result.Report.Diagnostics, a.format())
			if err != nil {
				return cliappUsageFromRender(err)
			}
			a.writeStdout(data)
			if exitErr != nil {
				return exitErr
			}
			return nil
		},
	}
}

// buildVerifyMountCommand собирает `fstab-edit verify-mount`: всегда
// статический findmnt --verify по фактическим байтам target (флаг --offline
// её не отключает). Предупреждения допускаются, errors дают code 1.
func (a *App) buildVerifyMountCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify-mount",
		Short: "выполнить статический findmnt --verify для target",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			verification, exitErr := a.service.VerifyMount(contextForHandlers(), a.flags.file)
			diagnostics := verification.Report.Diagnostics
			if diagnostics == nil {
				diagnostics = []models.Diagnostic{}
			}
			data, err := output.RenderDiagnostics(diagnostics, a.format())
			if err != nil {
				return cliappUsageFromRender(err)
			}
			a.writeStdout(data)
			if exitErr != nil {
				return exitErr
			}
			return nil
		},
	}
}
