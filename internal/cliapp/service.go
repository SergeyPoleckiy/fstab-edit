// Файл service.go собирает доменные операции CLI поверх готовых пакетов
// writer, parser, validator, sysutil и transaction. Service выполняет чтение
// target, валидацию, внешнюю проверку и запуск mutation transaction, не
// изменяя target самостоятельно: запись происходит только внутри transaction
// (RunMutation) либо в явном Rollback (rollback.go). Все границы отказа
// скрыты за Dependencies, поэтому тесты подменяют их fake seams без
// mutable globals и без реального /etc/fstab.
package cliapp

import (
	"context"
	"errors"
	"strings"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/parser"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/transaction"
	"fstab-edit/internal/validator"
	"fstab-edit/internal/writer"
	"fstab-edit/pkg/models"
)

// Dependencies содержит границы отказа Service: чтение target, оркестрацию
// transaction, создание/выбор/открытие backup, замену target и внешнюю
// проверку записанного target. ProductionDependencies заполняет их готовыми
// пакетами; тесты заменяют отдельные поля, не трогая остальные.
type Dependencies struct {
	// ReadSnapshot безопасно читает target и фиксирует его байты и SHA-256.
	ReadSnapshot func(string) (writer.Snapshot, error)
	// ExecuteTransaction выполняет одну подготовленную mutation.
	ExecuteTransaction func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error)
	// CreateBackup создаёт data+metadata backup переданного snapshot.
	CreateBackup func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error)
	// SelectBackup выбирает backup по ID (пустой ID — newest first).
	SelectBackup func(string, string, string, string) (backup.Selection, error)
	// OpenBackup проверяет metadata/hash существующей backup-пары.
	OpenBackup func(string) (backup.Backup, error)
	// Replace атомарно заменяет target новыми байтами.
	Replace func(string, []byte) (writer.ReplaceResult, error)
	// VerifyTarget проверяет фактический target после replace через findmnt.
	VerifyTarget func(context.Context, sysutil.CommandRunner, string) sysutil.Verification
}

// ProductionDependencies заполняет каждую границу готовой проверенной
// реализацией. VerifyTarget собирается заново на каждый вызов, поэтому
// подменённый runner корректно доезжает до verifier без mutable state.
func ProductionDependencies(runner sysutil.CommandRunner) Dependencies {
	return Dependencies{
		ReadSnapshot:       writer.ReadSnapshot,
		ExecuteTransaction: transaction.Execute,
		CreateBackup:       backup.Create,
		SelectBackup:       backup.Select,
		OpenBackup:         backup.Open,
		Replace:            writer.Replace,
		VerifyTarget: func(ctx context.Context, runner sysutil.CommandRunner, path string) sysutil.Verification {
			return sysutil.NewFindmntVerifier(runner).Verify(ctx, path)
		},
	}
}

// Service выполняет доменные операции CLI для одного target. Методы не хранят
// состояния между вызовами и не используют глобальные переменные: runner и
// dependencies фиксируются при создании.
type Service struct {
	dependencies Dependencies
	runner       sysutil.CommandRunner
}

// NewService собирает Service с заданными dependencies и runner. Runner
// применяется только для read-only внешних проверок и post verification;
// внутренние проверки runner не требуют.
func NewService(dependencies Dependencies, runner sysutil.CommandRunner) *Service {
	return &Service{dependencies: dependencies, runner: runner}
}

// List читает target, разбирает документ и строит view-модель команды list.
// Операция строго read-only: документ на диске не изменяется. Entry views
// получают диагностики своей строки, а остальные диагностики остаются
// document-level в ListResult.Diagnostics. Ошибка чтения target отображается
// на code 6 с сохранённой причиной.
func (s *Service) List(target string) (ListResult, *ExitError) {
	snapshot, err := s.dependencies.ReadSnapshot(target)
	if err != nil {
		return ListResult{}, newExitErrorK(ExitIO, err, nil, "error.io.read_target", "прочитать target %s", target)
	}
	document := parser.Parse(snapshot.Data)
	report := validator.Validate(document)

	result := ListResult{
		Target:      snapshot.Path,
		Entries:     make([]EntryView, 0),
		Diagnostics: make([]models.Diagnostic, 0),
	}
	for index, node := range document.Nodes {
		if node.Kind != models.NodeEntry && node.Kind != models.NodeCommentedEntry {
			continue
		}
		result.Entries = append(result.Entries, entryView(document, index, node))
	}
	// Распределение диагностик по владельцу: внутренний валидатор привязывает
	// диагностики к физической строке, поэтому совпадение по Line переносит
	// диагностику в запись, а остальные (Line 0 или строка без записи) —
	// на уровень документа.
	entryIndexByLine := make(map[int]int, len(result.Entries))
	for i := range result.Entries {
		entryIndexByLine[result.Entries[i].Line] = i
	}
	for _, diagnostic := range report.Diagnostics {
		if owner, ok := entryIndexByLine[diagnostic.Line]; ok {
			result.Entries[owner].Diagnostics = append(result.Entries[owner].Diagnostics, diagnostic)
			continue
		}
		result.Diagnostics = append(result.Diagnostics, diagnostic)
	}
	return result, nil
}

// entryView собирает EntryView из узла записи: состояние active/commented,
// raw/decoded представления шести полей и conservative associated comment.
// Физический номер строки берётся из позиции узла, чтобы совпадать с Line
// диагностик валидатора.
func entryView(document *models.Document, index int, node models.Node) EntryView {
	state := "active"
	if node.Kind == models.NodeCommentedEntry {
		state = "commented"
	}
	view := EntryView{
		Line:        index + 1,
		State:       state,
		Comment:     associatedComment(document, index),
		Diagnostics: make([]models.Diagnostic, 0),
	}
	if node.Entry == nil {
		return view
	}
	entry := node.Entry
	view.Source = fieldView(&entry.Source)
	view.MountPoint = fieldView(&entry.MountPoint)
	view.FSType = fieldView(&entry.FSType)
	view.Options = fieldView(&entry.Options)
	view.Dump = fieldView(entry.Dump)
	view.Pass = fieldView(entry.Pass)
	return view
}

// fieldView копирует одно поле записи в FieldView; nil pointer (dump/pass)
// отображается как отсутствующее поле.
func fieldView(field *models.Field) FieldView {
	if field == nil {
		return FieldView{Present: false}
	}
	return FieldView{Raw: field.Raw, Decoded: field.Value, Present: true}
}

// associatedComment применяет conservative правило: к записи привязывается
// только непрерывный блок NodeComment непосредственно над ней. Блок,
// доходящий до самой первой строки документа, считается заголовком документа
// и намеренно пропускается: без отделяющей записи или blank line нельзя
// отличить пояснение от шапки. Несколько строк комментария объединяются
// переводом строки; маркер '#' и один пробел после него отбрасываются.
func associatedComment(document *models.Document, entryIndex int) string {
	lines := make([]string, 0)
	index := entryIndex - 1
	for index >= 0 && document.Nodes[index].Kind == models.NodeComment {
		lines = append([]string{commentText(document.Nodes[index].Raw)}, lines...)
		index--
	}
	if len(lines) == 0 || index < 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// commentText удаляет маркер комментария и ровно один разделитель после него,
// сохраняя остальное содержимое строки без изменений.
func commentText(raw string) string {
	text := strings.TrimLeft(raw, "#")
	return strings.TrimPrefix(text, " ")
}

// ValidationResult описывает результат команды validate: canonical target,
// объединённый отчёт проверки (внутренний, а online ещё и внешний) и исходная
// внешняя Verification для cmd layer. Сериализация для output определяется
// Task 4: Verification.Execution содержит raw вывод findmnt и не предназначен
// для прямой сериализации в JSON.
type ValidationResult struct {
	Target       string
	Report       models.Report
	Verification sysutil.Verification
}

// Validate читает target и проверяет его содержимое. Offline режим выполняет
// только внутреннюю проверку и runner не вызывает. Online режим дополнительно
// прогоняет байты target через sysutil.VerifyCandidate (приватный candidate
// файл + findmnt) и объединяет отчёты через models.MergeReports. Диагностики
// severity=error отображаются на code 1, но результат возвращается при любом
// исходе, чтобы cmd layer мог показать конкретные диагностики.
func (s *Service) Validate(ctx context.Context, target string, offline bool) (ValidationResult, *ExitError) {
	snapshot, err := s.dependencies.ReadSnapshot(target)
	if err != nil {
		return ValidationResult{}, newExitErrorK(ExitIO, err, nil, "error.io.read_target", "прочитать target %s", target)
	}
	internal := validator.Validate(parser.Parse(snapshot.Data))
	result := ValidationResult{
		Target: snapshot.Path,
		Report: internal,
	}
	if offline {
		return result, validationExitError(result.Report, snapshot.Path)
	}
	verification := sysutil.VerifyCandidate(ctx, s.runner, snapshot.Data)
	result.Verification = verification
	result.Report = models.MergeReports(internal, verification.Report)
	return result, validationExitError(result.Report, snapshot.Path)
}

// validationExitError превращает отчёт с ошибками в ExitError code 1;
// отчёт без ошибок (включая warnings) успешен. Причина отчёта — сами
// диагностики в result, поэтому Cause не дублируется.
func validationExitError(report models.Report, target string) *ExitError {
	if !report.HasErrors() {
		return nil
	}
	return newExitErrorK(ExitValidation, nil, nil, "error.validation.failed",
		"проверка target %s завершилась с диагностиками severity=error", target)
}

// VerifyMount проверяет фактические байты target через статический findmnt:
// содержимое target копируется в приватный candidate-файл внутри
// sysutil.VerifyCandidate, реальный /etc/fstab и системные ресурсы не
// затрагиваются. Предупреждения допускаются, ошибки отображаются на code 1.
func (s *Service) VerifyMount(ctx context.Context, target string) (sysutil.Verification, *ExitError) {
	snapshot, err := s.dependencies.ReadSnapshot(target)
	if err != nil {
		return sysutil.Verification{}, newExitErrorK(ExitIO, err, nil, "error.io.read_target", "прочитать target %s", target)
	}
	verification := sysutil.VerifyCandidate(ctx, s.runner, snapshot.Data)
	if verification.Report.HasErrors() {
		return verification, newExitErrorK(ExitValidation, nil, nil, "error.external.verify_failed",
			"внешняя проверка target %s завершилась с severity=error", snapshot.Path)
	}
	return verification, nil
}

// RunMutation передаёт подготовленный preview в transaction.Execute: Plan
// собирается из preview и target, а options пробрасываются вызывающему без
// изменений. TransactionResult возвращается даже при ошибке. Ошибка
// *transaction.TransactionError отображается на exit code через
// ExitCodeForFailureKind, сохраняя unwrap, recovery paths и sentinel-классы;
// прочие ошибки относятся к I/O (code 6), чтобы сбой не маскировался под успех.
func (s *Service) RunMutation(ctx context.Context, target string, preview MutationPreview, options transaction.Options) (transaction.TransactionResult, error) {
	plan := transaction.Plan{
		TargetPath: target,
		Candidate:  preview.Candidate,
		Operation:  preview.Operation,
	}
	result, err := s.dependencies.ExecuteTransaction(ctx, plan, options)
	if err == nil {
		return result, nil
	}
	var txErr *transaction.TransactionError
	if errors.As(err, &txErr) {
		return result, newExitErrorK(ExitCodeForFailureKind(txErr.Kind), txErr, nil, "error.io.mutation_not_saved", "мутация не сохранена")
	}
	return result, newExitErrorK(ExitIO, err, nil, "error.io.mutation_not_saved", "мутация не сохранена")
}
