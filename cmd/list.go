// Файл list.go реализует команду list: read-only просмотр активных и
// закомментированных записей target. Команда не изменяет target и не
// обращается к внешним утилитам.
package cmd

import (
	"github.com/spf13/cobra"

	"fstab-edit/internal/output"
)

// buildListCommand собирает `fstab-edit list`: записи на stdout в выбранном
// формате, а диагностики в table-режиме — на stderr, чтобы машинный вывод
// одного документа и человекочитаемая таблица не смешивались.
func (a *App) buildListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "показать активные и закомментированные записи fstab",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, exitErr := a.service.List(a.flags.file)
			if exitErr != nil {
				return exitErr
			}
			data, err := output.RenderList(result, a.format())
			if err != nil {
				return cliappUsageFromRender(err)
			}
			a.writeStdout(data)
			// В table-режиме диагностики — сопровождающая диагностика: она
			// идёт в stderr. В machine-форматах диагностики уже внутри
			// единственного JSON-документа и никуда больше не дублируются.
			if a.format() == output.FormatTable && len(result.Diagnostics) > 0 {
				diagnostics, err := output.RenderDiagnostics(result.Diagnostics, output.FormatTable)
				if err == nil {
					a.writeStderr(string(diagnostics))
				}
			}
			return nil
		},
	}
}
