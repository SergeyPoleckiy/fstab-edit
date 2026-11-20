# CLI Commands, Formats and Exit Codes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** реализовать полноценный CLI `fstab-edit`: десять команд, селекторы, dry-run, confirmations, unified diff, table/JSON/CSV/TSV output и стабильные exit codes 0–8.

**Architecture:** доменный слой `internal/cliapp` работает с `parser/editor/backup/transaction` и не зависит от Cobra; `cmd` содержит только Cobra commands, flags, prompt wiring и exit mapping; `internal/output` сериализует стабильные schema v1; `internal/diff` строит unified diff по строкам. Зависимости ввода-вывода, transaction и runner передаются через immutable structs, что позволяет E2E tests использовать temp files, fake runner и scripted stdin без реального `/etc/fstab`, findmnt или TTY.

**Tech Stack:** Go 1.22+, `github.com/spf13/cobra v1.8.1`, стандартные `encoding/csv`, `encoding/json`, `text/tabwriter`, существующие внутренние пакеты; новых global state нет.

## Global Constraints

- Рабочая область выполнения: `/home/sergey/projects/fstab-editor/work/.worktrees/stage-1-cli`.
- Модуль `fstab-edit`, Go `1.22.0`; все файлы, публичные API и нетривиальная логика получают максимальные содержательные русские comments по ТЗ 4.3.
- Реальный `/etc/fstab` не читается и не изменяется в tests. Все файловые tests используют `t.TempDir`; runner tests используют fake `sysutil.CommandRunner`; prompt/TTY подменяются interfaces/functions.
- Default target в production — `/etc/fstab`; `--file=<path>` переопределяет его. `--help` и `--version` не читают target и не запускают `findmnt`.
- Поддерживаются команды `list`, `add`, `edit`, `delete`, `comment`, `uncomment`, `validate`, `verify-mount`, `backup`, `rollback`.
- Форматы output: `table`, `json`, `csv`, `tsv`; неизвестный format — usage error.
- Глобальные flags: `--file`, `--dry-run`, `--yes/-y`, `--output/-o`, `--log-level`, `--verbose/-v`, `--quiet/-q`, `--backup-dir`, `--offline`, `--lang`, `--config`, `--help/-h`, `--version`. `--verbose` и `--quiet` несовместимы; неизвестный level/lang — usage error.
- Флаги `--log-level`, `--lang` и `--config` в этом блоке только валидируются/сохраняются; logger, config loader и полные RU/EN catalogs реализуются следующим блоком. Текущие пользовательские сообщения — на русском, это README обязан зафиксировать.
- Positional `<id>` и явные `--line/--mount-point/--device` взаимоисключающи; ровно один способ выбора записи. Сравнение selector — полное decoded value, без substring и symlink resolution.
- Несколько совпадений selector → exit code 8 и список candidate lines. Interactive выбор допускается только при TTY и без `--yes`; `--yes` никогда не выбирает произвольный кандидат.
- `add` требует `--device`, `--mount`, `--type`, `--options`; default dump/pass — `"0"`. `edit` требует selector и хотя бы один изменяющий flag; обязательные поля нельзя сделать пустыми; `--comment` и `--clear-comment` несовместимы.
- Dry-run не меняет target, не создаёт backup/lock и не требует `--yes`; показывает diff и diagnostics.
- Мутации add/edit/delete/comment/uncomment и rollback требуют confirmation `[y/N]` до mutation, если не задан `--yes`; без TTY и без `--yes` — usage error. Default answer — No.
- Diff — unified; headers различают original и candidate; неизменённые строки не переформатируются ради diff.
- Обычные данные — stdout; диагностика и prompt — stderr. Для machine formats stdout содержит ровно один structured document; diff и diagnostics встроены в JSON schema и не печатаются отдельно.
- JSON root содержит `schema_version: 1`. Entry schema содержит line/state, raw и decoded для шести полей, наличие optional-полей, associated comment и diagnostics. Mutation schema содержит operation/changed/dry_run/target/diff/diagnostics/backup_id и rollback result при наличии.
- CSV/TSV имеют стабильную первую строку. TSV документированно экранирует `\`, tab, CR и LF обратной косой чертой без потери обратимости. Diagnostics не добавляются как псевдозаписи fstab.
- Exit codes: 0 success (warnings допустимы), 1 validation/check error, 2 usage, 3 permissions, 4 lock timeout/base conflict, 5 external utility error, 6 I/O/backup/metadata/write/rotation-after-success, 7 critical rollback failure, 8 ambiguous selector. Rollback failure имеет highest priority.
- Manual backup не изменяет target и не требует mutation prompt. Manual rollback сначала выбирает копию до safety backup, затем создаёт safety copy `before-rollback`, атомарно восстанавливает выбранную копию и выполняет post-check.
- Тесты не должны требовать root, реальный findmnt process, TTY или изменения домашнего каталога.

---

## Структура файлов и внешние контракты

- `internal/diff/diff.go` — line-based unified diff с LCS и stable context.
- `internal/diff/diff_test.go` — exact unified output, no-final-newline и unchanged cases.
- `internal/cliapp/types.go` — EntryView, diagnostics rows, result structs, ExitError, failure-kind mapping.
- `internal/cliapp/types_test.go` — stable exit codes, schema basics.
- `internal/cliapp/select.go` — selector request validation, document resolution, interactive ambiguity chooser.
- `internal/cliapp/select_test.go` — positional/explicit exclusivity, ambiguity, interactive choice, --yes запрет.
- `internal/cliapp/mutation.go` — Add/Edit/Delete/Comment/Uncomment request → candidate bytes, changed, diff, diagnostics.
- `internal/cliapp/mutation_test.go` — operation semantics, no-op и invalid usage.
- `internal/cliapp/service.go` — production dependencies, list/validate/verify/mutation execution.
- `internal/cliapp/service_test.go` — fake dependencies integration.
- `internal/cliapp/rollback.go` — manual backup и rollback algorithms.
- `internal/cliapp/rollback_test.go` — dry-run, safety-before-restore, restore target, post-check.
- `internal/output/output.go` — table/json/csv/tsv renderers.
- `internal/output/output_test.go` — exact schema/columns/escaping.
- `cmd/app.go` — App, Cobra root, global flags, exit wrapper.
- `cmd/list.go`, `cmd/mutation.go`, `cmd/validate.go`, `cmd/backup.go`, `cmd/rollback.go` — thin command handlers.
- `cmd/*_test.go` — CLI E2E через temp files, fake runner, scripted stdin, memory buffers.
- `main.go` — production wiring.
- `go.mod`, `go.sum` — `github.com/spf13/cobra v1.8.1`.
- `README.md` — CLI статус, ограничения deferred config/logger/i18n, examples.

Ключевые exported types:

```go
type ExitError struct {
	Code        int
	Message     string
	Err         error
	Candidates  []int
	Diagnostics []models.Diagnostic
}
func (e *ExitError) Error() string
func (e *ExitError) Unwrap() error
func ExitCodeForFailureKind(kind transaction.FailureKind) int

type FieldView struct {
	Raw     string `json:"raw"`
	Decoded string `json:"decoded"`
	Present bool   `json:"present"`
}
type EntryView struct {
	Line        int                  `json:"line"`
	State       string               `json:"state"`
	Source      FieldView            `json:"source"`
	MountPoint  FieldView            `json:"mount_point"`
	FSType      FieldView            `json:"fs_type"`
	Options     FieldView            `json:"options"`
	Dump        FieldView            `json:"dump"`
	Pass        FieldView            `json:"pass"`
	Comment     string               `json:"comment,omitempty"`
	Diagnostics []models.Diagnostic  `json:"diagnostics"`
}
type ListResult struct {
	Target      string              `json:"target"`
	Entries     []EntryView         `json:"entries"`
	Diagnostics []models.Diagnostic `json:"diagnostics"`
}

type MutationKind string
const (
	MutationAdd MutationKind = "add"
	MutationEdit MutationKind = "edit"
	MutationDelete MutationKind = "delete"
	MutationComment MutationKind = "comment"
	MutationUncomment MutationKind = "uncomment"
)
type MutationRequest struct {
	Kind     MutationKind
	Selector editor.Selector
	NewEntry editor.NewEntry
	Changes  editor.Changes
}
type MutationPreview struct {
	Operation   backup.Operation
	Changed     bool
	Target      string
	Candidate   []byte
	Diff        string
	Diagnostics []models.Diagnostic
}

type Dependencies struct {
	ReadSnapshot       func(string) (writer.Snapshot, error)
	ExecuteTransaction func(context.Context, transaction.Plan, transaction.Options) (transaction.TransactionResult, error)
	CreateBackup       func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error)
	SelectBackup       func(string, string, string, string) (backup.Selection, error)
	OpenBackup         func(string) (backup.Backup, error)
	Replace            func(string, []byte) (writer.ReplaceResult, error)
	VerifyTarget       func(context.Context, sysutil.CommandRunner, string) sysutil.Verification
}
func ProductionDependencies(runner sysutil.CommandRunner) Dependencies
type Service struct { /* Dependencies + Runner */ }
func (s *Service) List(target string, offline bool) (ListResult, error)
func (s *Service) Validate(ctx context.Context, target string, offline bool) (ValidationResult, error)
func (s *Service) VerifyMount(ctx context.Context, target string) (sysutil.Verification, error)
func (s *Service) RunMutation(ctx context.Context, target string, preview MutationPreview, options transaction.Options) (transaction.TransactionResult, error)
func (s *Service) Backup(target string, request BackupRequest) (BackupResult, error)
func (s *Service) Rollback(ctx context.Context, target string, request RollbackRequest) (RollbackResult, error)

type AppOptions struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Stdin   io.Reader
	Version string
	Runner  sysutil.CommandRunner
	TTY     func() bool
	Chooser cliapp.Chooser
}
type App struct { /* options + Service */ }
func NewApp(options AppOptions) *App
func (a *App) Execute(args []string) (code int, err error)
```

### Task 1: Diff, selector resolution и mutation preview

**Files:**
- Create: `internal/diff/diff.go`
- Create: `internal/diff/diff_test.go`
- Create: `internal/cliapp/types.go`
- Create: `internal/cliapp/select.go`
- Create: `internal/cliapp/mutation.go`
- Test: `internal/cliapp/select_test.go`, `internal/cliapp/mutation_test.go`, `internal/cliapp/types_test.go`

**Interfaces:**
- Consumes: `parser.Parse`, `editor`, `validator`, `models`, `transaction.FailureKind`.
- Produces: `ExitError`, `ExitCodeForFailureKind`, selector resolution, `MutationPreview` для Tasks 2–4.

- [ ] **Step 1: Написать failing diff tests**

`Unified(originalName, candidateName string, original, candidate []byte) string` использует line-based LCS. Required cases:

```go
func TestUnifiedDiffAddAndContext(t *testing.T) {
	got := Unified("a", "b", []byte("one\ntwo\n"), []byte("one\nTWO\nthree\n"))
	if !strings.Contains(got, "--- a") || !strings.Contains(got, "+++ b") {
		t.Fatalf("headers missing: %q", got)
	}
	if !strings.Contains(got, "-two") || !strings.Contains(got, "+TWO") || !strings.Contains(got, "+three") {
		t.Fatalf("hunk missing: %q", got)
	}
}
func TestUnifiedDiffNoFinalNewline(t *testing.T) {
	got := Unified("a", "b", []byte("x"), []byte("y"))
	if !strings.Contains(got, `\ No newline at end of file`) {
		t.Fatalf("missing no-newline marker: %q", got)
	}
}
func TestUnifiedDiffUnchanged(t *testing.T) {
	if got := Unified("a", "b", []byte("x\n"), []byte("x\n")); got != "" {
		t.Fatalf("unchanged diff: %q", got)
	}
}
```

Run: `go test ./internal/diff -v`
Expected: FAIL, package missing.

- [ ] **Step 2: Реализовать Unified**

Разбей bytes на lines с сохранением признака final newline. LCS по строкам без endings; changed ranges объединяются в hunks с 3 строками context. Формат hunk `@@ -l,n +l,n @@`. Для замен выводится `-old` затем `+new`; для добавлений/удалений — соответствующие маркеры. У последней строки без final newline после вывода добавляется `\ No newline at end of file`. Функция не мутирует inputs и не использует временные файлы.

Run: `go test ./internal/diff -v`
Expected: PASS.

- [ ] **Step 3: Написать failing selector/mutation tests**

Selector tests:

1. `SelectorFromValues(positional,line,mount,source)` возвращает usage `*ExitError{Code:2}`, если задано более одного способа или ни одного для command, требующего selector.
2. Positional `"12"` → `editor.ByLine(12)`; `"12x"` → попытка line parse, затем `ByMountPoint("12x")` (full decoded value).
3. `Resolve(document, selector, chooser, interactive)` возвращает index; `editor.ErrAmbiguous` без interactive/with nil chooser → `*ExitError{Code:8, Candidates:lines}`. Interactive chooser, выбравший одну candidate line, возвращает её index; выбор посторонней строки — code8.
4. `--yes` semantics покрывается на уровне chooser: chooser не вызывается, если interactive=false.

Mutation tests:

```go
func TestPrepareAddProducesDiffAndDiagnostics(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	preview, err := PrepareMutation(document, MutationRequest{
		Kind: MutationAdd,
		NewEntry: editor.NewEntry{Source:"/dev/sdb1", MountPoint:"/mnt/data", FSType:"ext4", Options:"defaults,noatime"},
	})
	if err != nil { t.Fatal(err) }
	if !preview.Changed || preview.Operation != backup.OperationBeforeAdd { t.Fatalf("%+v", preview) }
	if preview.Diff == "" || !bytes.Contains(preview.Candidate, []byte("/mnt/data")) { t.Fatalf("preview incomplete") }
}
```

Cases: edit exact field preserves unrelated formatting; delete; comment active; uncomment commented; wrong state returns validation ExitError code1; editor.ErrAmbiguous code8; no-op edit returns Changed=false, empty diff, candidate=original; invalid change code1; add required fields empty code1.

Run: `go test ./internal/cliapp -run='Test(Selector|PrepareMutation)' -v`
Expected: FAIL, package missing.

- [ ] **Step 4: Реализовать types/select/mutation**

`ExitCodeForFailureKind`: validation→1, conflict→4, external→5, io→6, rollback→7, rotation→6. `ExitError.Error()` русское сообщение, `Unwrap` сохраняет cause. `UsageError`, `ValidationError`, `AmbiguityError`, `PermissionError` helpers заполняют Code.

`PrepareMutation` копирует document через повторный `parser.Parse(document.Serialize())`, применяет ровно одну editor operation согласно Kind, вычисляет candidate bytes, diff `Unified("<original>", "<candidate>", ...)`, `validator.Validate(candidateDoc)` и operation mapping. Ошибки editor map: ErrAmbiguous→code8, ErrNotFound/ErrWrongState/ErrInvalidChange/ErrEmptyField/parser/Encode errors→code1. Candidate slice копируется.

Run: `gofmt -w internal/diff internal/cliapp && go test ./internal/diff ./internal/cliapp -v`
Expected: PASS.

- [ ] **Step 5: Проверки и commit**

```bash
go test ./... && go test -race ./internal/diff ./internal/cliapp && go vet ./...
```

```bash
git add internal/diff internal/cliapp
git commit -m "feat: add cli mutation preparation"
```

### Task 2: Output formats schema v1

**Files:**
- Create: `internal/output/output.go`
- Create: `internal/output/output_test.go`

**Interfaces:**
- Consumes: `cliapp.ListResult`, `MutationPreview`, `transaction.TransactionResult`, diagnostics rows.
- Produces: `RenderList`, `RenderDiagnostics`, `RenderMutation`, `RenderBackup`, `RenderRollback` для cmd layer.

- [ ] **Step 1: Написать failing output tests**

JSON list test unmarshals root `schema_version == 1`; entries содержат active/commented, six raw/decoded fields, `dump.present=false`, `pass.present=true`, associated comment, diagnostics array. Table test содержит header `LINE`, `STATE`, `SOURCE`, `MOUNT`, `TYPE`, `OPTIONS`, `DUMP`, `PASS`, `STATUS` и значения `-` для missing optional fields. CSV test exact header:

```text
line,state,source_raw,source,mount_point_raw,mount_point,fs_type_raw,fs_type,options_raw,options,dump_raw,dump,pass_raw,pass,comment
```

TSV тест проверяет escaping: raw value `a\tb` сериализуется как `a\\tb`, newline как `\\n`, backslash как `\\\\`. Mutation JSON содержит `operation`, `changed`, `dry_run`, `target`, `diff`, `diagnostics`, `backup_id,omitempty`; transaction rollback result встраивается как documented struct. Backup JSON содержит `backup_id`, `data_path`, `metadata_path`, `dry_run`; rollback JSON содержит selected/safety backup IDs и post verification.

Run: `go test ./internal/output -v`
Expected: FAIL, package missing.

- [ ] **Step 2: Реализовать renderers**

JSON structs объявляются в `internal/output` с snake_case tags и `Diagnostics non-nil`. Table через `tabwriter`; `state` values `active`/`commented`; status column содержит краткую severity summary (`OK`, `WARNING`, `ERROR`). CSV через `encoding/csv`; TSV вручную с documented escaping через backslash. Diagnostics CSV/TSV имеют отдельные колонки `line,severity,code,field,message,params_json`, params сериализуются через `encoding/json`.

Для machine formats функции возвращают только structured bytes; prompts/log не добавляются. Для table mutation renderer возвращает human summary + diff; cmd layer печатает его stdout.

Run: `gofmt -w internal/output && go test ./internal/output -v`
Expected: PASS.

- [ ] **Step 3: Проверки и commit**

```bash
go test ./... && go vet ./...
git add internal/output
git commit -m "feat: add cli output formats"
```

### Task 3: Service read-only operations, mutation execution и manual rollback

**Files:**
- Create: `internal/cliapp/service.go`
- Create: `internal/cliapp/rollback.go`
- Test: `internal/cliapp/service_test.go`, `internal/cliapp/rollback_test.go`

**Interfaces:**
- Consumes: Task 1 preview, Task 2 views, `backup`, `transaction`, `writer`, `sysutil`.
- Produces: `Service`, production dependencies, list/validate/verify/backup/rollback results для cmd layer.

- [ ] **Step 1: Написать failing service tests**

List: temp target, service.List returns target path, active+commented entries, diagnostics; document не изменяется. Validate offline: internal diagnostics only, runner calls=0. Validate online: `sysutil.VerifyCandidate` path, internal+external diagnostics; external error maps code1. VerifyMount uses `FindmntVerifier` on actual target bytes.

Mutation execution: fake `ExecuteTransaction` records plan/options; service returns transaction result and maps `*transaction.TransactionError` via `ExitCodeForFailureKind`; validation failure code1, rollback code7, rotation code6; plain error code6.

Manual backup: creates data+metadata, returns IDs; dry-run calls no create and returns `DryRun=true`.

Manual rollback: prepare directory with 2 backups; `RestoreID` empty selects newest; explicit ID selects exact. Dry-run reads/verifies selected and shows diff, создаёт no safety backup. Real rollback event order: select → open selected → diff → safety backup (`OperationBeforeRollback`) → replace target with selected bytes → verify target. Fake replace failure → code6 и target unchanged; post verification captured in result. Safety backup всегда создаётся до restore.

Run: `go test ./internal/cliapp -run='Test(Service|Manual)' -v`
Expected: FAIL, API missing.

- [ ] **Step 2: Реализовать Service read-only и mutation execution**

`ProductionDependencies` использует `writer.ReadSnapshot`, `transaction.Execute`, `backup.Create`, `backup.Select`, `backup.Open`, `writer.Replace`, `NewFindmntVerifier.Verify`. `Service.List` читает snapshot, parse, report=`validator.Validate`, строит entry views через conservative associated comments (не заголовок документа). `Validate` online объединяет `models.MergeReports(internal, external.Report)`; offline только internal. `VerifyMount` вызывает `VerifyCandidate` на bytes target. `RunMutation` вызывает `transaction.Execute` с Plan из preview и options caller; error mapping сохраняет `*transaction.TransactionError` unwrap и recovery paths.

Run: `gofmt -w internal/cliapp && go test ./internal/cliapp -run='Test(Service)' -v`
Expected: PASS.

- [ ] **Step 3: Реализовать manual backup и rollback**

`BackupRequest{Directory, Prefix string; DryRun bool}`. Dry-run returns `DryRun=true`, no I/O. Real `backup.Create` from `ReadSnapshot` with `OperationManual`, `SourceNotChecked`.

`RollbackRequest{RestoreID, Directory, Prefix string; DryRun bool}`. Selection happens через `SelectBackup(target,prefix,directory,id)` до safety backup; selected `Open` проверяет metadata/hash. Diff direction — current target → selected backup. Dry-run stops after diagnostics. Real flow создаёт safety backup текущего target (`OperationBeforeRollback`), затем `Replace(target, selectedSnapshot.Data)`, затем `VerifyTarget`. Если replace fails, safety backup сохраняется и возвращается code6; post verification сохраняется в result, даже если содержит warnings/errors, но не откатывает выбранную копию автоматически.

Run: `gofmt -w internal/cliapp && go test ./internal/cliapp -run='TestManual' -v`
Expected: PASS.

- [ ] **Step 4: Полные tests и commit**

```bash
go test ./... && go test -race ./internal/cliapp && go vet ./...
git add internal/cliapp
git commit -m "feat: add cli service operations"
```

### Task 4: Cobra commands, confirmations, executable и E2E

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `cmd/app.go`, `cmd/list.go`, `cmd/mutation.go`, `cmd/validate.go`, `cmd/backup.go`, `cmd/rollback.go`
- Test: `cmd/app_test.go`, `cmd/e2e_test.go`
- Create: `main.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: production executable `fstab-edit` и CLI contract.

- [ ] **Step 1: Добавить Cobra и написать failing command tests**

Dependency:

```bash
go mod edit -require=github.com/spf13/cobra@v1.8.1
go mod tidy
```

App tests через `NewApp(AppOptions{Stdout:&out, Stderr:&errOut, Stdin:strings.NewReader("y\n"), TTY:func()bool{return false}, Runner:fake})`:

1. `--help` и `--version` return code0, stdout contains usage/version, do not read default `/etc/fstab` и не вызывают runner even if target missing.
2. `list --file temp -o json` valid JSON schema; table output entries; `--quiet` stdout empty for successful list.
3. `add --file temp --device ... --mount ... --type ... --options ... --dry-run -o json` leaves file unchanged, structured result has `changed=true,dry_run=true`.
4. Same add without `--dry-run`, non-TTY and no `--yes` → code2; with `--yes` and fake runner success → code0, target changed, backup exists.
5. `edit/delete/comment/uncomment` selectors work; ambiguous selector code8 and stderr lists candidate lines; `--yes` still code8.
6. `validate --offline` code0 for warnings, code1 for invalid; `verify-mount` fake runner argv verified.
7. `backup` creates files; `rollback --restore <id> --dry-run` leaves file; real rollback with scripted `"y\n"` restores bytes and creates safety backup.
8. Usage errors: verbose+quiet code2, unknown output code2, explicit selectors conflict code2, add missing required field code2.
9. Machine output stdout has exactly one JSON document; prompts/diagnostics are stderr only.
10. Exit mapping integration: fake transaction error kinds produce 1/4/5/6/7; rollback 7 has highest code even if diagnostics contain other errors.

Run: `go test ./cmd -v`
Expected: FAIL, cmd package missing.

- [ ] **Step 2: Реализовать App root и global flags**

`NewApp` defaults nil writers to `io.Discard`/empty reader; production caller passes os streams. Root command: `Use:"fstab-edit"`, `Short` русский, `SilenceUsage:true`, `SilenceErrors:true`, disable completion command, Version from options, version template `fstab-edit version {{.Version}}\n`. Persistent flags задаются точно из Global Constraints; validation формата/level/lang и verbose/quiet conflict выполняются до command handler и возвращают `ExitError` code2. `--help/--version` handled by Cobra до target access.

`Execute(args)` вызывает `root.Execute`, не печатает Cobra errors itself. Для `*ExitError` App returns его Code и уже записанное stderr; для plain errors returns code2 и пишет message. Успешный machine output записывается stdout один раз.

Run: `go test ./cmd -run='TestApp(Global|Help|Version)' -v`
Expected: PASS.

- [ ] **Step 3: Реализовать list/validate/verify commands**

`list` flags: `--file`, `--output`, `--quiet`. Handler вызывает `Service.List`, render через `output.RenderList`; table/json на stdout, diagnostics warnings/errors на stderr для table, в JSON только structured diagnostics.

`validate`: `--file`, `--output`, `--offline`, `--quiet`; offline runner не вызывается. `verify-mount`: `--file`, `--output`, `--quiet`; всегда static findmnt через Service. Exit0 допускает warnings; errors code1. JSON schemas из Task2.

Run: `go test ./cmd -run='Test(List|Validate|VerifyMount)' -v`
Expected: PASS.

- [ ] **Step 4: Реализовать mutation commands и confirmations**

Command-specific flags по TZ 11.4. `add` positional args=0, required flags presence проверяется через `Flags().Changed` и non-empty values; dump/pass default "0". `edit` требует selector и at least one changed flag; optional `--unset-dump/--unset-pass`; comment/clear conflict code2. `delete/comment/uncomment` принимают positional или explicit selector.

Handler: load service snapshot/document via `cliapp.PrepareMutation`; errors map; if dry-run render preview; else confirmation. `confirm` prints diff и warnings к stderr и спрашивает `[y/N]`; non-TTY без yes → code2; invalid/EOF/no → no-op exit0 (или documented code0 with message) и target unchanged. `--yes` skips prompt only after preview validation. Then `Service.RunMutation`; structured result includes backup ID/rollback from transaction.

Run: `go test ./cmd -run='Test(Add|Edit|Delete|Comment|Uncomment)' -v`
Expected: PASS.

- [ ] **Step 5: Реализовать backup/rollback и exit mapping E2E**

`backup` flags `--file`, `--backup-dir`, `--dry-run`, `--output`, `--quiet`; no mutation prompt. `rollback` flags positional optional restore ID и `--restore`; ровно один источник ID; selector через `backup.Select`, ambiguity/not found mapping code1/8 per backup API. Confirmation с diff current→selected; `--yes` skip. Dry-run no safety copy. Real flow service.Rollback; result output schema.

`main.go` wires `os.Stdin/Stdout/Stderr`, `sysutil.NewRunner()`, version `"dev"`, TTY function and interactive chooser; `os.Exit(code)` only in main. Static binary builds.

Run: `go test ./cmd -v && CGO_ENABLED=0 go build -o fstab-edit .`
Expected: PASS and binary ignored by `.gitignore`.

- [ ] **Step 6: README и commit**

README обновить: команды и examples из ТЗ; CLI статус; `--lang/log-level/config` принимаются, но config/logger/full i18n будут следующим блоком; default target `/etc/fstab`; tests не используют реальный fstab.

```bash
gofmt -w cmd internal/cliapp internal/output internal/diff
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
go test ./... -cover
CGO_ENABLED=0 go build -o fstab-edit .
git add go.mod go.sum cmd main.go README.md
git commit -m "feat: add fstab edit cli"
```

Expected: all PASS, lint 0 issues; удалить локальный `fstab-edit` binary после проверки, если не нужен как deliverable; `git status --short` clean.

## Финальная приёмка

1. Все десять команд реализованы; `--help` и `--version` не читают target/findmnt.
2. Selector exclusivity, full decoded matching, physical line, ambiguity code8 и запрет `--yes` arbitrary choice покрыты unit/E2E tests.
3. Add/edit flags и no-op behavior соответствуют ТЗ 11.4; no-op не создаёт backup/lock/write.
4. List включает active/commented, excludes comments/blanks, показывает physical line/state/raw+decoded/optional presence/comment.
5. Dry-run ничего не изменяет и не требует `--yes`.
6. Confirmation `[y/N]`, default No, non-TTY требует `--yes`, machine stdout не смешивается с prompt/diagnostics.
7. Unified diff headers original/candidate, no-final-newline marker, неизменённые lines не переформатируются.
8. JSON schema v1, CSV/TSV headers и TSV escaping проверены exact tests; diagnostics не псевдозаписи fstab.
9. Exit codes 0–8 и priority rollback7 проверены unit/integration tests.
10. `CGO_ENABLED=0 go build` создаёт исполняемый `fstab-edit`.
11. Task reviews и final whole-branch review без открытых Critical/Important; fast-forward merge, post-merge tests, удаление worktree/branch, push `origin/main`, clean synchronized status.
