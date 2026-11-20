// Package transaction оркестрирует одну безопасную запись fstab. Пакет расположен
// отдельно от writer и backup, потому что backup уже зависит от writer: доменной
// оркестрации разрешено использовать оба пакета, а циклическая зависимость
// запрещена. Task 1 реализует только входную модель, no-op detection и внутреннюю
// проверку candidate; lock, backup, atomic replace и rollback подключаются позднее.
package transaction

import (
	"bytes"
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
	if changed {
		internalReport = validator.Validate(parser.Parse(plan.Candidate))
		if internalReport.HasErrors() {
			return preparedTransaction{}, transactionFailure(FailureValidation, ErrTransactionValidation,
				errors.New("candidate содержит диагностики severity=error"), RecoveryPaths{Target: base.Path})
		}
	}

	return preparedTransaction{
		plan:            plan,
		base:            base,
		candidateSHA256: candidateSHA,
		changed:         changed,
		internalReport:  internalReport,
	}, nil
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
