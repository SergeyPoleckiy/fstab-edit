# План реализации — безопасный runner и findmnt verifier

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** реализовать безопасный запуск внешних утилит и статическую проверку fstab candidate через `findmnt --verify --tab-file`, возвращая единые diagnostics без монтирования ресурсов.

**Architecture:** `internal/sysutil.Runner` разрешает имя утилиты только в фиксированных системных каталогах и запускает absolute path через `exec.CommandContext` без shell. `FindmntVerifier` зависит от интерфейса `CommandRunner`, поэтому unit/integration tests используют fake runner; отдельная функция создаёт private candidate 0600 в каталоге 0700 и удаляет его после проверки. Raw stdout/stderr сохраняются в `RunResult`, а `models.Report` содержит только классифицированные сообщения без raw output/source/options.

**Tech Stack:** Go 1.22+, `os/exec`, `context`, `testing` helper process, fake runner, testdata fixtures, fuzz/race/vet/lint; новых зависимостей нет.

## Global Constraints

- Рабочая область: `/home/sergey/projects/fstab-editor/work/.worktrees/stage-1-findmnt`.
- Модуль `fstab-edit`, Go `1.22.0`; все файлы получают максимальные русские comments по ТЗ 4.3.
- Команда строго `findmnt --verify --tab-file <path>` через explicit argv; никакого shell, конкатенированной command line или реального монтирования.
- Executable search только в `/usr/bin`, `/bin`, `/usr/sbin`, `/sbin` в указанном порядке; PATH и current directory не участвуют.
- Runner устанавливает `LC_ALL=C`, имеет timeout/cancellation и ограничивает stdout и stderr независимо. Defaults: 10 секунд и 64 KiB на stream.
- Результат сохраняет exit code, stdout, stderr, duration, timeout и признаки truncation.
- Unit/integration tests findmnt не запускают настоящий findmnt; Runner tests используют Go test helper process. Реальный `/etc/fstab` не читается и не изменяется.
- Проверка candidate использует только временный каталог 0700 и файл 0600 с исходными bytes; candidate удаляется после вызова.
- Raw stdout/stderr и аргументы не копируются в Diagnostic Message/Value/Params. `Verification.Execution` хранит raw output для последующего controlled display; logger ещё не реализован и не должен получать его без redaction.
- Классификация зависит от `[E]`, `[W]`, `parse error at line N` и summary, а не только от exit code. Timeout/start/cancellation/truncation — error.
- Формат зафиксирован на util-linux `2.42.4`; другие версии не объявляются проверенными без fixtures.
- `MergeReports` объединяет internal/external reports в порядке аргументов и всегда возвращает non-nil Diagnostics.
- План не создаёт CLI, lock/writer, backup, transaction, config или logger.

---

## Структура файлов и внешние коды

- `internal/sysutil/runner.go` — fixed-directory resolution, limited buffers, timeout и exec.
- `internal/sysutil/runner_test.go` — helper process и resolution/run tests.
- `internal/sysutil/findmnt.go` — интерфейс runner, Verify и классификация результата.
- `internal/sysutil/candidate.go` — private candidate lifecycle.
- `internal/sysutil/findmnt_test.go` — argv, fixtures, exit/output classification и redaction.
- `internal/sysutil/candidate_test.go` — bytes/mode/cleanup и объединение с internal validator.
- `internal/sysutil/fuzz_test.go` — детерминизм и устойчивость классификатора.
- `internal/sysutil/testdata/findmnt/*.stdout|stderr` — наблюдаемые и синтетические classification fixtures.
- `internal/sysutil/testdata/findmnt/README.md` — русское описание fixtures и версии 2.42.4.
- `pkg/models/diagnostic.go` — `MergeReports`.
- `pkg/models/diagnostic_test.go` — merge contract.
- `README.md` — статус и границы findmnt adapter.

| Code | Severity | Причина |
|------|----------|---------|
| `findmnt.invalid_path` | error | пустой candidate/target path |
| `findmnt.unavailable` | error | nil runner, resolution или start error |
| `findmnt.timeout` | error | deadline exceeded |
| `findmnt.canceled` | error | отмена parent context |
| `findmnt.output_truncated` | error | хотя бы один output stream превысил limit |
| `findmnt.parse_error` | error | findmnt сообщил parse error |
| `findmnt.error` | error | `[E]` или summary errors |
| `findmnt.warning` | warning | `[W]` или summary warnings без detail lines |
| `findmnt.failed` | error | nonzero exit без распознанной error classification |
| `findmnt.candidate` | error | создание/запись private candidate не удалась |
| `findmnt.candidate_cleanup` | warning | временный candidate не удалось удалить |

### Task 1: Безопасный Runner внешних утилит

**Files:**
- Create: `internal/sysutil/runner.go`
- Create: `internal/sysutil/runner_test.go`

**Interfaces:**
- Consumes: стандартная библиотека.
- Produces:
  - `type CommandRunner interface { Run(context.Context, string, ...string) RunResult }`;
  - `type Runner struct { Timeout time.Duration; OutputLimit int }`;
  - `func NewRunner() *Runner`;
  - `func ResolveExecutable(name string) (string, error)`;
  - `func (r *Runner) Run(ctx context.Context, name string, args ...string) RunResult`;
  - `type RunResult` с полями `Path string`, `ExitCode int`, `Stdout string`, `Stderr string`, `Duration time.Duration`, `TimedOut bool`, `StdoutTruncated bool`, `StderrTruncated bool`, `Err error`;
  - `ErrInvalidExecutable`, `ErrExecutableNotFound`.

- [ ] **Step 1: Написать failing resolution tests**

Создать `runner_test.go` в package `sysutil` для доступа к private `resolveInDirectories` и `runAbsolute`. Public behavior дополнительно проверяется через exported API. Каждый test/case получает русский comment.

Тесты resolution через temp dirs:

```go
func TestResolveInDirectories(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	// Оба каталога содержат executable: должен победить первый fixed directory,
	// независимо от PATH и порядка чтения directory entries.
	firstTool := filepath.Join(first, "tool")
	secondTool := filepath.Join(second, "tool")
	if err := os.WriteFile(firstTool, []byte("first"), 0o755); err != nil { t.Fatal(err) }
	if err := os.WriteFile(secondTool, []byte("second"), 0o755); err != nil { t.Fatal(err) }
	got, err := resolveInDirectories("tool", []string{first, second})
	if err != nil || got != firstTool {
		t.Fatalf("resolution: got %q, err=%v", got, err)
	}
}
```

Отдельно проверить: non-executable regular file пропускается; directory с именем tool пропускается; symlink на regular executable разрешается через `os.Stat` (нужно для `/bin` → `/usr/bin`); пустое имя, `.`, `tool/name`, `../tool` и absolute name дают `ErrInvalidExecutable`; отсутствие даёт `ErrExecutableNotFound`; returned path absolute. `ResolveExecutable("definitely-not-present-fstab-edit")` должен вернуть not found, не используя PATH.

Run: `go test ./internal/sysutil -run=TestResolve -v`
Expected: FAIL, package/API отсутствуют.

- [ ] **Step 2: Реализовать resolution**

В `runner.go` добавить file/package comments и fixed list:

```go
var executableDirectories = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

func ResolveExecutable(name string) (string, error) {
	return resolveInDirectories(name, executableDirectories)
}
```

`resolveInDirectories` отклоняет `name == ""`, `name == "."`, `filepath.Base(name) != name` и `filepath.IsAbs(name)` как `ErrInvalidExecutable`. Для каждого directory строит absolute candidate, применяет `os.Stat`, пропускает ошибки `IsNotExist`, directory, non-regular и `mode.Perm()&0111 == 0`. Возвращает первый executable. Ошибки Stat, отличные от not-exist, сохранить и при отсутствии executable вернуть wrapped error; иначе `ErrExecutableNotFound`. Не использовать `exec.LookPath` и PATH.

- [ ] **Step 3: Написать failing helper-process tests Runner**

`TestRunnerHelperProcess` запускается только как отдельный process через `-test.run=TestRunnerHelperProcess -- <mode>`. При обычном parent suite возвращается. Modes:

- `success`: stdout `out`, stderr `err`, print `LC_ALL=<value>`, exit 0;
- `exit7`: exit 7 после output;
- `sleep`: sleep 5 секунд;
- `large`: stdout 128 повторов `A`, stderr 128 повторов `B`.

Использовать `runAbsolute(context, os.Args[0], args...)` в tests, поэтому fixed-directory resolver не мешает helper binary. Проверить:

1. Success: ExitCode 0, Path == os.Args[0], raw stdout/stderr, LC_ALL=C, Duration >= 0, Err nil, no timeout/truncation.
2. Exit7: ExitCode 7, output сохранён, Err не nil как `exec.ExitError`.
3. Timeout: Runner.Timeout 30 ms, mode sleep → TimedOut true, `errors.Is(Err, context.DeadlineExceeded)`, ExitCode nonzero.
4. Parent cancellation: canceled context → TimedOut false, `errors.Is(Err, context.Canceled)`.
5. Limit 8: stdout/stderr ровно 8 bytes, оба Truncated true; writer не возвращает short write и process завершается.
6. Start failure: absolute nonexistent path → ExitCode -1, Err не nil, no panic.
7. Zero-value Runner применяет defaults; не тестировать реальный 10s timeout.

Run: `go test ./internal/sysutil -run='TestRunner' -v`
Expected: FAIL, runAbsolute/RunResult ещё не реализованы.

- [ ] **Step 4: Реализовать limited buffer и Runner**

Defaults:

```go
const (
	DefaultTimeout     = 10 * time.Second
	DefaultOutputLimit = 64 * 1024
)
```

`limitedBuffer.Write(p)` возвращает исходный `len(p), nil`, сохраняет только оставшиеся bytes до limit и выставляет truncated при отброшенной части. Это предотвращает short write и неограниченный рост памяти. Каждый stream имеет собственный buffer; дополнительная синхронизация между двумя writers не требуется.

Runner использует `exec.CommandContext` только с absolute resolved path и переданным args slice. Nil context заменить `context.Background()`. Timeout <= 0 и OutputLimit <= 0 означают defaults. Для timeout создать child context; не путать parent cancel с deadline.

Окружение: скопировать `os.Environ()`, удалить существующие `LC_ALL=` entries и добавить один `LC_ALL=C`. Не очищать остальное окружение: test helper и системные утилиты могут требовать system variables.

Инициализировать ExitCode=-1 до запуска. После `cmd.Run` заполнить ProcessState.ExitCode при наличии, Duration через `time.Since(start)`. При `errors.Is(runCtx.Err(), context.DeadlineExceeded)` установить TimedOut и Err DeadlineExceeded; при parent cancellation — Err Canceled; иначе сохранить фактический run error. Не включать args в error/diagnostics.

- [ ] **Step 5: Проверить и закоммитить**

Run: `gofmt -w internal/sysutil && go test ./internal/sysutil -v && go test ./... && go test -race ./internal/sysutil`
Expected: PASS без helper output noise в parent suite.

```bash
git add internal/sysutil/runner.go internal/sysutil/runner_test.go
git commit -m "feat: add secure external command runner"
```

### Task 2: Findmnt Verify и классификация fixtures

**Files:**
- Create: `internal/sysutil/findmnt.go`
- Create: `internal/sysutil/findmnt_test.go`
- Create: `internal/sysutil/testdata/findmnt/valid-2.42.4.stdout`
- Create: `internal/sysutil/testdata/findmnt/warnings-2.42.4.stdout`
- Create: `internal/sysutil/testdata/findmnt/errors-2.42.4.stdout`
- Create: `internal/sysutil/testdata/findmnt/errors-2.42.4.stderr`
- Create: `internal/sysutil/testdata/findmnt/README.md`
- Modify: `pkg/models/diagnostic.go`
- Modify: `pkg/models/diagnostic_test.go`

**Interfaces:**
- Consumes: `CommandRunner`, `RunResult`, `models.Report`.
- Produces:
  - `type Verification struct { Report models.Report; Execution RunResult }`;
  - `type FindmntVerifier struct { runner CommandRunner }`;
  - `func NewFindmntVerifier(runner CommandRunner) *FindmntVerifier`;
  - `func (v *FindmntVerifier) Verify(ctx context.Context, path string) Verification`;
  - `func MergeReports(reports ...Report) Report` в models.

- [ ] **Step 1: Создать fixtures и failing tests**

README testdata на русском: valid/warnings/errors основаны на наблюденном выводе util-linux 2.42.4 на временных файлах; raw fixtures могут содержать device names только как synthetic examples и никогда не попадают в Report. Не заявлять совместимость с другими версиями.

Fixture valid:

```text
0 parse errors, 0 errors, 0 warnings
```

Warnings fixture:

```text
0 parse errors, 0 errors, 3 warnings
/
   [W] ext4 does not match with on-disk btrfs
none
   [W] unreachable source: /dev/sda2: No such file or directory
   [W] cannot detect on-disk filesystem type (No such file or directory)
```

Errors stdout:

```text
2 parse errors, 2 errors, 3 warnings
relative
   [E] unreachable on boot required target: No such file or directory
   [W] ext4 does not match with on-disk btrfs
/mnt/data
   [E] unreachable on boot required target: No such file or directory
   [W] unreachable source: /dev/sda3: No such file or directory
   [W] cannot detect on-disk filesystem type (No such file or directory)
```

Errors stderr:

```text
findmnt: /private/candidate: parse error at line 3 -- ignored
findmnt: /private/candidate: parse error at line 4 -- ignored
```

Fake runner сохраняет ctx/name/args и возвращает заранее заданный RunResult. Тест Verify проверяет name `findmnt`, args ровно `--verify`, `--tab-file`, candidate path; path не concat с flags; result Execution сохраняет исходный stdout/stderr/ExitCode/Duration.

Classification tests:

1. Empty path → `findmnt.invalid_path/error`, runner не вызван.
2. Nil runner → `findmnt.unavailable/error`, no panic.
3. Valid exit 0 → non-nil empty Report.
4. Warnings exit 0 → только `findmnt.warning/warning`, HasErrors false.
5. Errors fixture exit 1 → parse diagnostics Line 3/4, `[E]` errors, `[W]` warnings; raw `/dev/sda3` и candidate path отсутствуют в Report JSON, но сохранены в Execution.
6. `[E]` при exit 0 всё равно error.
7. Summary `1 parse errors, 1 errors, 0 warnings` без detail lines создаёт error classification.
8. Nonzero exit с одними `[W]` добавляет `findmnt.failed/error`, потому что причина exit не объяснена error findings.
9. Nonzero exit без output → `findmnt.failed/error` с numeric Params `exit_code`; Message не содержит raw args/output.
10. Start Err → unavailable; TimedOut → timeout; context.Canceled Err → canceled. Эти результаты не классифицируются как обычный successful output.
11. Любой Truncated → `findmnt.output_truncated/error`; truncated partial `[W]` не может привести к HasErrors false.
12. Любой accepted external diagnostic имеет непустой Code/Message и допустимую Severity; Report никогда не содержит raw stdout/stderr markers.

Run: `go test ./internal/sysutil -run='TestFindmnt' -v`
Expected: FAIL, Verify/classifier отсутствуют.

- [ ] **Step 2: Реализовать Verify и pure classifier**

`FindmntVerifier` хранит только interface. Verify проверяет path/runner, затем вызывает:

```go
execution := v.runner.Run(ctx, "findmnt", "--verify", "--tab-file", path)
return Verification{Report: classifyFindmnt(execution), Execution: execution}
```

`classifyFindmnt` создаёт non-nil Report и не вызывает runner/I/O. При Err: timeout/canceled/start code выбирается по TimedOut и errors.Is; добавить truncation при наличии. Не включать `Err.Error()` в Report: ошибка exec может содержать path, а будущие runners — иные sensitive данные. Numeric exit code допустим.

Для обычного результата:

1. Разобрать stderr regexp `parse error at line ([0-9]+)`; каждый match → `findmnt.parse_error/error`, Line parsed decimal. Candidate path вокруг match не копируется.
2. Разобрать stdout построчно. Trimmed `[E]` → `findmnt.error/error`; `[W]` → `findmnt.warning/warning`. Detail text, heading и source не копируются в Diagnostic.
3. Summary regexp `([0-9]+) parse errors?, ([0-9]+) errors?, ([0-9]+) warnings?`. Если parse/errors/warnings count > 0, но соответствующих detail diagnostics нет, добавить одну generic diagnostic соответствующего code/severity. Не создавать summary warning, если уже есть `[W]`.
4. ExitCode != 0 и отсутствие error diagnostics после content classification → `findmnt.failed/error` с Params `exit_code`. Nonzero exit с `[E]`/parse error не требует дополнительного generic failure.
5. ExitCode 0 не удаляет errors. Warnings сами по себе не меняют ExitCode и не блокируют.
6. Truncation всегда добавляет error, даже если частичный output выглядит successful.

Все messages русские и generic, Value пустой. Допустимые Params: `exit_code`, summary `count`; они numeric и не раскрывают output.

- [ ] **Step 3: Реализовать MergeReports и tests**

В models:

```go
func MergeReports(reports ...Report) Report {
	total := 0
	for _, report := range reports { total += len(report.Diagnostics) }
	merged := Report{Diagnostics: make([]Diagnostic, 0, total)}
	for _, report := range reports {
		merged.Diagnostics = append(merged.Diagnostics, report.Diagnostics...)
	}
	return merged
}
```

Добавить полный Russian API comment. Tests: nil/empty args → non-nil empty; internal error + external warning сохраняют порядок и HasErrors; исходные slices не меняются и не разделяют backing array с merged при последующем append.

- [ ] **Step 4: Проверить и закоммитить**

Run: `gofmt -w internal/sysutil pkg/models && go test ./internal/sysutil ./pkg/models -v && go test ./...`
Expected: PASS.

```bash
git add internal/sysutil pkg/models
git commit -m "feat: classify findmnt verification results"
```

### Task 3: Private candidate, fuzz и документация

**Files:**
- Create: `internal/sysutil/candidate.go`
- Create: `internal/sysutil/candidate_test.go`
- Create: `internal/sysutil/fuzz_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `CommandRunner`, `FindmntVerifier`, `Verification`, `MergeReports`, `validator.Validate`.
- Produces: `func VerifyCandidate(ctx context.Context, runner CommandRunner, candidate []byte) Verification`.

- [ ] **Step 1: Написать failing candidate lifecycle test**

Fake runner в Run проверяет, что args[2] — absolute path внутри созданного temp dir; читает файл и сравнивает bytes с candidate, включая invalid UTF-8 и отсутствие final newline; `os.Stat` проверяет regular file 0600 и parent directory 0700 (учесть только Perm bits). Возвращает successful empty output. После VerifyCandidate test проверяет, что captured path и parent больше не существуют. Исходный candidate slice не изменён.

Дополнительные tests:

- пустой candidate создаёт пустой файл и всё равно вызывает verify;
- runner видит только candidate path, а не `/etc/fstab`;
- internal `validator.Validate(parser.Parse(candidate))` и external report объединяются через `MergeReports`: internal error идёт до external warning, HasErrors true;
- cleanup происходит и при runner error/timeout;
- VerifyCandidate не монтирует ресурсы: fake runner является единственной внешней точкой и получает только findmnt verify argv.

Run: `go test ./internal/sysutil -run='TestVerifyCandidate' -v`
Expected: FAIL, функция отсутствует.

- [ ] **Step 2: Реализовать candidate.go**

Создать `os.MkdirTemp("", "fstab-edit-candidate-*")` (MkdirTemp создаёт 0700), затем `filepath.Join(dir, "candidate.fstab")` и `os.WriteFile(..., 0600)`. Не добавлять newline и не нормализовать bytes. Вызвать `NewFindmntVerifier(runner).Verify(ctx, path)` только после успешной записи.

Ошибки create/write возвращают Verification с `findmnt.candidate/error`, Execution.ExitCode=-1 и Err; Document/candidate не затрагиваются. Не включать temp path или file contents в Diagnostic.

Использовать named return и defer RemoveAll: после основной проверки удалить весь private dir. При ошибке cleanup добавить `findmnt.candidate_cleanup/warning` и сохранить первичный Report/Execution.Err. Cleanup error не должен перезаписывать первичную ошибку. При ошибке записи тоже попытаться удалить dir.

- [ ] **Step 3: Добавить fuzz классификатора**

В package `sysutil` создать `FuzzClassifyFindmnt(stdout, stderr string, exitCode int)`. Нормализовать exit code к -1/0/1 только в seed wrapper не нужно: classifier обязан выдержать любой int. Установить Err nil и truncation false. Перед вызовом обернуть fuzz strings в устойчивые маркеры `sensitiveStdout := "do-not-leak-stdout:" + stdout` и `sensitiveStderr := "do-not-leak-stderr:" + stderr`; именно эти полные значения передать classifier и искать в JSON Report.

Инварианты:

- no panic; Report.Diagnostics non-nil;
- два вызова `classifyFindmnt` дают reflect.DeepEqual;
- Code/Message непустые, Severity только error/warning/info;
- JSON Report не содержит полные `sensitiveStdout`/`sensitiveStderr`, поскольку classifier не копирует output; маркерный prefix исключает случайные совпадения с короткими numeric diagnostics;
- если любая stdout line после TrimSpace начинается с `[E]` и exit любой, HasErrors true; если exit 0 и output только пустой/success summary, HasErrors false;
- raw Execution остаётся отдельной от Report ответственностью Verify, fuzz не сериализует Execution как безопасный для логов объект.

Run: `go test ./internal/sysutil -run=FuzzClassifyFindmnt -fuzz=FuzzClassifyFindmnt -fuzztime=3s`
Expected: PASS.

- [ ] **Step 4: Обновить README**

Отметить реализованные secure runner, findmnt verifier и private candidate check. Задокументировать:

- проверенный формат util-linux 2.42.4;
- timeout 10s и output limit 64 KiB/stream по умолчанию;
- findmnt только статически проверяет candidate и ничего не монтирует;
- raw stdout/stderr потенциально sensitive и не предназначены для записи в log без redaction;
- transaction ещё не вызывает verifier, lock/writer/backup и CLI остаются следующими задачами.

- [ ] **Step 5: Полные проверки и commit**

```bash
gofmt -w internal/sysutil pkg/models
go test ./...
go vet ./...
go test -race ./...
golangci-lint run
go test ./... -cover
go test ./internal/sysutil -run=FuzzClassifyFindmnt -fuzz=FuzzClassifyFindmnt -fuzztime=3s
```

Expected: все PASS, lint 0 issues; записать фактический coverage. Проверить, что test helper не оставил processes/temp candidates и git status содержит только запрошенные файлы.

```bash
git add internal/sysutil README.md
git commit -m "feat: verify private fstab candidates"
```

## Финальная приёмка

1. Все findmnt tests используют fake runner; Runner tests используют helper process; реальный `/etc/fstab` не затрагивается.
2. `findmnt --verify --tab-file <path>` передаётся как четыре отдельных argv после executable; shell отсутствует.
3. Timeout/cancel/start/truncation и content classification имеют direct tests.
4. Exit 0 с `[E]` блокирует; exit 0 с warnings не блокирует; nonzero warnings-only получает unexplained failure error.
5. Report не содержит raw output/candidate path/source; Execution сохраняет raw output отдельно.
6. Candidate bytes/modes/cleanup проверены, MergeReports сохраняет порядок internal → external.
7. Whole-branch review clean; после merge `go test ./...`, clean status и push SSH `origin/main`.
