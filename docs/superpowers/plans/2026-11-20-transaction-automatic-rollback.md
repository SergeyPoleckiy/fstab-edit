# Transaction and Automatic Rollback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** реализовать доменную transaction сохранения fstab: no-op detection, внутреннюю проверку candidate, lock, повторный base hash, findmnt pre-check, backup текущей операции, atomic replace, post-check и automatic rollback копии именно этой transaction.

**Architecture:** оркестрация размещается в отдельном доменном пакете `internal/transaction`, потому что `internal/backup` уже зависит от `internal/writer`, и размещение transaction в writer создало бы цикл импортов. Публичный `Execute` принимает immutable `Plan` с готовыми bytes и `Options` с `sysutil.CommandRunner`; он не занимается CLI, prompt, diff, dry-run или журналом. `Execute` использует уже проверенные примитивы `writer.AcquireLock/VerifyBaseHash/Replace`, `backup.Create/Open/Rotate` и `sysutil.VerifyCandidate/FindmntVerifier`. Fault injection и все границы отказов выполняются через immutable per-call operations struct.

**Tech Stack:** Go 1.22+, существующие internal packages (`parser`, `validator`, `models`, `sysutil`, `backup`, `writer`), стандартные `context`, `crypto/sha256`, `errors`; новых зависимостей нет.

## Global Constraints

- Рабочая область выполнения: `/home/sergey/projects/fstab-editor/work/.worktrees/stage-1-transaction`.
- Модуль `fstab-edit`, Go `1.22.0`; все файлы, публичные API и нетривиальная логика получают максимальные содержательные русские comments по ТЗ 4.3.
- Реальный `/etc/fstab` не читается и не изменяется. Тесты только `t.TempDir`, fake `sysutil.CommandRunner` и fake lock handle; root не требуется.
- Transaction не выполняет CLI parsing, user prompts, diff rendering, dry-run, manual rollback, logging и config. Эти подсистемы подключатся позднее; README обязан явно зафиксировать границу.
- No-op candidate, байтово равный текущему target, завершается до lock, validation, backup, findmnt и write с `Changed=false`.
- Internal candidate validation выполняется до lock; любой `SeverityError` запрещает lock и изменения.
- Lock удерживается от повторного `VerifyBaseHash` до post-check/rollback/rotation; read-only/no-op path lock не создаёт.
- Под lock target читается повторно и сравнивается с base SHA-256; конфликт запрещает backup/write и требует освобождения lock.
- Pre-write `sysutil.VerifyCandidate` выполняется до backup и replace; diagnostics `SeverityError` запрещают мутацию.
- Backup создаётся из snapshot target, повторно прочитанного под lock, и заново проверяется через `backup.Open` до replace.
- `writer.Replace` вызывается один раз для candidate. Если `err != nil && !Replaced`, target не тронут и rollback не нужен. Если `err != nil && Replaced` или post-check имеет error, выполняется ровно одна попытка automatic rollback текущего backup.
- Rollback читает data текущего backup через secure snapshot и атомарно восстанавливает её через `writer.Replace`. Повторных попыток и внешних команд нет; результат явно возвращается.
- Post-write warnings не вызывают rollback; errors вызывают.
- Rotation запускается только после полностью успешной changed transaction, после post-check; ID backup текущей transaction всегда передаётся protected. Ошибка rotation не откатывает уже успешную запись.
- Cleanup/lock release не скрывает первичную ошибку; `errors.Is` по классам и первичной причине сохраняется.
- Ошибки возвращаются как `*TransactionError` со стабильным `FailureKind`; rollback failure имеет наивысший класс `rollback` и recovery paths.
- Fault injection только per-call operations structs с полной production конфигурацией; incomplete config отклоняется до чтения target или создания lock.

---

## Структура файлов и внешние контракты

- `internal/transaction/transaction.go` — Plan/Options/Result, FailureKind/TransactionError, prepare и публичный Execute.
- `internal/transaction/transaction_test.go` — no-op/validation/base-conflict/lock lifecycle tests.
- `internal/transaction/transaction_execute_test.go` — backup/replace/post-check/rollback fault matrix.
- `internal/transaction/transaction_rotation_test.go` — success rotation, protected current backup и integration tests.
- `README.md` — transaction guarantees и отсутствующие CLI/logger/dry-run.

Экспортируемый контракт:

```go
type Plan struct {
	TargetPath      string
	Candidate       []byte
	Operation       backup.Operation
	BackupDirectory string
	BackupPrefix    string
	RotationKeep    int
}

type Options struct {
	Runner               sysutil.CommandRunner
	LockTimeout          time.Duration
	RotationProtectedIDs []string
}

type RecoveryPaths struct {
	Target   string
	Backup   string
	Metadata string
	Log      string
}

type TransactionResult struct {
	Changed           bool
	TargetPath        string
	CandidateSHA256   [32]byte
	InternalReport    models.Report
	PreVerification   sysutil.Verification
	PostVerification  sysutil.Verification
	Backup            backup.Backup
	RolledBack        bool
	RollbackError     error
	Recovery          RecoveryPaths
	Rotation          backup.RotationResult
}

type FailureKind string

const (
	FailureValidation FailureKind = "validation"
	FailureConflict   FailureKind = "conflict"
	FailureExternal   FailureKind = "external"
	FailureIO         FailureKind = "io"
	FailureRollback   FailureKind = "rollback"
	FailureRotation   FailureKind = "rotation_after_success"
)

type TransactionError struct {
	Kind     FailureKind
	Err      error
	Recovery RecoveryPaths
}

func (e *TransactionError) Error() string
func (e *TransactionError) Unwrap() error

var (
	ErrTransactionValidation = errors.New("candidate не прошёл внутреннюю проверку")
	ErrTransactionConflict   = errors.New("target недоступен, изменился или lock недоступен")
	ErrTransactionExternal   = errors.New("внешняя проверка завершилась ошибкой")
	ErrTransactionIO         = errors.New("I/O, backup или write завершились ошибкой")
	ErrTransactionRollback   = errors.New("automatic rollback не восстановил target")
	ErrRotationAfterSuccess  = errors.New("transaction записана успешно, но rotation завершился ошибкой")
)

func Execute(ctx context.Context, plan Plan, options Options) (TransactionResult, error)
```

### Task 1: Transaction model, no-op и internal validation

**Files:**
- Create: `internal/transaction/transaction.go`
- Create: `internal/transaction/transaction_test.go`

**Interfaces:**
- Consumes: `parser.Parse`, `validator.Validate`, `writer.ReadSnapshot`, `writer.Snapshot`, `backup.Operation`, `models.Report`.
- Produces: Plan/Options/RecoveryPaths/TransactionResult/FailureKind/TransactionError/sentinel errors, `prepareTransaction`, `transactionFailure`; публичный `Execute` intentionally появится в Task 2.

- [ ] **Step 1: Написать failing tests model/preparation**

Тесты package `transaction`, temp target и candidate. Проверь exact error classification:

```go
_, err := prepareTransaction(Plan{TargetPath: path, Candidate: []byte("...")}, prepareOperations{readSnapshot: writer.ReadSnapshot})
var txErr *TransactionError
if !errors.As(err, &txErr) || txErr.Kind != FailureValidation {
	t.Fatalf("ожидался validation failure, получено %v", err)
}
```

Cases:

1. Valid temp target и valid candidate → prepared содержит canonical target snapshot, candidate SHA-256, `Changed=true`, non-nil `InternalReport.Diagnostics`, no errors.
2. Candidate bytes, байтово равные current target → `Changed=false`, internal report не вызывает findmnt и не требует lock (проверяется только через отсутствие вызовов в later task); returned result fields candidate hash/target path заполнены.
3. Nil candidate slice отклоняется `FailureValidation` до read target.
4. Пустая или неизвестная `Plan.Operation` отклоняются `FailureValidation` до read target явной проверкой по всем константам `backup.Operation`; невалидная операция не должна доходить до backup metadata.
5. Corrupt candidate (например, строка без четырёх полей, если validator присваивает error) → `FailureValidation`, `errors.Is(err, ErrTransactionValidation)`.
6. Missing/invalid target read wraps `FailureIO` и `ErrTransactionIO`.
7. Target read returns snapshot, но `ReadSnapshot` error сохраняет причину через `errors.Is`.
8. `TransactionError.Error()` содержит Russian kind/cause без stack; `Unwrap()` возвращает первичную cause.
9. `FailureKind` values точно совпадают с контрактом: `validation`, `conflict`, `external`, `io`, `rollback`, `rotation_after_success`.

Run: `go test ./internal/transaction -run='Test(PrepareTransaction|TransactionError)' -v`
Expected: FAIL, transaction model отсутствует.

- [ ] **Step 2: Реализовать types и typed transaction failure**

`transaction.go` определяет Plan/Options/RecoveryPaths/TransactionResult с русскими field comments. `TransactionResult` не содержит errors; сбои передаются отдельным возвращаемым `*TransactionError`, чтобы successful/partial results можно было inspect даже при ошибке.

`TransactionError` хранит stable kind, cause и recovery paths. `Error()` формирует русское сообщение без копирования данных target/candidate. `Unwrap()` возвращает cause, поэтому `errors.Is` работает и на sentinel, и на конкретную I/O причину. Sentinel errors создаются один раз и используются как классы.

Helpers:

```go
func transactionFailure(kind FailureKind, sentinel, cause error, recovery RecoveryPaths) *TransactionError
func wrapTransactionFailure(kind FailureKind, sentinel, cause error, recovery RecoveryPaths) *TransactionError
```

`transactionFailure` применяется, когда cause ещё не обёрнут sentinel; `wrapTransactionFailure` используется, когда cause уже несёт class sentinel, чтобы не дублировать вложение.

- [ ] **Step 3: Реализовать prepareTransaction**

Internal `prepareOperations` содержит только `readSnapshot func(string) (writer.Snapshot, error)`; production value — `writer.ReadSnapshot`. Nil readSnapshot отклоняется programming error до чтения target.

Algorithm:

1. Reject nil candidate slice with `FailureValidation`.
2. Проверь `plan.Operation` явным `switch` по всем семи константам `backup.Operation`; пустое/неизвестное значение → `FailureValidation` + `ErrTransactionValidation`. Это usage error доменного слоя, а не I/O.
3. `base, err := operations.readSnapshot(plan.TargetPath)`; ошибка → `FailureIO` + `ErrTransactionIO`.
4. Compute `candidateSHA256`.
5. `bytes.Equal(base.Data, plan.Candidate)` → `Changed=false`; InternalReport остаётся `models.Report{Diagnostics: []models.Diagnostic{}}`, поскольку ТЗ завершает no-op до проверки/записи.
6. Иначе `InternalReport = validator.Validate(parser.Parse(plan.Candidate))`; `HasErrors()` → `FailureValidation` + `ErrTransactionValidation`.
7. Return `preparedTransaction{plan, options, base, candidateSHA, changed, internalReport}`.

Не делай lock/backup/replace в Task 1.

Run: `gofmt -w internal/transaction && go test ./internal/transaction -run='Test(PrepareTransaction|TransactionError)' -v`
Expected: PASS.

- [ ] **Step 4: Проверить и закоммитить**

Run: `go test ./... && go test -race ./internal/transaction && go vet ./...`
Expected: PASS.

```bash
git add internal/transaction/transaction.go internal/transaction/transaction_test.go
git commit -m "feat: add transaction validation preparation"
```

### Task 2: Execute с backup, post-check и automatic rollback

**Files:**
- Modify: `internal/transaction/transaction.go`
- Create: `internal/transaction/transaction_execute_test.go`

**Interfaces:**
- Consumes: Task 1 prepared transaction; `writer.AcquireLock`, `VerifyBaseHash`, `Replace`; `backup.Create/Open`; `sysutil.VerifyCandidate`, `NewFindmntVerifier`.
- Produces: публичный `Execute`, `transactionOperations`, full pre/post rollback behavior без rotation (Task 3).

- [ ] **Step 1: Написать failing execute tests без rotation**

Define fake runner implementing `sysutil.CommandRunner`; valid result:

```go
sysutil.RunResult{ExitCode: 0, Stdout: "0 parse errors, 0 errors, 0 warnings\n"}
```

Define fake lock with `Close` count/error. Internal seam:

```go
type transactionOperations struct {
	readSnapshot       func(string) (writer.Snapshot, error)
	acquireLock        func(context.Context, string, time.Duration) (transactionLock, error)
	verifyCandidate    func(context.Context, sysutil.CommandRunner, []byte) sysutil.Verification
	createBackup       func(writer.Snapshot, backup.CreateOptions) (backup.Backup, error)
	openBackup         func(string) (backup.Backup, error)
	replace            func(string, []byte) (writer.ReplaceResult, error)
	verifyTarget       func(context.Context, sysutil.CommandRunner, string) sysutil.Verification
	readBackupSnapshot func(string) (writer.Snapshot, error)
}
type transactionLock interface{ Close() error }
```

Production constructor заполняет все fields; nil field отклоняется до read target. Public `Execute(ctx, plan, options)` вызывает `executeWithOperations` с production config и `writer.AcquireLock` wrapped as `transactionLock`.

Success test использует real temp target/candidate и реальные `backup.Create`, `backup.Open`, `writer.Replace`, но fake lock и fake runner; `verifyCandidate/verifyTarget` могут быть production `sysutil.VerifyCandidate`/`FindmntVerifier` с fake runner. Проверь target candidate bytes, result Changed=true, backup metadata source path/hash/operation, pre/post verification, lock Close count=1.

No-op test через public Execute (production prepare, fake lock seam): result Changed=false, lock/backup/replace not called.

Failure matrix:

1. Lock acquire error → `FailureConflict`, `ErrTransactionConflict`, no backup/replace.
2. Base conflict на повторном read → `FailureConflict`, backup/replace not called, lock closed.
3. Candidate verification `HasErrors` → `FailureExternal`, `ErrTransactionExternal`, no backup/replace.
4. Backup create error → `FailureIO`, replace not called, lock closed.
5. `backup.Open` error после create → `FailureIO`, replace not called, current backup path в recovery.
6. Replace returns `(ReplaceResult{Replaced:false}, err)` → `FailureIO`, no rollback, lock closed.
7. Replace returns `(ReplaceResult{Replaced:true}, err)` → одна rollback попытка; target restored to backup bytes; result `RolledBack=true`, returned error `FailureIO`; lock closed.
8. Post verification target `HasErrors` после successful Replace → rollback; result `RolledBack=true`, primary `FailureExternal`; lock closed.
9. Post verification warnings only → success, `RolledBack=false`.
10. Rollback `readBackupSnapshot` error → `FailureRollback`, `ErrTransactionRollback`, `RollbackError` сохраняет cause, recovery target/backup/metadata заполнены, no repeated rollback.
11. Rollback `replace` error → `FailureRollback`, primary cause не теряется (`errors.Is` по обеим), no repeated rollback.
12. Deferred lock close error присоединяется к returned error без потери primary class; lock count remains один.

Run: `go test ./internal/transaction -run='TestExecute' -v`
Expected: FAIL, Execute/operations отсутствуют.

- [ ] **Step 2: Реализовать locked sequence и lock lifecycle**

`executeWithOperations` вызывает `prepareTransaction` через seam `readSnapshot`; если changed=false, return result с hash/target/internal report и nil error.

Для changed path:

1. Acquire lock через seam with `options.LockTimeout` (0 = writer default). Любая ошибка, включая context cancellation/timeout, → `FailureConflict`.
2. `lockClosed := false`; deferred close once. Close error присоединяется через `errors.Join`; он не меняет `TransactionResult` и не заменяет primary class.
3. Под lock прочитай target через seam `operations.readSnapshot(plan.TargetPath)` и явно сравни `current.SHA256` с `prepared.base.SHA256`; `writer.ErrBaseHashConflict` или read error → `FailureConflict`. Это позволяет fault injection покрыть повторное чтение без вызова несвободного от seam внутреннего API.
4. `pre := operations.verifyCandidate(ctx, options.Runner, plan.Candidate)`; `HasErrors()` → `FailureExternal`. Сохранить `result.PreVerification`.
5. sourceVerification := `backup.SourceValid` при успешной проверке; `CreateOptions{Directory: plan.BackupDirectory, Prefix: plan.BackupPrefix, Operation: plan.Operation, SourceVerification: sourceVerification}`. Backup создаётся из `current`, не из первоначального base snapshot.
6. `created, err := operations.createBackup(current, createOptions)`; error → `FailureIO`, recovery уже содержит target path.
7. `verified, err := operations.openBackup(created.Path)`; error → `FailureIO`, recovery target+backup paths.
8. `replaceResult, err := operations.replace(current.Path, plan.Candidate)`; any error → if `!replaceResult.Replaced` return `FailureIO`; if `Replaced` перейти к rollback helper.
9. После nil error `post := operations.verifyTarget(ctx, options.Runner, current.Path)`; `HasErrors()` → rollback helper с primary `FailureExternal`; warnings допустимы.
10. После успешного post-check вернуть result без rotation (Task 3 добавит следующий шаг).

`RecoveryPaths` заполняется как только известны: Target после base read, Backup/Metadata после create, Log остаётся пустым до logger.

- [ ] **Step 3: Реализовать одиночный automatic rollback**

Internal helper получает primary `*TransactionError`, current target path и verified backup. Он не вызывает `Select` или повторный поиск «последнего» backup — только переданный объект текущей transaction.

Rollback algorithm:

1. `snapshot, err := operations.readBackupSnapshot(verified.Path)`; error → result `RolledBack=false`, `RollbackError=err`, returned `FailureRollback`, recovery заполнен.
2. `_, err = operations.replace(currentPath, snapshot.Data)`; любая ошибка, включая `Replaced=true`, → `FailureRollback`; primary class/cause сохраняется через multiple `%w`.
3. Success → `RolledBack=true`, `RollbackError=nil`; returned error остаётся исходным primary class (`FailureExternal` или `FailureIO`). Rollback result доступен в TransactionResult.

Не делай post-rollback findmnt, повторную запись или cleanup backup: backup текущей transaction сохраняется для диагностики.

Run: `gofmt -w internal/transaction && go test ./internal/transaction -run='TestExecute' -v && go test -race ./internal/transaction`
Expected: PASS.

- [ ] **Step 4: Полный пакет и commit**

Run: `go test ./... && go vet ./...`
Expected: PASS.

```bash
git add internal/transaction/transaction.go internal/transaction/transaction_execute_test.go
git commit -m "feat: execute fstab transactions with rollback"
```

### Task 3: Rotation после успеха и README

**Files:**
- Modify: `internal/transaction/transaction.go`
- Create: `internal/transaction/transaction_rotation_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 2 successful changed transaction.
- Produces: rotation step в `executeWithOperations`, `ErrRotationAfterSuccess` behavior и documented transaction boundaries.

- [ ] **Step 1: Написать failing rotation tests**

Use real temp target/candidate/backup plus fake lock/runner.

1. Successful transaction calls rotation with canonical target path, normalized default prefix, plan directory/keep и protected IDs = copy(options.RotationProtectedIDs) + verified backup ID. Result Rotation populated; returned error nil.
2. Caller-provided protected ID не удаляется; current transaction backup ID не удаляется при keep=1.
3. Rotation remove failure after successful write → target remains candidate bytes, result Changed=true/RolledBack=false, returned `FailureRotation` with `ErrRotationAfterSuccess`, `errors.Is` сохраняет rotation cause; no rollback attempt.
4. Rotation protected/current backup survives even if keep small.
5. Rotation not called for no-op, validation failure, base conflict, pre-check failure, backup failure или rollback result.
6. Rotation called after post verification success, not before; event order test: backup create → open backup → replace → verify target → rotate → lock close.
7. Empty `RotationProtectedIDs` slice не алиасится с append; caller slice не изменяется.
8. Result.Rotation slices non-nil при rotation success/failure.

Run: `go test ./internal/transaction -run='TestExecuteRotation' -v`
Expected: FAIL, rotation step отсутствует.

- [ ] **Step 2: Реализовать post-success rotation**

Add `rotate func(string, string, string, int, []string) (backup.RotationResult, error)` to operations; production uses `backup.Rotate`. Nil rotate rejects config before transaction starts.

Only after successful post verification:

```go
protected := append([]string(nil), options.RotationProtectedIDs...)
protected = append(protected, verified.ID)
rotation, err := operations.rotate(current.Path, effectivePrefix, plan.BackupDirectory, plan.RotationKeep, protected)
result.Rotation = rotation
if err != nil {
	return result, transactionFailure(FailureRotation, ErrRotationAfterSuccess, err, result.Recovery)
}
return result, nil
```

Empty backup prefix normalizes to `backup.DefaultBackupPrefix` для rotation, чтобы selection и удаление использовали один effective prefix. Rotation error не вызывает rollback и не меняет target; `ErrRotationAfterSuccess` явно говорит, что запись уже успешна.

Lock deferred close присоединяет close error после rotation: если rotation вернула ошибку, она остаётся primary; cleanup error не заменяет её.

Run: `gofmt -w internal/transaction && go test ./internal/transaction -run='TestExecuteRotation' -v`
Expected: PASS.

- [ ] **Step 3: README, полная приёмка и commit**

README: transaction orchestrates lock/base check, internal validation, findmnt candidate check, current backup, atomic replace, post-check и один automatic rollback; no-op не создаёт backup/lock/write; post-write warnings допустимы; rollback failure критический и оставляет paths; rotation error после успешной записи не откатывает изменение; CLI/prompt/diff/dry-run/logger/config/manual rollback ещё не подключены.

```bash
gofmt -w internal/transaction
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
go test ./internal/transaction -cover
```

Expected: все PASS, lint 0 issues, coverage пакета `internal/transaction` не ниже 80%; зафиксируй фактический процент. Проверь отсутствие temp files/artifacts и `git status --short`.

```bash
git add internal/transaction README.md
git commit -m "feat: rotate backups after successful transactions"
```

## Финальная приёмка

1. No-op завершается до lock/validation/backup/write с `Changed=false`.
2. Validation errors до lock, conflict после lock, external pre/post errors, backup/IO errors и rollback failures имеют стабильные distinct `FailureKind`.
3. Backup создаётся только из snapshot, повторно прочитанного под lock, и проверяется до replace.
4. `Replaced=false` никогда не вызывает rollback; `Replaced=true` и любая post-check error вызывают ровно одну попытку restore текущего backup.
5. Rollback success возвращает primary error с `RolledBack=true`; rollback failure возвращает `FailureRollback`, сохраняет primary и rollback causes и recovery paths.
6. Rotation только после полного success; current backup protected; rotation error не откатывает target.
7. Lock закрывается на всех путях один раз; cleanup/close errors не скрывают primary.
8. Fake runner не запускает findmnt; temp files только t.TempDir; реальный `/etc/fstab` не используется.
9. `go test ./...`, race, vet, lint 0 issues и coverage >=80% проходят на итоговом HEAD.
10. Task reviews и final whole-branch review без открытых Critical/Important; merge в main, post-merge tests, cleanup worktree/branch, SSH push origin/main и clean synchronized status.
