// Файл output_test.go фиксирует стабильную schema v1 machine output: точные
// JSON-ключи, заголовки и порядок колонок table/CSV/TSV и правила escaping.
// Тесты работают только с in-memory структурами: пакет output не выполняет I/O,
// реальный /etc/fstab не используется.
package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/transaction"
	"fstab-edit/pkg/models"
)

// listResultFixture собирает ListResult с активной записью полного набора полей
// и закомментированной записью без dump: обе границы optional-полей зафиксированы
// в одном fixture, чтобы тесты схемы не дублировали данные.
func listResultFixture() cliapp.ListResult {
	return cliapp.ListResult{
		Target: "/etc/fstab",
		Entries: []cliapp.EntryView{
			{
				Line:  1,
				State: "active",
				Source: cliapp.FieldView{
					Raw: "UUID=aaa", Decoded: "/dev/sda1", Present: true,
				},
				MountPoint: cliapp.FieldView{Raw: "/", Decoded: "/", Present: true},
				FSType:     cliapp.FieldView{Raw: "ext4", Decoded: "ext4", Present: true},
				Options: cliapp.FieldView{
					Raw: "defaults,noatime", Decoded: "defaults,noatime", Present: true,
				},
				Dump:    cliapp.FieldView{Raw: "0", Decoded: "0", Present: true},
				Pass:    cliapp.FieldView{Raw: "1", Decoded: "1", Present: true},
				Comment: "корневая файловая система",
				Diagnostics: []models.Diagnostic{
					{
						Code:     "options.duplicate",
						Severity: models.SeverityWarning,
						Line:     1,
						Field:    "options",
						Message:  "Повторяющаяся опция",
						Params:   map[string]string{"option": "noatime"},
					},
				},
			},
			{
				Line:  3,
				State: "commented",
				Source: cliapp.FieldView{
					Raw: "/dev/sdb1", Decoded: "/dev/sdb1", Present: true,
				},
				MountPoint:  cliapp.FieldView{Raw: "/data", Decoded: "/data", Present: true},
				FSType:      cliapp.FieldView{Raw: "xfs", Decoded: "xfs", Present: true},
				Options:     cliapp.FieldView{Raw: "defaults", Decoded: "defaults", Present: true},
				Dump:        cliapp.FieldView{Present: false},
				Pass:        cliapp.FieldView{Raw: "0", Decoded: "0", Present: true},
				Comment:     "отключено до проверки диска",
				Diagnostics: []models.Diagnostic{},
			},
		},
		Diagnostics: []models.Diagnostic{},
	}
}

// TestRenderListJSONSchema unmarshals корневой документ и проверяет стабильные
// ключи schema v1: schema_version, active/commented state, шесть raw/decoded
// полей с present, associated comment и non-nil diagnostics у каждой записи.
func TestRenderListJSONSchema(t *testing.T) {
	data, err := RenderList(listResultFixture(), FormatJSON)
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}

	var document struct {
		SchemaVersion int    `json:"schema_version"`
		Target        string `json:"target"`
		Entries       []struct {
			Line   int    `json:"line"`
			State  string `json:"state"`
			Source struct {
				Raw     string `json:"raw"`
				Decoded string `json:"decoded"`
				Present bool   `json:"present"`
			} `json:"source"`
			MountPoint struct {
				Raw     string `json:"raw"`
				Decoded string `json:"decoded"`
				Present bool   `json:"present"`
			} `json:"mount_point"`
			FSType struct {
				Raw     string `json:"raw"`
				Decoded string `json:"decoded"`
				Present bool   `json:"present"`
			} `json:"fs_type"`
			Options struct {
				Raw     string `json:"raw"`
				Decoded string `json:"decoded"`
				Present bool   `json:"present"`
			} `json:"options"`
			Dump struct {
				Raw     string `json:"raw"`
				Decoded string `json:"decoded"`
				Present bool   `json:"present"`
			} `json:"dump"`
			Pass struct {
				Raw     string `json:"raw"`
				Decoded string `json:"decoded"`
				Present bool   `json:"present"`
			} `json:"pass"`
			Comment     string              `json:"comment"`
			Diagnostics []models.Diagnostic `json:"diagnostics"`
		} `json:"entries"`
		Diagnostics []models.Diagnostic `json:"diagnostics"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("декодировать list JSON: %v", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == nil {
		t.Fatalf("stdout содержит больше одного JSON-документа")
	}

	if document.SchemaVersion != 1 {
		t.Errorf("schema_version: получено %d, ожидается 1", document.SchemaVersion)
	}
	if document.Target != "/etc/fstab" {
		t.Errorf("target: получено %q", document.Target)
	}
	if len(document.Entries) != 2 {
		t.Fatalf("entries: получено %d, ожидается 2", len(document.Entries))
	}

	active := document.Entries[0]
	if active.State != "active" {
		t.Errorf("state первой записи: получено %q", active.State)
	}
	if active.Source.Raw != "UUID=aaa" || active.Source.Decoded != "/dev/sda1" || !active.Source.Present {
		t.Errorf("source: %+v", active.Source)
	}
	if active.Options.Raw != "defaults,noatime" {
		t.Errorf("options.raw: получено %q", active.Options.Raw)
	}
	if active.Comment != "корневая файловая система" {
		t.Errorf("comment: получено %q", active.Comment)
	}
	if active.Diagnostics == nil || len(active.Diagnostics) != 1 {
		t.Fatalf("diagnostics первой записи: ожидается один элемент, получено %+v", active.Diagnostics)
	}
	if active.Diagnostics[0].Code != "options.duplicate" {
		t.Errorf("diagnostic code: получено %q", active.Diagnostics[0].Code)
	}

	commented := document.Entries[1]
	if commented.State != "commented" {
		t.Errorf("state второй записи: получено %q", commented.State)
	}
	if commented.Dump.Present {
		t.Errorf("dump.present: получено true, ожидается false")
	}
	if !commented.Pass.Present || commented.Pass.Decoded != "0" {
		t.Errorf("pass: %+v", commented.Pass)
	}
	if commented.Diagnostics == nil {
		t.Errorf("diagnostics второй записи: nil, ожидается non-nil пустой массив")
	}
	if document.Diagnostics == nil {
		t.Errorf("корневые diagnostics: nil, ожидается non-nil пустой массив")
	}
}

// TestRenderListTableHeaderAndMissingValues проверяет заголовок таблицы и
// значение "-" для отсутствующих optional-полей.
func TestRenderListTableHeaderAndMissingValues(t *testing.T) {
	data, err := RenderList(listResultFixture(), FormatTable)
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	text := string(data)

	header := "LINE  STATE      SOURCE     MOUNT  TYPE  OPTIONS           DUMP  PASS  STATUS"
	if !strings.Contains(text, header) {
		t.Errorf("таблица не содержит заголовок %q:\n%s", header, text)
	}
	for _, column := range []string{"LINE", "STATE", "SOURCE", "MOUNT", "TYPE", "OPTIONS", "DUMP", "PASS", "STATUS"} {
		if !strings.Contains(text, column) {
			t.Errorf("таблица не содержит колонку %q:\n%s", column, text)
		}
	}
	if !strings.Contains(text, "-     0     OK") {
		t.Errorf("missing dump должен отображаться как '-':\n%s", text)
	}
	if !strings.Contains(text, "WARNING") {
		t.Errorf("status должен содержать severity summary WARNING:\n%s", text)
	}
}

// TestRenderListCSVExactHeader фиксирует точный порядок и имена CSV-колонок.
func TestRenderListCSVExactHeader(t *testing.T) {
	data, err := RenderList(listResultFixture(), FormatCSV)
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("строк CSV: получено %d, ожидается 3:\n%s", len(lines), data)
	}
	expected := "line,state,source_raw,source,mount_point_raw,mount_point," +
		"fs_type_raw,fs_type,options_raw,options,dump_raw,dump,pass_raw,pass,comment"
	if lines[0] != expected {
		t.Errorf("CSV header:\nполучено  %q\nожидается %q", lines[0], expected)
	}
	if !strings.Contains(lines[2], "commented") {
		t.Errorf("вторая запись не найдена в CSV:\n%s", lines[2])
	}
}

// TestRenderListTSVEscaping фиксирует документированный backslash-escaping TSV:
// tab → \t, newline → \n, backslash → \\.
func TestRenderListTSVEscaping(t *testing.T) {
	result := listResultFixture()
	result.Entries[0].Options = cliapp.FieldView{
		Raw: "a\tb\nc\\d", Decoded: "a\tb\nc\\d", Present: true,
	}
	data, err := RenderList(result, FormatTSV)
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `a\tb\nc\\d`) {
		t.Errorf("TSV escaping: ожидается %q в выводе:\n%s", `a\tb\nc\\d`, text)
	}
	if strings.Contains(text, "a\tb") {
		t.Errorf("TSV содержит неэкранированный tab:\n%s", text)
	}
}

// TestRenderDiagnosticsCSVHeader проверяет отдельные колонки диагностик и
// сериализацию params через encoding/json.
func TestRenderDiagnosticsCSVHeader(t *testing.T) {
	diagnostics := []models.Diagnostic{
		{
			Code:     "options.duplicate",
			Severity: models.SeverityWarning,
			Line:     5,
			Field:    "options",
			Message:  "Повторяющаяся опция",
			Params:   map[string]string{"option": "noatime"},
		},
	}
	data, err := RenderDiagnostics(diagnostics, FormatCSV)
	if err != nil {
		t.Fatalf("RenderDiagnostics: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("строк CSV: получено %d, ожидается 2:\n%s", len(lines), data)
	}
	if lines[0] != "line,severity,code,field,message,params_json" {
		t.Errorf("header diagnostics CSV: %q", lines[0])
	}
	// params_json — JSON внутри CSV-ячейки, поэтому парсим запись через encoding/csv:
	// стандартный quoting обязан корректно вернуть исходный JSON-объект.
	reader := csv.NewReader(strings.NewReader(string(data)))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("прочитать CSV: %v", err)
	}
	if len(records) != 2 || records[1][5] != `{"option":"noatime"}` {
		t.Errorf("params_json не сериализованы через encoding/json: %+v", records)
	}
}

// TestRenderDiagnosticsTSVHeader проверяет те же колонки для TSV с escaping.
func TestRenderDiagnosticsTSVHeader(t *testing.T) {
	diagnostics := []models.Diagnostic{
		{
			Code:     "mount.missing",
			Severity: models.SeverityError,
			Line:     2,
			Message:  "Точка\tмонтирования",
		},
	}
	data, err := RenderDiagnostics(diagnostics, FormatTSV)
	if err != nil {
		t.Fatalf("RenderDiagnostics: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "line\tseverity\tcode\tfield\tmessage\tparams_json\n") {
		t.Errorf("header diagnostics TSV: %q", text)
	}
	if !strings.Contains(text, `Точка\tмонтирования`) {
		t.Errorf("TSV diagnostics escaping: ожидается экранированный tab:\n%s", text)
	}
}

// TestRenderDiagnosticsJSONDocument проверяет корневой JSON-документ диагностик.
func TestRenderDiagnosticsJSONDocument(t *testing.T) {
	data, err := RenderDiagnostics(nil, FormatJSON)
	if err != nil {
		t.Fatalf("RenderDiagnostics: %v", err)
	}
	var document struct {
		SchemaVersion int                 `json:"schema_version"`
		Diagnostics   []models.Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("декодировать diagnostics JSON: %v", err)
	}
	if document.SchemaVersion != 1 {
		t.Errorf("schema_version: %d", document.SchemaVersion)
	}
	if document.Diagnostics == nil {
		t.Errorf("diagnostics: nil, ожидается non-nil пустой массив")
	}
}

// mutationPreviewFixture собирает preview мутации без обращения к parser/editor:
// renderers принимают готовые доменные результаты.
func mutationPreviewFixture() *cliapp.MutationPreview {
	return &cliapp.MutationPreview{
		Changed:   true,
		Operation: backup.OperationBeforeAdd,
		Diff:      "--- <original>\n+++ <candidate>\n@@ -1,1 +1,2 @@\n /dev/sda1 / ext4 defaults 0 1\n+/dev/sdb1 /data xfs defaults 0 2\n",
		Report: models.Report{
			Diagnostics: []models.Diagnostic{
				{Code: "entry.added", Severity: models.SeverityInfo, Message: "Запись добавлена"},
			},
		},
	}
}

// TestRenderMutationJSONSchema проверяет ключи mutation JSON и отсутствие
// backup_id без выполненной transaction.
func TestRenderMutationJSONSchema(t *testing.T) {
	data, err := RenderMutation(mutationPreviewFixture(), "/etc/fstab", true, nil, FormatJSON)
	if err != nil {
		t.Fatalf("RenderMutation: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("декодировать mutation JSON: %v", err)
	}
	for _, key := range []string{"schema_version", "operation", "changed", "dry_run", "target", "diff", "diagnostics"} {
		if _, ok := document[key]; !ok {
			t.Errorf("mutation JSON не содержит ключ %q", key)
		}
	}
	if _, ok := document["backup_id"]; ok {
		t.Errorf("backup_id должен отсутствовать без transaction")
	}
	if _, ok := document["rollback"]; ok {
		t.Errorf("rollback должен отсутствовать без transaction")
	}

	var operation string
	if err := json.Unmarshal(document["operation"], &operation); err != nil {
		t.Fatalf("operation: %v", err)
	}
	if operation != "before-add" {
		t.Errorf("operation: получено %q", operation)
	}
	var dryRun bool
	if err := json.Unmarshal(document["dry_run"], &dryRun); err != nil {
		t.Fatalf("dry_run: %v", err)
	}
	if !dryRun {
		t.Errorf("dry_run: получено false")
	}
	var diff string
	if err := json.Unmarshal(document["diff"], &diff); err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(diff, "+/dev/sdb1") {
		t.Errorf("diff не содержит добавленную строку: %q", diff)
	}
}

// TestRenderMutationJSONWithTransaction проверяет backup_id и встроенный
// documented struct rollback после выполненной transaction.
func TestRenderMutationJSONWithTransaction(t *testing.T) {
	executed := &transaction.TransactionResult{
		Changed:    true,
		TargetPath: "/etc/fstab",
		RolledBack: true,
		Backup:     backup.Backup{ID: "2026-11-20_10-00-00.000000000Z"},
	}
	data, err := RenderMutation(mutationPreviewFixture(), "/etc/fstab", false, executed, FormatJSON)
	if err != nil {
		t.Fatalf("RenderMutation: %v", err)
	}
	var document struct {
		BackupID string `json:"backup_id"`
		Changed  bool   `json:"changed"`
		DryRun   bool   `json:"dry_run"`
		Rollback *struct {
			RolledBack bool   `json:"rolled_back"`
			BackupID   string `json:"backup_id"`
		} `json:"rollback"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("декодировать mutation JSON: %v", err)
	}
	if document.BackupID != "2026-11-20_10-00-00.000000000Z" {
		t.Errorf("backup_id: получено %q", document.BackupID)
	}
	if document.DryRun {
		t.Errorf("dry_run: получено true")
	}
	if document.Rollback == nil {
		t.Fatalf("rollback struct отсутствует")
	}
	if !document.Rollback.RolledBack {
		t.Errorf("rollback.rolled_back: получено false")
	}
	if document.Rollback.BackupID != "2026-11-20_10-00-00.000000000Z" {
		t.Errorf("rollback.backup_id: получено %q", document.Rollback.BackupID)
	}
}

// TestRenderMutationTableSummary проверяет человекочитаемый summary и diff.
func TestRenderMutationTableSummary(t *testing.T) {
	data, err := RenderMutation(mutationPreviewFixture(), "/etc/fstab", true, nil, FormatTable)
	if err != nil {
		t.Fatalf("RenderMutation: %v", err)
	}
	text := string(data)
	for _, fragment := range []string{"before-add", "/etc/fstab", "+/dev/sdb1"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("table mutation не содержит %q:\n%s", fragment, text)
		}
	}
}

// TestRenderBackupJSONSchema проверяет ключи backup JSON.
func TestRenderBackupJSONSchema(t *testing.T) {
	created := backup.Backup{
		ID:           "2026-11-20_10-00-00.000000000Z",
		Path:         "/etc/fstab.bak.2026-11-20_10-00-00.000000000Z",
		MetadataPath: "/etc/fstab.bak.2026-11-20_10-00-00.000000000Z.json",
	}
	data, err := RenderBackup(created, false, FormatJSON)
	if err != nil {
		t.Fatalf("RenderBackup: %v", err)
	}
	var document struct {
		SchemaVersion int    `json:"schema_version"`
		BackupID      string `json:"backup_id"`
		DataPath      string `json:"data_path"`
		MetadataPath  string `json:"metadata_path"`
		DryRun        bool   `json:"dry_run"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("декодировать backup JSON: %v", err)
	}
	if document.SchemaVersion != 1 {
		t.Errorf("schema_version: %d", document.SchemaVersion)
	}
	if document.BackupID != created.ID {
		t.Errorf("backup_id: %q", document.BackupID)
	}
	if document.DataPath != created.Path {
		t.Errorf("data_path: %q", document.DataPath)
	}
	if document.MetadataPath != created.MetadataPath {
		t.Errorf("metadata_path: %q", document.MetadataPath)
	}
	if document.DryRun {
		t.Errorf("dry_run: получено true")
	}
}

// TestRenderRollbackJSONSchema проверяет selected/safety backup IDs и post verification.
func TestRenderRollbackJSONSchema(t *testing.T) {
	info := RollbackInfo{
		SelectedBackupID: "2026-11-19_09-00-00.000000000Z",
		SafetyBackupID:   "2026-11-20_10-00-00.000000000Z",
		DryRun:           true,
		PostVerification: models.Report{
			Diagnostics: []models.Diagnostic{
				{Code: "verify.ok", Severity: models.SeverityInfo, Message: "Проверка пройдена"},
			},
		},
	}
	data, err := RenderRollback(info, FormatJSON)
	if err != nil {
		t.Fatalf("RenderRollback: %v", err)
	}
	var document struct {
		SchemaVersion    int    `json:"schema_version"`
		SelectedBackupID string `json:"selected_backup_id"`
		SafetyBackupID   string `json:"safety_backup_id"`
		DryRun           bool   `json:"dry_run"`
		PostVerification struct {
			Diagnostics []models.Diagnostic `json:"diagnostics"`
		} `json:"post_verification"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("декодировать rollback JSON: %v", err)
	}
	if document.SchemaVersion != 1 {
		t.Errorf("schema_version: %d", document.SchemaVersion)
	}
	if document.SelectedBackupID != info.SelectedBackupID {
		t.Errorf("selected_backup_id: %q", document.SelectedBackupID)
	}
	if document.SafetyBackupID != info.SafetyBackupID {
		t.Errorf("safety_backup_id: %q", document.SafetyBackupID)
	}
	if !document.DryRun {
		t.Errorf("dry_run: получено false")
	}
	if len(document.PostVerification.Diagnostics) != 1 {
		t.Errorf("post_verification.diagnostics: %+v", document.PostVerification.Diagnostics)
	}
}

// TestRenderRollbackMachineFormats проверяет стабильные колонки CSV/TSV rollback.
func TestRenderRollbackMachineFormats(t *testing.T) {
	info := RollbackInfo{
		SelectedBackupID: "selected",
		SafetyBackupID:   "safety",
	}
	csvData, err := RenderRollback(info, FormatCSV)
	if err != nil {
		t.Fatalf("RenderRollback csv: %v", err)
	}
	firstLine, _, _ := strings.Cut(string(csvData), "\n")
	if firstLine != "selected_backup_id,safety_backup_id,dry_run,post_status" {
		t.Errorf("rollback CSV header: %q", firstLine)
	}
	tsvData, err := RenderRollback(info, FormatTSV)
	if err != nil {
		t.Fatalf("RenderRollback tsv: %v", err)
	}
	firstLine, _, _ = strings.Cut(string(tsvData), "\n")
	if firstLine != "selected_backup_id\tsafety_backup_id\tdry_run\tpost_status" {
		t.Errorf("rollback TSV header: %q", firstLine)
	}
}

// TestRenderBackupMachineFormats проверяет колонки CSV/TSV backup.
func TestRenderBackupMachineFormats(t *testing.T) {
	created := backup.Backup{ID: "id-1", Path: "/tmp/data", MetadataPath: "/tmp/data.json"}
	csvData, err := RenderBackup(created, true, FormatCSV)
	if err != nil {
		t.Fatalf("RenderBackup csv: %v", err)
	}
	firstLine, _, _ := strings.Cut(string(csvData), "\n")
	if firstLine != "backup_id,data_path,metadata_path,dry_run" {
		t.Errorf("backup CSV header: %q", firstLine)
	}
	if !strings.Contains(string(csvData), "true") {
		t.Errorf("dry_run=true не отображён:\n%s", csvData)
	}
	tsvData, err := RenderBackup(created, false, FormatTSV)
	if err != nil {
		t.Fatalf("RenderBackup tsv: %v", err)
	}
	firstLine, _, _ = strings.Cut(string(tsvData), "\n")
	if firstLine != "backup_id\tdata_path\tmetadata_path\tdry_run" {
		t.Errorf("backup TSV header: %q", firstLine)
	}
}

// TestUnknownFormatRejected проверяет, что неизвестный формат — ошибка, а не
// молчаливый fallback: cmd layer отобразит её в usage error code 2.
func TestUnknownFormatRejected(t *testing.T) {
	if _, err := RenderList(cliapp.ListResult{}, Format("yaml")); err == nil {
		t.Errorf("неизвестный формат list: ожидается ошибка")
	}
	if _, err := RenderDiagnostics(nil, Format("yaml")); err == nil {
		t.Errorf("неизвестный формат diagnostics: ожидается ошибка")
	}
	if _, err := RenderMutation(nil, "/etc/fstab", false, nil, Format("yaml")); err == nil {
		t.Errorf("неизвестный формат mutation: ожидается ошибка")
	}
	if _, err := RenderBackup(backup.Backup{}, false, Format("yaml")); err == nil {
		t.Errorf("неизвестный формат backup: ожидается ошибка")
	}
	if _, err := RenderRollback(RollbackInfo{}, Format("yaml")); err == nil {
		t.Errorf("неизвестный формат rollback: ожидается ошибка")
	}
}

// TestMachineOutputIsSingleJSONDocument проверяет чистоту machine stdout:
// ровно один JSON-документ без prompts и log-сообщений.
func TestMachineOutputIsSingleJSONDocument(t *testing.T) {
	renderers := map[string]func() ([]byte, error){
		"list":        func() ([]byte, error) { return RenderList(listResultFixture(), FormatJSON) },
		"diagnostics": func() ([]byte, error) { return RenderDiagnostics(nil, FormatJSON) },
		"mutation": func() ([]byte, error) {
			return RenderMutation(mutationPreviewFixture(), "/etc/fstab", true, nil, FormatJSON)
		},
		"backup": func() ([]byte, error) {
			return RenderBackup(backup.Backup{ID: "id"}, false, FormatJSON)
		},
		"rollback": func() ([]byte, error) {
			return RenderRollback(RollbackInfo{SelectedBackupID: "sel", SafetyBackupID: "safe"}, FormatJSON)
		},
	}
	for name, render := range renderers {
		data, err := render()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Errorf("%s: stdout не является JSON-документом: %v", name, err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); err == nil {
			t.Errorf("%s: stdout содержит более одного JSON-документа", name)
		}
	}
}

// TestRenderListCSVMissingFieldsValues проверяет, что отсутствующие optional-поля
// дают "-" в ячейках CSV, а не пустые строки.
func TestRenderListCSVMissingFieldsValues(t *testing.T) {
	data, err := RenderList(listResultFixture(), FormatCSV)
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	reader := csv.NewReader(strings.NewReader(string(data)))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("прочитать CSV: %v", err)
	}
	commented := records[2]
	// dump_raw и dump — колонки 10 и 11 (нумерация с нуля).
	if commented[10] != "-" || commented[11] != "-" {
		t.Errorf("missing dump в CSV: %+v", commented)
	}
	if commented[1] != "commented" {
		t.Errorf("state в CSV: %q", commented[1])
	}
}

// TestSeveritySummary проверяет приоритет severity: error побеждает warning,
// info статус не повышает.
func TestSeveritySummary(t *testing.T) {
	cases := []struct {
		name        string
		diagnostics []models.Diagnostic
		expected    string
	}{
		{"empty", nil, "OK"},
		{"info only", []models.Diagnostic{{Severity: models.SeverityInfo}}, "OK"},
		{"warning", []models.Diagnostic{{Severity: models.SeverityWarning}}, "WARNING"},
		{"error beats warning", []models.Diagnostic{
			{Severity: models.SeverityWarning},
			{Severity: models.SeverityError},
		}, "ERROR"},
	}
	for _, testCase := range cases {
		if actual := severitySummary(testCase.diagnostics); actual != testCase.expected {
			t.Errorf("%s: получено %q, ожидается %q", testCase.name, actual, testCase.expected)
		}
	}
}

// TestRenderMutationNilPreview проверяет, что nil preview — ошибка при любом
// формате, а не panic или пустой вывод.
func TestRenderMutationNilPreview(t *testing.T) {
	for _, format := range []Format{FormatJSON, FormatTable, FormatCSV, FormatTSV} {
		if _, err := RenderMutation(nil, "/etc/fstab", false, nil, format); err == nil {
			t.Errorf("nil preview при формате %s: ожидается ошибка", format)
		}
	}
}

// TestEscapeTSVRoundTrip проверяет обратимость escaping: смешанные
// последовательности экранируются без перекрытий.
func TestEscapeTSVRoundTrip(t *testing.T) {
	value := "a\\b\tc\nd\\\\e"
	escaped := escapeTSV(value)
	expected := `a\\b\tc\nd\\\\e`
	if escaped != expected {
		t.Errorf("escapeTSV: получено %q, ожидается %q", escaped, expected)
	}
	if escapeTSV("plain") != "plain" {
		t.Errorf("plain значение не должно изменяться")
	}
}
