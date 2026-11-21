// Файл tui.go реализует команду `fstab-edit tui`: требует интерактивный
// терминал, собирает tui.Options из резолвленных настроек (PersistentPreRunE
// заполнил a.settings и нормализовал a.flags) и передаёт управление TUI
// (production tui.Run или инъектируемому launcher'у из AppOptions).
package cmd

import (
	"github.com/spf13/cobra"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/i18n"
	"fstab-edit/tui"
)

// buildTuiCommand собирает `fstab-edit tui`: резолвит настройки (как остальные
// команды, через a.settings), требует TTY и передаёт управление tui.Run.
// Команда read-only до подтверждения сохранения внутри TUI: файловый журнал
// открывает не PreRunE, а сам TUI перед транзакцией (ТЗ §14).
func (a *App) buildTuiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "интерактивный TUI-редактор fstab",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.launchTUI()
		},
	}
}

// launchTUI проверяет TTY, собирает tui.Options из резолвленных настроек и
// запускает TUI. Без интерактивного терминала — usage error (exit 2) с
// локализованным сообщением tui.no_tty; сбой запуска — exit 6 (io error).
func (a *App) launchTUI() error {
	if a.options.TTY == nil || !a.options.TTY() {
		return cliapp.UsageError(nil, i18n.Tf(a.lang, "tui.no_tty"))
	}
	opts := tui.Options{
		Target: a.flags.file,
		Lang:   a.lang,
		Theme:  a.settings.TUITheme,
		Backup: tui.BackupSettings{
			Dir:    a.settings.BackupDir,
			Prefix: a.settings.BackupPrefix,
			Keep:   a.settings.BackupKeep,
		},
		Log: tui.LogSettings{
			File:      a.settings.LogFile,
			Level:     a.settings.LogLevel,
			MaxSizeMB: a.settings.LogMaxSizeMB,
			Keep:      a.settings.LogKeep,
		},
		Runner: a.options.Runner,
	}
	launcher := a.options.TuiLauncher
	if launcher == nil {
		launcher = tui.Run
	}
	if err := launcher(opts); err != nil {
		// Сбой запуска TUI — io error (exit 6): bubbletea возвращает
		// нетипизированную ошибку терминала, она подставляется в перевод ключа.
		return &cliapp.ExitError{
			Code:    cliapp.ExitIO,
			Message: i18n.Tf(a.lang, "error.tui.launch", err.Error()),
			Cause:   err,
		}
	}
	return nil
}
