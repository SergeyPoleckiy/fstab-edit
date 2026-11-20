// Package output сериализует доменные результаты cliapp/transaction/backup в
// стабильные форматы вывода CLI: table (для людей), JSON schema v1, CSV и TSV
// (для скриптов). Пакет не выполняет ввода-вывода и не печатает сам: функции
// возвращают готовые bytes, а cmd layer решает, куда их записать. Machine
// formats возвращают ровно один structured-документ без prompts и log-сообщений,
// чтобы stdout оставался пригоден для pipes. Mutable globals отсутствуют.
package output

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/transaction"
	"fstab-edit/pkg/models"
)

// schemaVersion — первая стабильная версия machine output schema. Ключ
// schema_version обязан присутствовать в корне каждого JSON-документа;
// обратно несовместимые изменения требуют новой версии.
const schemaVersion = 1

// Format — стабильное имя формата вывода. Неизвестное значение отклоняется
// ошибкой, чтобы cmd layer отобразил его в usage error, а не молчаливый fallback.
type Format string

// Поддерживаемые форматы вывода. Значения совпадают с допустимыми значениями
// флага --output и не локализуются.
const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatCSV   Format = "csv"
	FormatTSV   Format = "tsv"
)

// ErrUnknownFormat отклоняет формат вне whitelist table/json/csv/tsv.
var ErrUnknownFormat = errors.New("неизвестный формат вывода")

// listDocument — корневой JSON-документ команды list. ListResult встраивается
// как анонимное поле, чтобы snake_case ключи view-модели (target, entries)
// остались в одном месте, а schema_version добавлялся только в machine output.
type listDocument struct {
	SchemaVersion int `json:"schema_version"`
	cliapp.ListResult
}

// mutationDocument — JSON-представление результата мутации по ТЗ 11.9:
// operation, changed, dry_run, target, diff, diagnostics и backup_id только
// если backup создан. Rollback встраивается documented struct-ом, когда
// transaction выполнялась и откат был инициирован.
type mutationDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	Operation     backup.Operation    `json:"operation"`
	Changed       bool                `json:"changed"`
	DryRun        bool                `json:"dry_run"`
	Target        string              `json:"target"`
	Diff          string              `json:"diff"`
	Diagnostics   []models.Diagnostic `json:"diagnostics"`
	BackupID      string              `json:"backup_id,omitempty"`
	Rollback      *rollbackView       `json:"rollback,omitempty"`
}

// rollbackView — documented struct результата automatic rollback внутри
// mutation JSON. Присутствует только если transaction пыталась откатить target.
type rollbackView struct {
	RolledBack bool   `json:"rolled_back"`
	BackupID   string `json:"backup_id"`
	Error      string `json:"error,omitempty"`
}

// backupDocument — JSON-представление результата команды backup.
type backupDocument struct {
	SchemaVersion int    `json:"schema_version"`
	BackupID      string `json:"backup_id"`
	DataPath      string `json:"data_path"`
	MetadataPath  string `json:"metadata_path"`
	DryRun        bool   `json:"dry_run"`
}

// verificationView — documented struct проверки: Report без исходного Execution,
// чтобы sensitive stdout/stderr findmnt не утекали в machine output.
type verificationView struct {
	Diagnostics []models.Diagnostic `json:"diagnostics"`
}

// rollbackDocument — JSON-представление результата команды rollback.
type rollbackDocument struct {
	SchemaVersion    int              `json:"schema_version"`
	SelectedBackupID string           `json:"selected_backup_id"`
	SafetyBackupID   string           `json:"safety_backup_id"`
	DryRun           bool             `json:"dry_run"`
	PostVerification verificationView `json:"post_verification"`
}

// diagnosticsDocument — корневой JSON-документ отдельного списка диагностик.
type diagnosticsDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	Diagnostics   []models.Diagnostic `json:"diagnostics"`
}

// RollbackInfo — входные данные RenderRollback: выбранная и safety backup IDs,
// признак dry-run и post verification фактического target. PostVerification
// Report сериализуется без Execution по тем же причинам, что и в mutation.
type RollbackInfo struct {
	SelectedBackupID string
	SafetyBackupID   string
	DryRun           bool
	PostVerification models.Report
}

// diagnosticsHeader — стабильные имена колонок диагностик для CSV/TSV. Params
// сериализуются отдельной колонкой params_json через encoding/json, чтобы не
// смешивать структурированные данные с message.
const diagnosticsHeader = "line,severity,code,field,message,params_json"

// listCSVHeader — точный порядок и имена колонок list: raw/decoded поля не
// смешиваются в одну неоднозначную колонку (ТЗ 11.10).
const listCSVHeader = "line,state,source_raw,source,mount_point_raw,mount_point," +
	"fs_type_raw,fs_type,options_raw,options,dump_raw,dump,pass_raw,pass,comment"

// RenderList сериализует ListResult в выбранный формат. JSON — schema v1
// документ; CSV/TSV — стабильные колонки с "-" для отсутствующих полей;
// table — колонки LINE STATE SOURCE MOUNT TYPE OPTIONS DUMP PASS STATUS.
func RenderList(result cliapp.ListResult, format Format) ([]byte, error) {
	switch format {
	case FormatJSON:
		document := listDocument{SchemaVersion: schemaVersion, ListResult: result}
		document.Entries = normalizeEntries(document.Entries)
		document.Diagnostics = normalizeDiagnostics(document.Diagnostics)
		return marshalJSON(document)
	case FormatTable:
		return renderListTable(result), nil
	case FormatCSV:
		return renderDelimited(listRecords(result), ',')
	case FormatTSV:
		return renderDelimited(listRecords(result), '\t')
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownFormat, format)
	}
}

// RenderDiagnostics сериализует список диагностик в выбранный формат. JSON —
// корневой документ со schema_version; CSV/TSV — колонки
// line,severity,code,field,message,params_json.
func RenderDiagnostics(diagnostics []models.Diagnostic, format Format) ([]byte, error) {
	switch format {
	case FormatJSON:
		return marshalJSON(diagnosticsDocument{
			SchemaVersion: schemaVersion,
			Diagnostics:   normalizeDiagnostics(diagnostics),
		})
	case FormatCSV:
		return renderDelimited(diagnosticsRecords(diagnostics), ',')
	case FormatTSV:
		return renderDelimited(diagnosticsRecords(diagnostics), '\t')
	case FormatTable:
		return renderDiagnosticsTable(diagnostics), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownFormat, format)
	}
}

// RenderMutation сериализует preview мутации. Preview nil — programming error
// вызывающего слоя. Executed не-nil означает, что transaction выполнялась:
// backup_id и documented struct rollback берутся из её результата. Для table
// возвращается человекочитаемый summary вместе с diff; cmd layer печатает его
// stdout.
func RenderMutation(preview *cliapp.MutationPreview, target string, dryRun bool, executed *transaction.TransactionResult, format Format) ([]byte, error) {
	if preview == nil {
		return nil, errors.New("mutation preview не задан")
	}
	switch format {
	case FormatJSON:
		document := mutationDocument{
			SchemaVersion: schemaVersion,
			Operation:     preview.Operation,
			Changed:       preview.Changed,
			DryRun:        dryRun,
			Target:        target,
			Diff:          preview.Diff,
			Diagnostics:   normalizeDiagnostics(preview.Report.Diagnostics),
		}
		if executed != nil {
			document.BackupID = executed.Backup.ID
			document.Changed = executed.Changed
			if executed.RolledBack || executed.RollbackError != nil {
				view := rollbackView{
					RolledBack: executed.RolledBack,
					BackupID:   executed.Backup.ID,
				}
				if executed.RollbackError != nil {
					view.Error = executed.RollbackError.Error()
				}
				document.Rollback = &view
			}
		}
		return marshalJSON(document)
	case FormatTable:
		return renderMutationTable(preview, target, dryRun, executed), nil
	case FormatCSV, FormatTSV:
		separator := separatorFor(format)
		backupID := ""
		changed := strconv.FormatBool(preview.Changed)
		if executed != nil {
			backupID = executed.Backup.ID
			changed = strconv.FormatBool(executed.Changed)
		}
		records := [][]string{
			{"operation", "changed", "dry_run", "target", "backup_id", "diff"},
			{string(preview.Operation), changed, strconv.FormatBool(dryRun), target, backupID, preview.Diff},
		}
		return renderDelimited(records, separator)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownFormat, format)
	}
}

// RenderBackup сериализует созданную резервную копию. JSON — schema v1
// документ с backup_id, data_path, metadata_path и dry_run; CSV/TSV и table
// используют те же стабильные поля.
func RenderBackup(created backup.Backup, dryRun bool, format Format) ([]byte, error) {
	switch format {
	case FormatJSON:
		return marshalJSON(backupDocument{
			SchemaVersion: schemaVersion,
			BackupID:      created.ID,
			DataPath:      created.Path,
			MetadataPath:  created.MetadataPath,
			DryRun:        dryRun,
		})
	case FormatTable:
		var builder strings.Builder
		fmt.Fprintf(&builder, "Backup: %s\n", created.ID)
		fmt.Fprintf(&builder, "Data: %s\n", created.Path)
		fmt.Fprintf(&builder, "Metadata: %s\n", created.MetadataPath)
		fmt.Fprintf(&builder, "Dry-run: %s\n", yesNo(dryRun))
		return []byte(builder.String()), nil
	case FormatCSV, FormatTSV:
		separator := separatorFor(format)
		records := [][]string{
			{"backup_id", "data_path", "metadata_path", "dry_run"},
			{created.ID, created.Path, created.MetadataPath, strconv.FormatBool(dryRun)},
		}
		return renderDelimited(records, separator)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownFormat, format)
	}
}

// RenderRollback сериализует результат команды rollback: выбранную и safety
// копии, dry-run и post verification фактического target.
func RenderRollback(info RollbackInfo, format Format) ([]byte, error) {
	post := verificationView{Diagnostics: normalizeDiagnostics(info.PostVerification.Diagnostics)}
	switch format {
	case FormatJSON:
		return marshalJSON(rollbackDocument{
			SchemaVersion:    schemaVersion,
			SelectedBackupID: info.SelectedBackupID,
			SafetyBackupID:   info.SafetyBackupID,
			DryRun:           info.DryRun,
			PostVerification: post,
		})
	case FormatTable:
		var builder strings.Builder
		fmt.Fprintf(&builder, "Selected backup: %s\n", info.SelectedBackupID)
		fmt.Fprintf(&builder, "Safety backup: %s\n", info.SafetyBackupID)
		fmt.Fprintf(&builder, "Dry-run: %s\n", yesNo(info.DryRun))
		fmt.Fprintf(&builder, "Post verification: %s\n", severitySummary(post.Diagnostics))
		return []byte(builder.String()), nil
	case FormatCSV, FormatTSV:
		separator := separatorFor(format)
		records := [][]string{
			{"selected_backup_id", "safety_backup_id", "dry_run", "post_status"},
			{info.SelectedBackupID, info.SafetyBackupID, strconv.FormatBool(info.DryRun), severitySummary(post.Diagnostics)},
		}
		return renderDelimited(records, separator)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownFormat, format)
	}
}

// separatorFor возвращает разделитель записей для machine-форматов: запятую
// для CSV и tab для TSV.
func separatorFor(format Format) byte {
	if format == FormatTSV {
		return '\t'
	}
	return ','
}

// marshalJSON сериализует один документ компактно с завершающим newline:
// Unix-конвенция текстового документа и ровно один JSON value для pipes.
func marshalJSON(document any) ([]byte, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("сериализовать JSON: %w", err)
	}
	return append(data, '\n'), nil
}

// renderDelimited формирует CSV через encoding/csv (стандартное quoting) или
// TSV вручную с documented backslash-escaping. Оба варианта завершаются
// newline после каждой записи, включая последнюю.
func renderDelimited(records [][]string, separator byte) ([]byte, error) {
	var buffer strings.Builder
	if separator == ',' {
		writer := csv.NewWriter(&buffer)
		if err := writer.WriteAll(records); err != nil {
			return nil, fmt.Errorf("сериализовать CSV: %w", err)
		}
		return []byte(buffer.String()), nil
	}
	for _, record := range records {
		for index, cell := range record {
			if index > 0 {
				buffer.WriteByte(separator)
			}
			buffer.WriteString(escapeTSV(cell))
		}
		buffer.WriteByte('\n')
	}
	return []byte(buffer.String()), nil
}

// escapeTSV экранирует tab, newline, carriage return и backslash через backslash:
// tab → \t, LF → \n, CR → \r, backslash → \\. Backslash заменяется в рамках
// того же прохода по символам первым по смыслу: escape-последовательности не
// перекрываются и преобразование обратимо.
func escapeTSV(value string) string {
	if !strings.ContainsAny(value, "\\\t\n\r") {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, char := range value {
		switch char {
		case '\\':
			builder.WriteString(`\\`)
		case '\t':
			builder.WriteString(`\t`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

// renderListTable формирует человекочитаемую таблицу записей. Optional-поля
// без Present отображаются как "-"; STATUS содержит краткую severity summary.
func renderListTable(result cliapp.ListResult) []byte {
	var buffer strings.Builder
	writer := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	// tabwriter.Write не возвращает ошибок: данные буферизуются в памяти.
	_, _ = fmt.Fprintln(writer, "LINE\tSTATE\tSOURCE\tMOUNT\tTYPE\tOPTIONS\tDUMP\tPASS\tSTATUS")
	for _, entry := range result.Entries {
		_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.Line,
			entry.State,
			tableField(entry.Source),
			tableField(entry.MountPoint),
			tableField(entry.FSType),
			tableField(entry.Options),
			tableField(entry.Dump),
			tableField(entry.Pass),
			severitySummary(entry.Diagnostics),
		)
	}
	_ = writer.Flush()
	return []byte(buffer.String())
}

// renderDiagnosticsTable формирует человекочитаемый список диагностик.
func renderDiagnosticsTable(diagnostics []models.Diagnostic) []byte {
	var buffer strings.Builder
	for _, diagnostic := range diagnostics {
		line := strconv.Itoa(diagnostic.Line)
		field := diagnostic.Field
		if field == "" {
			field = "-"
		}
		fmt.Fprintf(&buffer, "%s:%s [%s] %s (%s)\n",
			line, field, diagnostic.Severity, diagnostic.Message, diagnostic.Code)
	}
	if buffer.Len() == 0 {
		return []byte("диагностик нет\n")
	}
	return []byte(buffer.String())
}

// renderMutationTable формирует human summary вместе с diff: итог операции
// читается до просмотра diff, а сам diff печатается без изменений. Вывод
// гарантированно завершается newline даже если diff не содержит финального
// перевода строки.
func renderMutationTable(preview *cliapp.MutationPreview, target string, dryRun bool, executed *transaction.TransactionResult) []byte {
	var builder strings.Builder
	changed := preview.Changed
	backupID := ""
	if executed != nil {
		changed = executed.Changed
		backupID = executed.Backup.ID
	}
	fmt.Fprintf(&builder, "Операция: %s\n", preview.Operation)
	fmt.Fprintf(&builder, "Изменён: %s\n", yesNo(changed))
	fmt.Fprintf(&builder, "Dry-run: %s\n", yesNo(dryRun))
	fmt.Fprintf(&builder, "Target: %s\n", target)
	if backupID != "" {
		fmt.Fprintf(&builder, "Backup: %s\n", backupID)
	}
	builder.WriteByte('\n')
	builder.WriteString(preview.Diff)
	if !strings.HasSuffix(builder.String(), "\n") {
		builder.WriteByte('\n')
	}
	return []byte(builder.String())
}

// listRecords собирает стабильные CSV/TSV-записи list с "-" для отсутствующих
// полей и пустого комментария.
func listRecords(result cliapp.ListResult) [][]string {
	records := make([][]string, 0, len(result.Entries)+1)
	records = append(records, strings.Split(listCSVHeader, ","))
	for _, entry := range result.Entries {
		sourceRaw, sourceDecoded := delimitedField(entry.Source)
		mountRaw, mountDecoded := delimitedField(entry.MountPoint)
		typeRaw, typeDecoded := delimitedField(entry.FSType)
		optionsRaw, optionsDecoded := delimitedField(entry.Options)
		dumpRaw, dumpDecoded := delimitedField(entry.Dump)
		passRaw, passDecoded := delimitedField(entry.Pass)
		records = append(records, []string{
			strconv.Itoa(entry.Line),
			entry.State,
			sourceRaw,
			sourceDecoded,
			mountRaw,
			mountDecoded,
			typeRaw,
			typeDecoded,
			optionsRaw,
			optionsDecoded,
			dumpRaw,
			dumpDecoded,
			passRaw,
			passDecoded,
			dashIfEmpty(entry.Comment),
		})
	}
	return records
}

// diagnosticsRecords собирает CSV/TSV-записи диагностик. Params сериализуются
// через encoding/json; nil Params отображаются как "{}" вместо "null", чтобы
// колонка всегда содержала JSON-объект.
func diagnosticsRecords(diagnostics []models.Diagnostic) [][]string {
	records := make([][]string, 0, len(diagnostics)+1)
	records = append(records, strings.Split(diagnosticsHeader, ","))
	for _, diagnostic := range diagnostics {
		records = append(records, []string{
			strconv.Itoa(diagnostic.Line),
			string(diagnostic.Severity),
			diagnostic.Code,
			dashIfEmpty(diagnostic.Field),
			diagnostic.Message,
			paramsJSON(diagnostic.Params),
		})
	}
	return records
}

// paramsJSON сериализует Params диагностики одним JSON-объектом.
func paramsJSON(params map[string]string) string {
	if params == nil {
		return "{}"
	}
	data, err := json.Marshal(params)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// normalizeEntries гарантирует non-nil slices в JSON даже для zero-value
// ListResult: машинный потребитель всегда видит массив, а не null.
func normalizeEntries(entries []cliapp.EntryView) []cliapp.EntryView {
	if entries == nil {
		return []cliapp.EntryView{}
	}
	return entries
}

// normalizeDiagnostics гарантирует non-nil slice диагностик для JSON.
func normalizeDiagnostics(diagnostics []models.Diagnostic) []models.Diagnostic {
	if diagnostics == nil {
		return []models.Diagnostic{}
	}
	return diagnostics
}

// tableField возвращает decoded значение поля для table либо "-" для
// отсутствующего поля.
func tableField(field cliapp.FieldView) string {
	if !field.Present {
		return "-"
	}
	return field.Decoded
}

// delimitedField возвращает raw и decoded значения поля с "-" для отсутствующего.
func delimitedField(field cliapp.FieldView) (string, string) {
	if !field.Present {
		return "-", "-"
	}
	return field.Raw, field.Decoded
}

// dashIfEmpty заменяет пустое значение на "-", чтобы отсутствие данных было
// видимо в колонке без неоднозначности "пустая ячейка или ошибка формата".
func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// severitySummary возвращает краткую сводку severity: ERROR при наличии error,
// иначе WARNING при наличии warning, иначе OK (info не повышает статус).
func severitySummary(diagnostics []models.Diagnostic) string {
	warning := false
	for _, diagnostic := range diagnostics {
		switch diagnostic.Severity {
		case models.SeverityError:
			return "ERROR"
		case models.SeverityWarning:
			warning = true
		}
	}
	if warning {
		return "WARNING"
	}
	return "OK"
}

// yesNo форматирует bool русскими да/нет для human table-вывода.
func yesNo(value bool) string {
	if value {
		return "да"
	}
	return "нет"
}
