// Файл rollback.go реализует ручные операции сервиса: явный backup текущего
// target и ручной rollback target из выбранной резервной копии. Оба метода
// работают только через Dependencies, поэтому тесты подменяют границы отказа
// fake seams. Rollback соблюдает инвариант «selection перед safety backup»:
// неоднозначный или отсутствующий RestoreID завершает команду до создания
// каких-либо копий и до любой записи в target.
package cliapp

import (
	"context"
	"errors"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/diff"
	"fstab-edit/internal/sysutil"
)

// BackupRequest описывает явное создание резервной копии target без его
// изменения. Directory и Prefix переопределяют место и имена файлов; пустые
// значения означают каталог target и backup.DefaultBackupPrefix.
type BackupRequest struct {
	Directory string
	Prefix    string
	DryRun    bool
}

// BackupResult описывает результат команды backup. Backup заполнен только при
// реальном создании; DryRun=true означает, что I/O не выполнялся вовсе. Формат
// вывода определяет Task 4 поверх этих данных.
type BackupResult struct {
	DryRun bool
	Backup backup.Backup
}

// Backup создаёт резервную копию текущего target. Dry-run не выполняет никакого
// I/O — ни чтения target, ни создания файлов — и лишь сообщает DryRun=true.
// Реальное создание идёт через backup.Create с OperationManual и честным
// SourceNotChecked: работоспособность исходника ручная копия не проверяла.
func (s *Service) Backup(target string, request BackupRequest) (BackupResult, *ExitError) {
	result := BackupResult{}
	if request.DryRun {
		result.DryRun = true
		return result, nil
	}
	snapshot, err := s.dependencies.ReadSnapshot(target)
	if err != nil {
		return result, newExitError(ExitIO, err, nil, "прочитать target %s", target)
	}
	created, err := s.dependencies.CreateBackup(snapshot, backup.CreateOptions{
		Directory:          request.Directory,
		Prefix:             request.Prefix,
		Operation:          backup.OperationManual,
		SourceVerification: backup.SourceNotChecked,
	})
	if err != nil {
		return result, newExitError(ExitIO, err, nil, "создать backup target %s", snapshot.Path)
	}
	result.Backup = created
	return result, nil
}

// RollbackRequest описывает ручное восстановление target из backup. Пустой
// RestoreID выбирает newest доступную копию; непустой — точное совпадение ID.
// Directory и Prefix имеют ту же семантику, что и в BackupRequest.
type RollbackRequest struct {
	RestoreID string
	Directory string
	Prefix    string
	DryRun    bool
}

// RollbackResult описывает исход rollback. Selected — проверенная выбранная
// копия; Diff направлен current target → selected backup; SafetyBackup —
// копия пре-restore состояния target, созданная до replace; PostVerification —
// проверка фактического target после записи. Формат вывода определяет Task 4.
type RollbackResult struct {
	DryRun           bool
	Selected         backup.Backup
	Diff             string
	SafetyBackup     backup.Backup
	PostVerification sysutil.Verification
}

// Rollback восстанавливает target из выбранной резервной копии. Порядок
// событий строго фиксирован: select → open selected → read target/backup →
// diff → safety backup → replace → verify target. Dry-run останавливается
// после diff и не создаёт safety backup. Сбой replace возвращает code 6, но
// safety backup уже на диске и target не изменён. Ошибки post verification
// возвращают code 5 без автоматического отката выбранной копии: результат
// сохраняется, а решение о восстановлении принимает пользователь.
func (s *Service) Rollback(ctx context.Context, target string, request RollbackRequest) (RollbackResult, *ExitError) {
	result := RollbackResult{}

	// Selection выполняется до любой записи: отсутствующий или неоднозначный
	// RestoreID обязан завершить команду, не создав safety backup.
	selection, err := s.dependencies.SelectBackup(target, request.Prefix, request.Directory, request.RestoreID)
	if err != nil {
		return result, rollbackSelectionError(err)
	}
	// Open повторно проверяет metadata/hash пары, а не доверяет имени файла.
	opened, err := s.dependencies.OpenBackup(selection.Selected.Path)
	if err != nil {
		result.Selected = selection.Selected
		return result, newExitError(ExitIO, err, nil, "проверить backup %s", selection.Selected.Path)
	}
	result.Selected = opened

	current, err := s.dependencies.ReadSnapshot(target)
	if err != nil {
		return result, newExitError(ExitIO, err, nil, "прочитать target %s", target)
	}
	selected, err := s.dependencies.ReadSnapshot(opened.Path)
	if err != nil {
		return result, newExitError(ExitIO, err, nil, "прочитать backup %s", opened.Path)
	}
	result.Diff = diff.Unified("<current>", "<backup>", current.Data, selected.Data)

	if request.DryRun {
		result.DryRun = true
		return result, nil
	}

	// Safety backup обязан быть создан до replace: при сбое записи прежнее
	// состояние target остаётся восстановимым вручную.
	safety, err := s.dependencies.CreateBackup(current, backup.CreateOptions{
		Directory: request.Directory,
		Prefix:    request.Prefix,
		Operation: backup.OperationBeforeRollback,
		// Safety backup лишь фиксирует байты до restore; их работоспособность
		// на этом этапе не проверялась.
		SourceVerification: backup.SourceNotChecked,
	})
	if err != nil {
		return result, newExitError(ExitIO, err, nil, "создать safety backup target %s", current.Path)
	}
	result.SafetyBackup = safety

	if _, err := s.dependencies.Replace(current.Path, selected.Data); err != nil {
		return result, newExitError(ExitIO, err, nil,
			"заменить target %s резервной копией %s", current.Path, opened.ID)
	}

	verification := s.dependencies.VerifyTarget(ctx, s.runner, current.Path)
	result.PostVerification = verification
	if verification.Report.HasErrors() {
		return result, newExitError(ExitExternal, nil, nil,
			"post-check target %s после восстановления завершился с severity=error", current.Path)
	}
	return result, nil
}

// rollbackSelectionError отображает ошибки выбора копии: отсутствующий
// пользовательский RestoreID — на code 1, а неоднозначность ID или сбой scan
// — на code 6, потому что это внутреннее состояние backup directory.
func rollbackSelectionError(err error) *ExitError {
	if errors.Is(err, backup.ErrBackupNotFound) {
		return ValidationError(err, "резервная копия не найдена")
	}
	return newExitError(ExitIO, err, nil, "выбрать резервную копию")
}
