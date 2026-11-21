package tui

import (
	"context"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/logger"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/transaction"
	"fstab-edit/internal/writer"
)

// Deps содержит границы отказа TUI; тесты подменяют их fake-реализациями.
// Зеркало cliapp.Dependencies плюс журнал: TUI не дублирует доменную логику,
// а получает те же seams, что и CLI.
type Deps struct {
	// ReadSnapshot безопасно читает target (байты + SHA-256 + canonical path).
	ReadSnapshot func(string) (writer.Snapshot, error)
	// ExecuteTransaction выполняет одну транзакцию сохранения candidate.
	ExecuteTransaction func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error)
	// ListBackups возвращает backups newest-first для истории.
	ListBackups func(target, prefix, directory string) ([]backup.Backup, error)
	// OpenBackup проверяет metadata/hash выбранной копии.
	OpenBackup func(string) (backup.Backup, error)
	// Replace атомарно заменяет target (восстановление из истории).
	Replace func(string, []byte) (writer.ReplaceResult, error)
	// VerifyTarget проверяет фактический target после restore.
	VerifyTarget func(context.Context, sysutil.CommandRunner, string) sysutil.Verification
	// OpenLog открывает журнал перед сохранением; ошибка блокирует сохранение.
	OpenLog func() (*logger.Logger, error)
}

// ProductionDeps заполняет границы отказа production-реализациями ядра.
func ProductionDeps(logOpts logger.Options) Deps {
	return Deps{
		ReadSnapshot:       writer.ReadSnapshot,
		ExecuteTransaction: transaction.Execute,
		ListBackups:        backup.List,
		OpenBackup:         backup.Open,
		Replace:            writer.Replace,
		VerifyTarget: func(ctx context.Context, runner sysutil.CommandRunner, path string) sysutil.Verification {
			return sysutil.NewFindmntVerifier(runner).Verify(ctx, path)
		},
		OpenLog: func() (*logger.Logger, error) { return logger.Open(logOpts) },
	}
}
