# Backup Metadata and Rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** реализовать создание резервных копий fstab с sidecar-метаданными, независимую проверку целостности, выбор backup по target/ID и безопасную ротацию без изменения самого target.

**Architecture:** пакет `internal/backup` работает с готовым `writer.Snapshot` и не выполняет lock, atomic replace, rollback или CLI. `Create` атомарно создаёт data 0600 и sidecar JSON 0600 через `O_EXCL/O_NOFOLLOW`, выполняет fsync и read-back SHA-256; `Open/List/Select` строго проверяют metadata и хеши; `Rotate` удаляет только однозначно распознанные собственные копии конкретного canonical target/prefix, исключая protected ID. Fault injection выполняется immutable per-call operations structs, а не globals.

**Tech Stack:** Go 1.22+, стандартный `encoding/json`, `crypto/sha256`, `os`, `path/filepath`, `time`, существующие `internal/writer` и `golang.org/x/sys/unix`; новых внешних зависимостей нет.

## Global Constraints

- Рабочая область выполнения: `/home/sergey/projects/fstab-editor/work/.worktrees/stage-1-backup`.
- Модуль `fstab-edit`, Go `1.22.0`; все файлы, публичные API и нетривиальная логика получают максимальные содержательные русские comments по ТЗ 4.3.
- Реальный `/etc/fstab` не читается и не изменяется. Все тесты используют `t.TempDir` и не требуют root.
- Backup не изменяет target. Lock, writer.Replace, transaction, automatic/manual rollback, dry-run и CLI в этот план не входят.
- Backup ID строго имеет ядро `YYYY-MM-DD_HH-MM-SS.nnnnnnnnnZ` в UTC. Числовой суффикс при коллизии добавляется к именам файлов (`<prefix>.<id>.1`, sidecar `<prefix>.<id>.1.json`), но не заменяет backup ID в metadata.
- Default prefix — `fstab.bak`; default backup directory — parent directory canonical target; непустой `Directory` в options переопределяет его.
- Data и sidecar создаются только через `O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC`, начальный mode 0600; финальный symlink отклоняется.
- Backup считается готовым только после write, fsync data и metadata, повторного чтения обоих файлов и совпадения SHA-256 data с metadata.
- Metadata schema v1 обязана содержать: `schema_version`, `backup_id`, UTC `created_at`, `source_path`, `source_sha256`, `backup_sha256`, `uid`, `gid`, `mode`, `operation`, `program_version`, результат исходной проверки. JSON keys и enum values не локализуются.
- Operation допускает ровно `before-add`, `before-edit`, `before-delete`, `before-comment`, `before-uncomment`, `before-rollback`, `manual`; source verification допускает ровно `not_checked`, `valid`, `invalid`, `unavailable`.
- Rotation default keep = 10. Удаляются только собственные, полностью проверенные backups текущего canonical target/prefix; symlink, чужие файлы, копии другого target и backups без корректной metadata не трогаются.
- Protected backup ID никогда не удаляется. Ошибка удаления останавливает дальнейшие удаления и возвращается с partial `RotationResult`; уже успешное изменение target в будущей transaction не откатывается.
- Cleanup после failed create сохраняет первичную ошибку через `errors.Join`.
- Fault injection только per-call operations structs с полной production конфигурацией; incomplete config отклоняется до создания файлов.
- README должен зафиксировать границы блока: backup гарантирует восстановление байтов, но не заявляет работоспособность повреждённого исходника.

---

## Структура файлов и внешние контракты

- `internal/backup/metadata.go` — стабильные типы metadata, operation/source-verification enums, валидация schema и хешей.
- `internal/backup/create.go` — secure paths, collision naming, create/fsync/read-back lifecycle и cleanup.
- `internal/backup/create_test.go` — exact filenames/JSON, metadata, collision и create fault injection.
- `internal/backup/verify.go` — `Open` по data path или sidecar с полной проверкой целостности.
- `internal/backup/selection.go` — `List` и `Select` по canonical target/prefix/directory.
- `internal/backup/verify_test.go` и `internal/backup/selection_test.go` — integrity, foreign/symlink/corrupt cases и выбор.
- `internal/backup/rotate.go` — safe rotation и partial result.
- `internal/backup/rotate_test.go` — keep/protection/foreign failures/fault injection.
- `README.md` — реализованные backup guarantees и отсутствующий rollback/transaction.

Экспортируемый контракт:

```go
const (
	MetadataSchemaVersion = 1
	DefaultBackupPrefix   = "fstab.bak"
	DefaultKeepBackups    = 10
	ProgramVersion        = "dev"
)

type Operation string
type SourceVerification string

type Metadata struct {
	SchemaVersion      int                `json:"schema_version"`
	BackupID           string             `json:"backup_id"`
	CreatedAt          string             `json:"created_at"`
	SourcePath         string             `json:"source_path"`
	SourceSHA256       string             `json:"source_sha256"`
	BackupSHA256       string             `json:"backup_sha256"`
	UID                uint32             `json:"uid"`
	GID                uint32             `json:"gid"`
	Mode               uint32             `json:"mode"`
	Operation          Operation          `json:"operation"`
	ProgramVersion     string             `json:"program_version"`
	SourceVerification SourceVerification `json:"source_verification"`
}

type Backup struct {
	ID           string
	Path         string
	MetadataPath string
	Metadata     Metadata
	SHA256       [32]byte
}

type CreateOptions struct {
	Directory          string
	Prefix             string
	Operation          Operation
	SourceVerification SourceVerification
	Now                time.Time // zero time означает time.Now().UTC()
}

func Create(source writer.Snapshot, options CreateOptions) (Backup, error)
func Open(path string) (Backup, error)

type Selection struct {
	RequestedID string
	Selected    Backup
	Candidates  []Backup
}

func List(targetPath, prefix, directory string) ([]Backup, error)
func Select(targetPath, prefix, directory, id string) (Selection, error)

type RotationResult struct {
	Removed []Backup
	Skipped []Backup
	Failed  []Backup
}

func Rotate(targetPath, prefix, directory string, keep int, protectedIDs []string) (RotationResult, error)

var (
	ErrInvalidBackup   = errors.New("недопустимый или повреждённый backup")
	ErrBackupNotFound  = errors.New("backup не найден")
	ErrBackupAmbiguous = errors.New("backup ID соответствует нескольким файлам")
)
```

### Task 1: Backup data + sidecar metadata

**Files:**
- Create: `internal/backup/metadata.go`
- Create: `internal/backup/create.go`
- Create: `internal/backup/create_test.go`

**Interfaces:**
- Consumes: `writer.Snapshot`, `writer.CanonicalTargetPath`, `writer.ReadSnapshot`.
- Produces: все типы/constants из контракта, `Create`, а также внутренние `parseMetadata`, `formatBackupID`, `readVerifiedBackup` и `createOperations`.

- [ ] **Step 1: Написать failing metadata/creation tests**

Создай package `backup` tests. Deterministic time:

```go
fixed := time.Date(2026, 11, 18, 15, 30, 45, 123456789, time.UTC)
expectedID := "2026-11-18_15-30-45.123456789Z"
```

Success test создаёт temp target, получает `writer.ReadSnapshot`, вызывает:

```go
backup, err := Create(source, CreateOptions{
	Operation:          OperationManual,
	SourceVerification: SourceNotChecked,
	Now:                fixed,
})
```

Проверь: `backup.ID == expectedID`; data path `<target-dir>/fstab.bak.<expectedID>`; sidecar path + `.json`; оба regular 0600; data bytes точно равны `source.Data`; metadata содержит exact values, canonical source path, lowercase hex source/backup SHA-256, UID/GID/mode из `os.Lstat(source.Path)`, `schema_version=1`, UTC `created_at`, `program_version="dev"`, `source_verification="not_checked"`; JSON decoder с `DisallowUnknownFields` принимает sidecar, а `json.Valid` true; source target не изменён; repeated open/read хеш совпадает.

Collision test предварительно создаёт data и sidecar для base имени, затем `Create` с тем же `Now`; ожидаются `.1` имена. Второй collision даёт `.2`. Проверь, что metadata `backup_id` во всех трёх случаях остаётся одним и тем же expected ID.

Дополнительные tests:

1. Empty prefix использует `fstab.bak`; explicit `Directory` размещает оба файла там.
2. Operation из whitelist принимается; пустая/неизвестная operation и неизвестная source verification отклоняются до создания файлов.
3. Missing custom directory создаётся как real directory 0700; существующий final symlink directory отклоняется.
4. Data и sidecar имена всегда образуют пару; sidecar symlink отклоняется; filename parsing не принимает произвольные похожие имена.
5. Zero `Now` работает и форматирует UTC; nanoseconds меньше девяти цифр дополняются нулями, больше — невозможны для time.Time.
6. Invalid UTF-8/binary source data копируются побайтово; source и backup SHA-256 совпадают.
7. Create после изменения source на disk всё равно копирует переданный Snapshot и metadata source hash: функция не читает target повторно для данных.

Run: `go test ./internal/backup -run='Test(Create|FormatBackupID|ParseMetadata)' -v`
Expected: FAIL, package/API отсутствуют.

- [ ] **Step 2: Реализовать metadata schema и strict validation**

`metadata.go` задаёт constants, enums и типы из контракта. Все operation/source-verification values описаны русскими comments. `formatBackupID` использует layout `"2006-01-02_15-04-05.000000000Z"` и требует UTC time.

`parseMetadata(data []byte) (Metadata, error)` создаёт decoder с `DisallowUnknownFields`, запрещает trailing JSON value, декодирует в struct и выполняет полную валидацию:

- `schema_version == MetadataSchemaVersion`;
- `backup_id` соответствует strict regexp `^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}\.\d{9}Z$` и повторно парсится тем же layout, чтобы regexp не принял несуществующую календарную дату;
- `created_at` парсится `time.RFC3339Nano`, `Location()==time.UTC`, nanosecond precision сохраняется; `created_at` и `backup_id` обязаны обозначать одно и то же UTC-время, включая nanoseconds;
- `source_path` absolute/clean; `source_sha256` и `backup_sha256` являются ровно 64 lowercase hex chars и декодируются в `[32]byte`;
- UID/GID не превышают `math.MaxUint32`, mode `<= 0o7777`;
- operation и source verification принадлежат точным whitelist.

Ошибки оборачивают `ErrInvalidBackup`, но не включают содержимое backup.

- [ ] **Step 3: Реализовать create lifecycle с O_EXCL и fsync**

`createOperations` — immutable per-call struct с fields `mkdirAll`, `lstat`, `openExclusive`, `readSnapshot` и remove. `backupFile` interface содержит `io.Writer`, `Sync`, `Close`, `Name`. Production constructor заполняет `os.MkdirAll`, `os.Lstat`, `unix.Open(O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC, 0600)` через `os.NewFile`, `writer.ReadSnapshot`, `os.Remove`; incomplete fields отклоняются до создания файлов.

Directory: absolute clean path; если отсутствует, `MkdirAll(..., 0700)`; `Lstat` обязан показать real directory, не symlink. Default directory — `filepath.Dir(source.Path)`. Metadata UID/GID/mode получай через `Lstat(source.Path)` и `Stat_t`, проверяя regular file.

Для одного base ID делай до 1000 попыток `suffix := 0,1,2...`; имя base при suffix 0, иначе `<prefix>.<id>.<suffix>`. Sidecar — `<dataName>.json`. Data и sidecar всегда открываются только после выбора пары: сначала data O_EXCL, затем sidecar O_EXCL; если sidecar занят, закрой/удали data и перейди к следующему suffix. EEXIST/EISDIR/ELOOP от exclusive open означают collision/небезопасную подмену и разрешают следующий suffix только для EEXIST; остальные немедленно возвращаются.

Write data → Sync → Close; при любой ошибке cleanup обоих уже созданных файлов. Write sidecar JSON, полученный через `json.Marshal` с trailing newline, → Sync → Close. После этого `readVerifiedBackup` заново открывает data и sidecar через `writer.ReadSnapshot`, парсит metadata, требует exact data/sidecar hashes, matching source hash, ID и paths. Несовпадение любого условия — `ErrInvalidBackup` с cleanup.

`Create` возвращает Backup только после успешного read-back. Primary error никогда не заменяется cleanup error; cleanup присоединяется через `errors.Join`. Не удаляй чужие файлы: cleanup path ограничен путями, созданными текущим вызовом.

Run: `gofmt -w internal/backup && go test ./internal/backup -run='Test(Create|FormatBackupID|ParseMetadata)' -v`
Expected: PASS.

- [ ] **Step 4: Fault injection и commit**

Table-driven tests копируют полностью production create operations и заменяют один seam:

1. MkdirAll, Lstat source, openExclusive data, data Write, data Sync, data Close errors → ни data, ни sidecar не остаются; target не изменён.
2. Sidecar open, Write, Sync, Close errors → data cleanup; если cleanup ошибается, обе причины доступны через `errors.Is`.
3. Read-back snapshot/hash/metadata mismatch errors → оба созданных файла удалены, primary reason сохранён.
4. EEXIST data и EEXIST sidecar действительно переходят на `.1`; ELOOP от data open немедленно ошибка без перехода.
5. Nil operation field отклоняется до создания файлов.
6. Cleanup не удаляет заранее созданные user files с похожими именами.

Run: `gofmt -w internal/backup && go test ./internal/backup -v && go test ./... && go test -race ./internal/backup && go vet ./...`
Expected: PASS.

```bash
git add internal/backup
git commit -m "feat: add fstab backup creation"
```

### Task 2: Verify, List и Select

**Files:**
- Create: `internal/backup/verify.go`
- Create: `internal/backup/selection.go`
- Create: `internal/backup/verify_test.go`
- Create: `internal/backup/selection_test.go`

**Interfaces:**
- Consumes: Task 1 `Backup`, `parseMetadata`, `readVerifiedBackup`, errors.
- Produces: `Open`, `List`, `Select`, `Selection`, `ErrBackupNotFound`, `ErrBackupAmbiguous`.

- [ ] **Step 1: Написать failing verify tests**

`Open(dataPath)` проверяет data и производный sidecar; `Open(sidecarPath)` проверяет sidecar и data без расширения `.json`. Тесты через созданный валидный backup:

- Open по обоим путям возвращает одинаковые ID/paths/metadata/hash.
- Data bytes побайтово совпадают; sidecar strict JSON не имеет unknown keys.
- Повреждение одного байта data без обновления metadata → `ErrInvalidBackup`.
- Повреждение hex hash в metadata → ошибка валидации.
- Изменение source_path на другой canonical path → backup открывается, но не участвует в `List`/`Select` чужого target.
- Неправильный `schema_version`, operation, source verification, mode >07777, created_at не UTC, backup_id с суффиксом/неправильной длиной → ошибка.
- Замена data/sidecar symlink → `ErrInvalidBackup`; foreign sidecar `prefix.9999....json` без data не создаёт Backup.
- Data/sidecar mode не 0600 → `ErrInvalidBackup`, чтобы tampering был заметен. Тесты chmod только своих temp files.
- Empty/corrupt/trailing JSON sidecar → ошибка без panic.

Run: `go test ./internal/backup -run='TestOpen' -v`
Expected: FAIL, Open отсутствует.

- [ ] **Step 2: Реализовать Open/readVerifiedBackup**

`Open(path)` преобразует path в absolute clean. Если suffix `.json`, sidecar — исходный, data — strings.TrimSuffix; иначе data — исходный, sidecar — `path+".json"`. Оба пути отдельно `Lstat`: должны быть regular, не symlink. Затем `readVerifiedBackup(dataPath, sidecarPath)` использует `writer.ReadSnapshot` для обоих файлов, что повторно гарантирует regular/O_NOFOLLOW и читает bytes; проверяет exact mode 0600 через Stat_t, парсит metadata, сравнивает backup SHA-256 data, требует совпадения metadata ID и пары путей.

`readVerifiedBackup` является внутренним единым verify seam для `Create/Open/List/Select`: она не доверяет имени файла как доказательству принадлежности target, а проверяет metadata. Return `Backup` заполнен всеми каноническими absolute paths.

Run: `gofmt -w internal/backup && go test ./internal/backup -run='TestOpen' -v`
Expected: PASS.

- [ ] **Step 3: Написать failing List/Select tests**

Создай несколько backups с разными `Now` для одного target и один backup другого target с тем же prefix/directory. `List` должен вернуть только backups текущего canonical target/prefix, отсортированные newest first; равные timestamps упорядочиваются по backup ID, затем path. Symlink на чужой valid sidecar в directory пропускается; sidecar foreign target, corrupt JSON, неправильный prefix и sidecar без data пропускаются. Чужие data files не считаются backup без sidecar.

`Select` cases:

1. Empty ID выбирает newest eligible backup.
2. Existing ID выбирает exact ID, включая collision backup `.1`, если это единственный с этим ID.
3. Missing ID → `ErrBackupNotFound` с непустым Candidates.
4. Два файла (base и `.1`) одного ID → `ErrBackupAmbiguous`, Candidates содержат оба в deterministic newest/suffix order.
5. Prefix mismatch не находит копию, даже если sidecar валидный.
6. Directory symlink/final symlink entries не сканируются как backup и не удаляются (это также защита rotation).

Run: `go test ./internal/backup -run='Test(List|Select)' -v`
Expected: FAIL, API отсутствует.

- [ ] **Step 4: Реализовать List/Select**

`List(targetPath, prefix, directory)` canonicalizes target через `writer.CanonicalTargetPath`; canonicalizes directory, где empty означает parent target; prefix empty заменяется default. `os.ReadDir(directory)` читает только entry names; для каждого `.json` regular file пробует `Open` full sidecar path. Сторонние/corrupt entries пропускаются, но ошибки ReadDir/open отдельного entry не прерывают весь scan. Отфильтровывай:

- data/sidecar не regular/no-follow безопасности;
- metadata `source_path != canonical target`;
- filename не начинается с `prefix + "."`;
- metadata ID не является ядром имени после prefix и допустимого числового collision suffix.

`List` возвращает newest-first stable sort. `Select` вызывает List; empty ID берёт первый. Nonempty ID собирает exact matches; 0 → `ErrBackupNotFound`, >1 → `ErrBackupAmbiguous`, ровно 1 → Selected. `Selection.RequestedID` сохраняет исходный запрос, включая empty.

Run: `gofmt -w internal/backup && go test ./internal/backup -run='Test(List|Select)' -v && go test ./...`
Expected: PASS.

```bash
git add internal/backup
git commit -m "feat: verify and select fstab backups"
```

### Task 3: Safe rotation и README

**Files:**
- Create: `internal/backup/rotate.go`
- Create: `internal/backup/rotate_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `List`, `Select`, `Backup`, `RotationResult`, `DefaultKeepBackups`.
- Produces: `Rotate`, documented behavior for future transaction and rollback.

- [ ] **Step 1: Написать failing rotation tests**

Создай 12 валидных backups одного target в хронологическом порядке и два protected ID: один из candidates и один отсутствующий. Добавь foreign valid backup другого target, sidecar чужого prefix, symlink на чужой valid sidecar и corrupt `.json`. Вызови `Rotate(target, prefix, dir, 10, protectedIDs)`.

Проверь:

- Остаются newest 10 candidates; два самых старых удалены data+sidecar; protected удалён не был; foreign/corrupt/symlink untouched.
- `RotationResult.Removed` содержит удалённые в oldest→newest order; `Skipped` содержит retained/protected candidates; `Failed` пуст.
- keep <= 0 заменяется `DefaultKeepBackups`; keep >= len candidates ничего не удаляет.
- Удалённая пара больше не возвращается `List`.
- Заранее удалённая metadata/data не ломает rotation: backup исключается как incomplete.
- Защита selected rollback: переданный protected ID из candidates не удаляется даже при keep=0 (keep normalization не позволяет удалить всё, но protected всё равно проверяется явно).

Run: `go test ./internal/backup -run='TestRotate' -v`
Expected: FAIL, Rotate отсутствует.

- [ ] **Step 2: Реализовать Rotate**

`Rotate` вызывает `List`; any List failure до сканирования возвращается. Build protected set из non-empty exact backup IDs. Candidates сортируются oldest first stable. Если `keep <= 0`, используй `DefaultKeepBackups`.

Retention loop: каждый candidate после первых keep newest либо protected остаётся в `Skipped`; иначе удаляются sidecar и data через per-call `rotateOperations{remove}`. Production remove — `os.Remove`; nil remove — ошибка до первого deletion. Если data или sidecar уже отсутствует, удаление другого файла всё равно предпринимается; оба отсутствуют после cleanup — candidate считается Removed. Если removal fails, candidate помещается в `Failed`, возвращается joined error и дальнейшие deletion attempts не выполняются. Успешный удалённый candidate идёт в `Removed`.

Rotation никогда не вызывает `os.RemoveAll`, не читает entries через symlink и не удаляет чужие/непроверенные файлы. Ошибка rotation не содержит данных backup и не влияет на target.

Run: `gofmt -w internal/backup && go test ./internal/backup -run='TestRotate' -v`
Expected: PASS.

- [ ] **Step 3: Rotation fault injection и README**

Fault tests:

1. remove sidecar error → candidate `Failed`, error сохранён через `errors.Is`, data не удаляется, более новые non-protected копии не удаляются дальше.
2. remove data error → sidecar уже удалён, candidate `Failed`, partial result explicit.
3. remove одного файла возвращает `os.ErrNotExist`, а второй удаляется успешно → candidate Removed; осиротевшие paths не остаются.
4. keep=10, keep=11, keep=0 и protected absent/present.
5. Symlink entries/foreign valid backups остаются; нет hidden deletion.
6. `RotationResult` slices non-nil даже при empty.

Обнови README: реализованные Create/Open/List/Select/Rotate guarantees; exact backup ID format и sidecar schema; default prefix/keep; collision semantics; `source_verification` не заявляет работоспособность повреждённого исходника; backup не меняет target; rollback/transaction и dry-run ещё не подключены.

```bash
gofmt -w internal/backup
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
go test ./internal/backup -cover
```

Expected: все PASS, lint 0 issues, backup coverage не ниже 80%; зафиксируй фактический процент, проверь отсутствие созданных tests artifacts и `git status --short`.

```bash
git add internal/backup README.md
git commit -m "feat: rotate verified fstab backups"
```

## Финальная приёмка

1. Backup ID/core format, default prefix, UTC timestamp, data/sidecar filenames и collision suffix покрыты exact tests.
2. Create не изменяет target и возвращает Backup только после write, fsync, read-back и SHA-256 проверки.
3. Open/List/Select отклоняют symlink, corrupt schema/hash mismatch, неправильные modes/enums/UTC, чужой target и prefix.
4. List newest-first deterministic; Select различает not found и ambiguous collision ID.
5. Rotation default keep=10, удаляет только собственные verified pairs, сохраняет protected ID и foreign files, останавливается на removal failure с partial explicit result.
6. Cleanup и rotation errors сохраняют primary errors и не содержат backup data.
7. `go test ./...`, race, vet, lint 0 issues и coverage >=80% проходят на итоговом HEAD.
8. Отдельные task reviews и final whole-branch review без открытых Critical/Important findings; fast-forward merge, post-merge tests, cleanup worktree/branch, SSH push origin/main и clean synchronized status.
