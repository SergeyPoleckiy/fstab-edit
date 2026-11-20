// Package cliapp содержит доменную логику CLI поверх готовых пакетов parser,
// editor, validator и transaction: разрешение селекторов, подготовку preview
// мутаций и типизированные exit errors. Пакет не выполняет ввод-вывод, не
// читает реальные системные файлы и не хранит mutable globals: каждая функция
// работает только со своими аргументами.
package cliapp

import (
	"errors"
	"fmt"

	"fstab-edit/internal/transaction"
)

// Стабильные CLI exit codes. Значения согласованы с mapping FailureKind
// транзакции и не меняются без обновления документации CLI.
const (
	// ExitSuccess обозначает успешное завершение команды.
	ExitSuccess = 0
	// ExitValidation означает недопустимые входные данные, документ или изменение.
	ExitValidation = 1
	// ExitUsage означает некорректную комбинацию аргументов командной строки.
	ExitUsage = 2
	// ExitPermission означает недостаток прав: небезопасный config, недоступный
	// журнал. Код 3 ранее был зарезервирован; активируется этим блоком.
	ExitPermission = 3
	// ExitConflict означает конфликт доступа: target изменился или lock недоступен.
	ExitConflict = 4
	// ExitExternal означает сбой внешней проверки (findmnt).
	ExitExternal = 5
	// ExitIO означает сбой ввода-вывода, backup или ротации после успеха.
	ExitIO = 6
	// ExitRollback означает, что автоматический rollback не восстановил target.
	ExitRollback = 7
	// ExitAmbiguous означает, что селектор соответствует нескольким записям.
	ExitAmbiguous = 8
)

// Sentinel errors классифицируют CLI exit errors для errors.Is. Конкретная
// причина сохраняется в ExitError.Cause, поэтому проверка работает и по
// sentinel, и по исходной ошибке editor/parser/validator.
var (
	// ErrValidation объединяет ошибки входных данных, документа и изменений.
	ErrValidation = errors.New("недопустимые данные или изменение")
	// ErrUsage обозначает некорректную комбинацию аргументов командной строки.
	ErrUsage = errors.New("некорректные аргументы")
	// ErrAmbiguous обозначает селектор, соответствующий нескольким записям.
	ErrAmbiguous = errors.New("неоднозначный селектор")
	// ErrPermission объединяет конфликты доступа к target и lock.
	ErrPermission = errors.New("конфликт доступа")
)

// ExitError — типизированная ошибка CLI. Message — русский fallback для
// логов и Error(); MessageKey/MessageArgs — i18n-ключ и аргументы: точка
// печати (cmd) переводит сообщение выбранным языком, доменный слой остаётся
// чистым от выбора языка. MessageKey == "" означает «нет перевода».
type ExitError struct {
	// Code — стабильный exit code процесса.
	Code int
	// Message — русское диагностическое сообщение для пользователя.
	Message string
	// MessageKey — i18n-ключ сообщения; "" означает «перевода нет».
	MessageKey string
	// MessageArgs — аргументы для форматирования перевода (fallback Message
	// формируется теми же аргументами через fmt.Sprintf).
	MessageArgs []any
	// Cause сохраняет исходную ошибку нижележащего слоя; может быть nil.
	Cause error
	// Candidates содержит one-based номера строк-кандидатов при Code == 8.
	Candidates []int
}

// Error формирует русское сообщение об ошибке с учётом сохранённой причины.
func (e *ExitError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil && e.Cause.Error() != "" {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

// Unwrap сохраняет sentinel cause для errors.Is и errors.As.
func (e *ExitError) Unwrap() error { return e.Cause }

// newExitError собирает ExitError без i18n-ключа (MessageKey == "").
// Используется внутренними call-сайтами пакета cliapp.
func newExitError(code int, cause error, candidates []int, format string, args ...any) *ExitError {
	var message string
	if format != "" {
		message = fmt.Sprintf(format, args...)
	}
	return &ExitError{
		Code:       code,
		Message:    message,
		Cause:      cause,
		Candidates: append([]int(nil), candidates...),
	}
}

// NewExitErrorK — общий конструктор ExitError с явным exit code и i18n-ключом.
// Message строится через fmt.Sprintf с теми же args (key используется как
// формат: для ключей без глаголов Message совпадает с ключом), поэтому
// русский fallback для Error() сохраняется.
func NewExitErrorK(code int, cause error, candidates []int, key string, args ...any) *ExitError {
	return newExitErrorK(code, cause, candidates, key, key, args...)
}

// newExitErrorK собирает ExitError с i18n-ключом и копией списка кандидатов.
// Message формируется через fmt.Sprintf(format, args...) — русский fallback
// для логов и Error() сохраняется независимо от наличия ключа.
func newExitErrorK(code int, cause error, candidates []int, key, format string, args ...any) *ExitError {
	var message string
	if format != "" {
		message = fmt.Sprintf(format, args...)
	}
	return &ExitError{
		Code:        code,
		Message:     message,
		MessageKey:  key,
		MessageArgs: append([]any(nil), args...),
		Cause:       cause,
		Candidates:  append([]int(nil), candidates...),
	}
}

// UsageError создаёт ExitError с code 2 для некорректных аргументов.
func UsageError(cause error, format string, args ...any) *ExitError {
	return UsageErrorK(cause, "", format, args...)
}

// UsageErrorK создаёт ExitError с code 2, i18n-ключом и русским fallback
// Message. args подставляются и в перевод ключа, и в fallback Message.
func UsageErrorK(cause error, key, format string, args ...any) *ExitError {
	if cause == nil {
		cause = ErrUsage
	} else {
		cause = errors.Join(ErrUsage, cause)
	}
	return newExitErrorK(ExitUsage, cause, nil, key, format, args...)
}

// ValidationError создаёт ExitError с code 1 для недопустимых данных или изменений.
func ValidationError(cause error, format string, args ...any) *ExitError {
	return ValidationErrorK(cause, "", format, args...)
}

// ValidationErrorK создаёт ExitError с code 1, i18n-ключом и русским fallback
// Message. args подставляются и в перевод ключа, и в fallback Message.
func ValidationErrorK(cause error, key, format string, args ...any) *ExitError {
	if cause == nil {
		cause = ErrValidation
	} else {
		cause = errors.Join(ErrValidation, cause)
	}
	return newExitErrorK(ExitValidation, cause, nil, key, format, args...)
}

// AmbiguityError создаёт ExitError с code 8 и списком кандидатов.
func AmbiguityError(cause error, candidates []int, format string, args ...any) *ExitError {
	return AmbiguityErrorK(cause, candidates, "", format, args...)
}

// AmbiguityErrorK создаёт ExitError с code 8, списком кандидатов, i18n-ключом
// и русским fallback Message. args подставляются и в перевод ключа, и в
// fallback Message.
func AmbiguityErrorK(cause error, candidates []int, key, format string, args ...any) *ExitError {
	if cause == nil {
		cause = ErrAmbiguous
	} else {
		cause = errors.Join(ErrAmbiguous, cause)
	}
	return newExitErrorK(ExitAmbiguous, cause, candidates, key, format, args...)
}

// PermissionError создаёт ExitError с code 3 для конфликтов доступа.
func PermissionError(cause error, format string, args ...any) *ExitError {
	return PermissionErrorK(cause, "", format, args...)
}

// PermissionErrorK создаёт ExitError с code ExitPermission (3), i18n-ключом
// и русским fallback Message. args подставляются и в перевод ключа, и в
// fallback Message.
func PermissionErrorK(cause error, key, format string, args ...any) *ExitError {
	if cause == nil {
		cause = ErrPermission
	} else {
		cause = errors.Join(ErrPermission, cause)
	}
	return newExitErrorK(ExitPermission, cause, nil, key, format, args...)
}

// ExitCodeForFailureKind отображает стабильный класс сбоя transaction на exit
// code процесса. Неизвестные классы относятся к I/O, чтобы сбой никогда не
// маскировался под успех.
func ExitCodeForFailureKind(kind transaction.FailureKind) int {
	switch kind {
	case transaction.FailureValidation:
		return ExitValidation
	case transaction.FailureConflict:
		return ExitConflict
	case transaction.FailureExternal:
		return ExitExternal
	case transaction.FailureRollback:
		return ExitRollback
	case transaction.FailureIO, transaction.FailureRotation:
		return ExitIO
	default:
		return ExitIO
	}
}
