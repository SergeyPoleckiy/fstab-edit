// Package transaction оркестрирует одну безопасную запись fstab. Пакет расположен
// отдельно от writer и backup, потому что backup уже зависит от writer: доменной
// оркестрации разрешено использовать оба пакета, а циклическая зависимость
// запрещена. Task 1 реализует только входную модель, no-op detection и внутреннюю
// проверку candidate; lock, backup, atomic replace и rollback подключаются позднее.
package transaction

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/validator"
	"fstab-edit/internal/writer"
	"fstab-edit/pkg/models"
)

// Plan описывает один запрос на сохранение готового candidate. Candidate уже
// сериализован вызывающим доменным кодом; transaction не переформатирует bytes.
type Plan struct {
	// TargetPath — существующий regular fstab file, обычно переопределённый --file
	// в тестах и будущем CLI. Реальный путь по умолчанию выбирает вызывающий слой.
	TargetPath string
	// Candidate содержит новые bytes целиком. Nil slice недопустим, чтобы случайный
	// zero-value Plan не интерпретировался как пустой файл.
	Candidate []byte
	// Operation фиксируется в metadata backup текущей transaction.
	Operation backup.Operation
	// BackupDirectory переопределяет каталог backup; пустое значение означает
	// каталог target и должно совпадать с семантикой backup.Create.
	BackupDirectory string
	// BackupPrefix переопределяет имя файлов backup; пустое значение означает
	// backup.DefaultBackupPrefix.
	BackupPrefix string
	// RotationKeep ограничивает количество newest backups после успешной записи;
	// неположительное значение позже нормализуется backup.DefaultKeepBackups.
	RotationKeep int
}

// Options содержит зависимости и ограничения одной transaction. Runner обязателен
// для changed path, потому что findmnt является обязательной частью сохранения.
type Options struct {
	// Runner запускает findmnt без shell; unit tests передают fake runner.
	Runner sysutil.CommandRunner
	// LockTimeout ограничивает ожидание эксклюзивного lock; 0 использует default.
	LockTimeout time.Duration
	// RotationProtectedIDs защищает выбранные backups от ротации. Сам backup текущей
	// transaction будет добавлен transaction отдельно и не должен здесь дублироваться.
	RotationProtectedIDs []string
	// BackupDirectory, BackupPrefix и RotationKeep переносят backup-настройки,
	// резолвленные CLI-слоем (config/env/флаги), в transaction, когда Plan —
	// построенный доменным слоем из preview — не задаёт их явно. Plan имеет
	// приоритет: значения из Options используются только как fallback.
	BackupDirectory string
	BackupPrefix    string
	RotationKeep    int
}

// RecoveryPaths указывает файлы, которые нужны для ручного восстановления после
// критического сбоя. Log остаётся пустым до появления обязательного logger.
type RecoveryPaths struct {
	Target   string
	Backup   string
	Metadata string
	Log      string
}

// TransactionResult описывает фактический исход операции даже при ошибке. Ошибка
// передаётся отдельным *TransactionError, чтобы caller мог отличить изменённый
// target от изменённого только частично состояния.
type TransactionResult struct {
	// Changed false означает, что candidate байтово совпал с target и lock/backup/
	// findmnt/write не выполнялись.
	Changed bool
	// TargetPath — canonical target path, использованный transaction.
	TargetPath string
	// CandidateSHA256 фиксирует ожидаемые bytes независимо от исхода replace.
	CandidateSHA256 [sha256.Size]byte
	// InternalReport содержит внутреннюю проверку candidate до lock. Для no-op это
	// намеренно пустой non-nil report: ТЗ завершает неизменённую операцию раньше.
	InternalReport models.Report
	// PreVerification содержит internal+external проверку private candidate.
	PreVerification sysutil.Verification
	// PostVerification содержит проверку фактического target после replace.
	PostVerification sysutil.Verification
	// Backup описывает резервную копию текущей transaction, если она была создана.
	Backup backup.Backup
	// RolledBack true означает, что после сбоя target был восстановлен из Backup.
	RolledBack bool
	// RollbackError сохраняет причину неуспешной единственной попытки rollback.
	RollbackError error
	// Recovery заполняется по мере появления известных путей.
	Recovery RecoveryPaths
	// Rotation описывает ротацию после полного успеха; slices всегда non-nil.
	Rotation backup.RotationResult
}

// FailureKind — стабильный machine-readable класс ошибки для будущих CLI exit codes.
// Значения не локализуются и не меняются без совместимого обновления CLI mapping.
type FailureKind string

// Классы сбоев transaction. Rollback failure имеет наивысший приоритет в CLI.
const (
	FailureValidation FailureKind = "validation"
	FailureConflict   FailureKind = "conflict"
	FailureExternal   FailureKind = "external"
	FailureIO         FailureKind = "io"
	FailureRollback   FailureKind = "rollback"
	FailureRotation   FailureKind = "rotation_after_success"
)

// Sentinel errors представляют классы transaction failures. Конкретная причина
// сохраняется в cause, поэтому errors.Is работает и на sentinel, и на I/O error.
var (
	ErrTransactionValidation = errors.New("candidate не прошёл внутреннюю проверку")
	ErrTransactionConflict   = errors.New("target недоступен, изменился или lock недоступен")
	ErrTransactionExternal   = errors.New("внешняя проверка завершилась ошибкой")
	ErrTransactionIO         = errors.New("I/O, backup или write завершились ошибкой")
	ErrTransactionRollback   = errors.New("automatic rollback не восстановил target")
	ErrRotationAfterSuccess  = errors.New("transaction записана успешно, но rotation завершился ошибкой")
)

// TransactionError связывает доменный класс, первичную причину и recovery paths.
// Тип возвращается по указателю, чтобы errors.As мог отличить его от обычных I/O.
type TransactionError struct {
	// Kind определяет будущий exit code и приоритет нескольких ошибок.
	Kind FailureKind
	// Err является первичной или уже классифицированной причиной сбоя.
	Err error
	// Recovery содержит только пути, никогда — содержимое target/candidate.
	Recovery RecoveryPaths
}

// Error формирует пользовательское русское сообщение без stack trace и без копирования
// потенциально sensitive данных target.
func (e *TransactionError) Error() string {
	if e == nil {
		return "transaction завершилась ошибкой"
	}
	if e.Err == nil {
		return fmt.Sprintf("transaction завершилась ошибкой (%s): %s", e.Kind, failureKindDescription(e.Kind))
	}
	return fmt.Sprintf("transaction завершилась ошибкой (%s): %s: %v", e.Kind, failureKindDescription(e.Kind), e.Err)
}

// Unwrap возвращает первичную причину, чтобы errors.Is мог проверять как sentinel
// класса, так и исходную ошибку чтения, backup или write.
func (e *TransactionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// failureKindDescription возвращает короткую русскую расшифровку stable kind.
func failureKindDescription(kind FailureKind) string {
	switch kind {
	case FailureValidation:
		return "ошибка внутренней проверки"
	case FailureConflict:
		return "конфликт версии или недоступный lock"
	case FailureExternal:
		return "внешняя проверка завершилась ошибкой"
	case FailureIO:
		return "ошибка ввода-вывода, backup или записи"
	case FailureRollback:
		return "не удалось восстановить резервную копию"
	case FailureRotation:
		return "запись успешна, но ротация завершилась ошибкой"
	default:
		return "неизвестный класс ошибки"
	}
}

// transactionFailure создаёт typed failure и оборачивает cause в sentinel класса.
// Если cause уже содержит sentinel, обёртка не дублируется.
func transactionFailure(kind FailureKind, sentinel, cause error, recovery RecoveryPaths) *TransactionError {
	if sentinel == nil {
		sentinel = ErrTransactionIO
	}
	if cause == nil {
		cause = sentinel
	} else if !errors.Is(cause, sentinel) {
		cause = fmt.Errorf("%w: %w", sentinel, cause)
	}
	return wrapTransactionFailure(kind, sentinel, cause, recovery)
}

// wrapTransactionFailure сохраняет cause без дополнительной обёртки. Он применяется,
// когда helper уже сформировал классифицированную причину и повторное вложение
// sentinel только затруднило бы errors.Is.
func wrapTransactionFailure(kind FailureKind, _ error, cause error, recovery RecoveryPaths) *TransactionError {
	if cause == nil {
		cause = errors.New("причина ошибки не указана")
	}
	return &TransactionError{Kind: kind, Err: cause, Recovery: recovery}
}

// prepareOperations содержит только входное чтение target. Отдельный seam позволяет
// тестам проверить отказ чтения и запрет обращения к target без реального I/O.
type prepareOperations struct {
	readSnapshot func(string) (writer.Snapshot, error)
}

// preparedTransaction хранит результат входной подготовки, которую Execute позднее
// продолжит lock, повторной проверкой base hash, backup и replace.
type preparedTransaction struct {
	plan            Plan
	base            writer.Snapshot
	candidateSHA256 [sha256.Size]byte
	changed         bool
	internalReport  models.Report
}

// prepareTransaction выполняет только фазы, которые ТЗ требует до lock: чтение
// base snapshot, no-op detection и внутреннюю проверку changed candidate. No-op
// намеренно не валидирует fstab и не обращается к findmnt: неизменённая операция
// завершается без backup и записи.
func prepareTransaction(plan Plan, operations prepareOperations) (preparedTransaction, error) {
	if plan.Candidate == nil {
		return preparedTransaction{}, transactionFailure(FailureValidation, ErrTransactionValidation,
			errors.New("candidate не задан"), RecoveryPaths{})
	}
	if !validPlanOperation(plan.Operation) {
		return preparedTransaction{}, transactionFailure(FailureValidation, ErrTransactionValidation,
			fmt.Errorf("недопустимая operation %q", plan.Operation), RecoveryPaths{})
	}
	if operations.readSnapshot == nil {
		return preparedTransaction{}, fmt.Errorf("prepare transaction: nil field readSnapshot")
	}

	base, err := operations.readSnapshot(plan.TargetPath)
	if err != nil {
		return preparedTransaction{}, transactionFailure(FailureIO, ErrTransactionIO,
			fmt.Errorf("прочитать target %s: %w", plan.TargetPath, err), RecoveryPaths{Target: plan.TargetPath})
	}

	candidateSHA := sha256.Sum256(plan.Candidate)
	internalReport := models.Report{Diagnostics: make([]models.Diagnostic, 0)}
	changed := !bytes.Equal(base.Data, plan.Candidate)
	prepared := preparedTransaction{
		plan:            plan,
		base:            base,
		candidateSHA256: candidateSHA,
		changed:         changed,
		internalReport:  internalReport,
	}
	if changed {
		prepared.internalReport = validator.Validate(parser.Parse(plan.Candidate))
		if prepared.internalReport.HasErrors() {
			// Partial result возвращается вместе с ошибкой: CLI должен показать
			// конкретные диагностики candidate, а не только общий validation class.
			return prepared, transactionFailure(FailureValidation, ErrTransactionValidation,
				errors.New("candidate содержит диагностики severity=error"), RecoveryPaths{Target: base.Path})
		}
	}

	return prepared, nil
}

// validPlanOperation проверяет operation по полному whitelist backup metadata. Явный
// switch предотвращает непреднамеренное расширение допустимых значений и позволяет
// отклонить usage error до любого target I/O.
func validPlanOperation(operation backup.Operation) bool {
	switch operation {
	case backup.OperationBeforeAdd,
		backup.OperationBeforeEdit,
		backup.OperationBeforeDelete,
		backup.OperationBeforeComment,
		backup.OperationBeforeUncomment,
		backup.OperationBeforeRollback,
		backup.OperationManual:
		return true
	default:
		return false
	}
}

// transactionLock — минимальный контракт владения блокировкой, необходимый
// transaction. Interface позволяет тестам подменять lock без создания файлов.
type transactionLock interface {
	Close() error
}

// transactionOperations — immutable per-call seams доменной оркестрации. Каждая
// граница отказа (чтение, lock, findmnt, backup, replace) подменяется в tests
// отдельно, без mutable globals и без запуска внешних утилит.
type transactionOperations struct {
	readSnapshot       func(string) (writer.Snapshot, error)
	acquireLock        func(context.Context, string, time.Duration) (transactionLock, error)
	verifyCandidate    func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification
	createBackup       func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error)
	openBackup         func(string) (backup.Backup, error)
	replace            func(string, []byte) (writer.ReplaceResult, error)
	verifyTarget       func(context.Context, sysutil.CommandRunner, string) sysutil.Verification
	readBackupSnapshot func(string) (writer.Snapshot, error)
	rotate             func(string, string, string, int, []string) (backup.RotationResult, error)
}

// productionTransactionOperations заполняет seams готовыми проверенными пакетами.
// Lock acquire оборачивается в transactionLock, сохраняя публичный writer API.
func productionTransactionOperations() transactionOperations {
	return transactionOperations{
		readSnapshot: writer.ReadSnapshot,
		acquireLock: func(ctx context.Context, path string, timeout time.Duration) (transactionLock, error) {
			return writer.AcquireLock(ctx, path, timeout)
		},
		verifyCandidate: sysutil.VerifyCandidate,
		createBackup:    backup.Create,
		openBackup:      backup.Open,
		replace:         writer.Replace,
		verifyTarget: func(ctx context.Context, runner sysutil.CommandRunner, path string) sysutil.Verification {
			return sysutil.NewFindmntVerifier(runner).Verify(ctx, path)
		},
		readBackupSnapshot: writer.ReadSnapshot,
		rotate:             backup.Rotate,
	}
}

// validateTransactionOperations отклоняет programming error до чтения target или
// создания lock, чтобы incomplete config не имела частичных side effects.
func validateTransactionOperations(operations transactionOperations) error {
	fields := []struct {
		name    string
		missing bool
	}{
		{name: "readSnapshot", missing: operations.readSnapshot == nil},
		{name: "acquireLock", missing: operations.acquireLock == nil},
		{name: "verifyCandidate", missing: operations.verifyCandidate == nil},
		{name: "createBackup", missing: operations.createBackup == nil},
		{name: "openBackup", missing: operations.openBackup == nil},
		{name: "replace", missing: operations.replace == nil},
		{name: "verifyTarget", missing: operations.verifyTarget == nil},
		{name: "readBackupSnapshot", missing: operations.readBackupSnapshot == nil},
		{name: "rotate", missing: operations.rotate == nil},
	}
	for _, field := range fields {
		if field.missing {
			return fmt.Errorf("transaction operations: nil field %s", field.name)
		}
	}
	return nil
}

// Execute выполняет одну changed transaction: internal validation, lock, повторный
// base hash, findmnt candidate check, backup текущей операции, atomic replace,
// post-check и при необходимости один automatic rollback. No-op завершается до lock.
func Execute(ctx context.Context, plan Plan, options Options) (TransactionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return executeWithOperations(ctx, plan, options, productionTransactionOperations())
}

// executeWithOperations реализует Execute с подменяемыми границами отказов. Lock
// освобождается deferred ровно один раз; ошибка Close присоединяется к primary error
// и не изменяет TransactionResult.
func executeWithOperations(ctx context.Context, plan Plan, options Options, operations transactionOperations) (result TransactionResult, err error) {
	if err := validateTransactionOperations(operations); err != nil {
		return TransactionResult{}, err
	}

	// Backup-fallback из Options: CLI резолвит каталог/prefix/keep независимо
	// от Plan, поэтому пустые поля Plan дополняются настройками запуска.
	if plan.BackupDirectory == "" {
		plan.BackupDirectory = options.BackupDirectory
	}
	if plan.BackupPrefix == "" {
		plan.BackupPrefix = options.BackupPrefix
	}
	if plan.RotationKeep <= 0 {
		plan.RotationKeep = options.RotationKeep
	}

	prepared, err := prepareTransaction(plan, prepareOperations{readSnapshot: operations.readSnapshot})
	result = TransactionResult{
		Changed:         prepared.changed,
		TargetPath:      prepared.base.Path,
		CandidateSHA256: prepared.candidateSHA256,
		InternalReport:  prepared.internalReport,
		Recovery:        RecoveryPaths{Target: prepared.base.Path},
	}
	if err != nil {
		return result, err
	}
	if !prepared.changed {
		return result, nil
	}

	lock, err := operations.acquireLock(ctx, plan.TargetPath, options.LockTimeout)
	if err != nil {
		return result, transactionFailure(FailureConflict, ErrTransactionConflict,
			fmt.Errorf("получить lock target %s: %w", plan.TargetPath, err), result.Recovery)
	}
	lockClosed := false
	defer func() {
		if lockClosed {
			return
		}
		lockClosed = true
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("освободить lock target %s: %w", plan.TargetPath, closeErr))
		}
	}()

	// Повторное чтение обязано идти через тот же seam: иначе fault injection не
	// сможет проверить внешнее изменение target между base read и lock.
	current, err := operations.readSnapshot(plan.TargetPath)
	if err != nil {
		return result, transactionFailure(FailureConflict, ErrTransactionConflict,
			fmt.Errorf("повторно прочитать target %s: %w", plan.TargetPath, err), result.Recovery)
	}
	if current.SHA256 != prepared.base.SHA256 {
		return result, transactionFailure(FailureConflict, ErrTransactionConflict,
			fmt.Errorf("%w: %s", writer.ErrBaseHashConflict, current.Path), result.Recovery)
	}
	result.TargetPath = current.Path
	result.Recovery.Target = current.Path

	preVerification := operations.verifyCandidate(ctx, options.Runner, plan.Candidate)
	result.PreVerification = preVerification
	if preVerification.Report.HasErrors() {
		return result, transactionFailure(FailureExternal, ErrTransactionExternal,
			errors.New("проверка candidate завершилась с severity=error"), result.Recovery)
	}

	createOptions := backup.CreateOptions{
		Directory:          plan.BackupDirectory,
		Prefix:             plan.BackupPrefix,
		Operation:          plan.Operation,
		SourceVerification: backup.SourceValid,
	}
	created, err := operations.createBackup(current, createOptions)
	if err != nil {
		return result, transactionFailure(FailureIO, ErrTransactionIO,
			fmt.Errorf("создать backup target %s: %w", current.Path, err), result.Recovery)
	}
	result.Recovery.Backup = created.Path
	result.Recovery.Metadata = created.MetadataPath

	verified, err := operations.openBackup(created.Path)
	if err != nil {
		return result, transactionFailure(FailureIO, ErrTransactionIO,
			fmt.Errorf("проверить backup %s: %w", created.Path, err), result.Recovery)
	}
	result.Backup = verified

	replaceResult, err := operations.replace(current.Path, plan.Candidate)
	if err != nil {
		primary := transactionFailure(FailureIO, ErrTransactionIO,
			fmt.Errorf("заменить target %s: %w", current.Path, err), result.Recovery)
		if !replaceResult.Replaced {
			return result, primary
		}
		return rollbackAfterFailure(result, primary, current.Path, verified, operations)
	}

	postVerification := operations.verifyTarget(ctx, options.Runner, current.Path)
	result.PostVerification = postVerification
	if postVerification.Report.HasErrors() {
		primary := transactionFailure(FailureExternal, ErrTransactionExternal,
			errors.New("post-check target завершился с severity=error"), result.Recovery)
		return rollbackAfterFailure(result, primary, current.Path, verified, operations)
	}

	return rotateAfterSuccess(result, plan, options, current.Path, verified, operations)
}

// rotateAfterSuccess выполняет разрешённую ротацию только после полностью успешной
// записи и post-check. Backup текущей transaction добавляется в protected list,
// чтобы свежая копия оставалась доступной для явного rollback; caller slice копируется
// и никогда не изменяется через append capacity.
func rotateAfterSuccess(result TransactionResult, plan Plan, options Options, targetPath string, verified backup.Backup, operations transactionOperations) (TransactionResult, error) {
	effectivePrefix := plan.BackupPrefix
	if effectivePrefix == "" {
		effectivePrefix = backup.DefaultBackupPrefix
	}
	protected := append([]string(nil), options.RotationProtectedIDs...)
	protected = append(protected, verified.ID)

	rotation, err := operations.rotate(targetPath, effectivePrefix, plan.BackupDirectory, plan.RotationKeep, protected)
	result.Rotation = normalizeRotationResult(rotation)
	if err != nil {
		// Target уже успешно записан. По ТЗ ошибка rotation сообщается отдельно и
		// не должна молча откатывать это успешное изменение.
		return result, transactionFailure(FailureRotation, ErrRotationAfterSuccess,
			fmt.Errorf("выполнить rotation backups target %s: %w", targetPath, err), result.Recovery)
	}
	return result, nil
}

// normalizeRotationResult гарантирует non-nil slices в public TransactionResult даже
// если rotate seam или будущий caller вернул zero-value partial result.
func normalizeRotationResult(rotation backup.RotationResult) backup.RotationResult {
	if rotation.Removed == nil {
		rotation.Removed = make([]backup.Backup, 0)
	}
	if rotation.Skipped == nil {
		rotation.Skipped = make([]backup.Backup, 0)
	}
	if rotation.Failed == nil {
		rotation.Failed = make([]backup.Backup, 0)
	}
	return rotation
}

// rollbackAfterFailure выполняет ровно одну попытку восстановления backup текущей
// transaction. Он никогда не ищет «последний» backup и не повторяет replace: при
// повторном сбое сохраняются primary и rollback causes, а backup остаётся на диске.
func rollbackAfterFailure(result TransactionResult, primary *TransactionError, targetPath string, verified backup.Backup, operations transactionOperations) (TransactionResult, error) {
	backupSnapshot, err := operations.readBackupSnapshot(verified.Path)
	if err != nil {
		result.RollbackError = err
		rollbackFailure := transactionFailure(FailureRollback, ErrTransactionRollback,
			fmt.Errorf("прочитать backup %s: %w", verified.Path, err), result.Recovery)
		rollbackFailure.Err = errors.Join(rollbackFailure.Err, fmt.Errorf("первичная ошибка: %w", primary.Err))
		return result, rollbackFailure
	}

	_, err = operations.replace(targetPath, backupSnapshot.Data)
	if err != nil {
		result.RollbackError = err
		rollbackFailure := transactionFailure(FailureRollback, ErrTransactionRollback,
			fmt.Errorf("восстановить backup %s в target %s: %w", verified.Path, targetPath, err), result.Recovery)
		rollbackFailure.Err = errors.Join(rollbackFailure.Err, fmt.Errorf("первичная ошибка: %w", primary.Err))
		return result, rollbackFailure
	}

	result.RolledBack = true
	result.RollbackError = nil
	return result, primary
}
