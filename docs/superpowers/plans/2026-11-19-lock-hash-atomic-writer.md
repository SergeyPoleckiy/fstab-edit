# Lock, Base Hash and Atomic Writer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** реализовать безопасное чтение target с SHA-256 базовой версии, стабильную эксклюзивную блокировку через отдельный lock-файл и атомарную замену candidate с сохранением metadata и диагностикой точки сбоя.

**Architecture:** пакет `internal/writer` открывает target только как regular file с `O_NOFOLLOW`, читает snapshot через один descriptor и сравнивает базовый SHA-256 после повторного чтения. `Lock` владеет отдельным стабильным файлом в защищённом runtime-каталоге и использует неблокирующий `flock` с context/timeout. Низкоуровневый `Replace` создаёт temp 0600 в каталоге target, копирует UID/GID/mode/xattrs через descriptors, выполняет file sync → rename → directory sync → hash verification; `ReplaceResult.Replaced` отличает ошибки до и после точки замены.

**Tech Stack:** Go 1.22+, Linux, `crypto/sha256`, `os`, `path/filepath`, `context`, `golang.org/x/sys/unix v0.29.0` (Go 1.18+), стандартные `testing` и temp files; Cobra, backup и transaction в этот план не входят.

## Global Constraints

- Рабочая область выполнения: `/home/sergey/projects/fstab-editor/work/.worktrees/stage-1-atomic-writer`.
- Модуль `fstab-edit`, Go `1.22.0`; публичные API, файлы и пошаговая логика получают максимальные содержательные русские comments по ТЗ 4.3.
- Реальный `/etc/fstab` не читается и не изменяется. Все тесты используют `t.TempDir`; root, заполнение диска и read-only FS не требуются.
- Target обязан существовать и быть regular file. Symlink, directory, FIFO, socket и device отклоняются до записи; descriptor дополнительно проверяется после `O_NOFOLLOW`.
- Относительный target преобразуется в canonical absolute path. Имя lock содержит SHA-256 именно canonical path, а lock никогда не создаётся на inode target.
- Lock приобретается только явным вызовом записывающего слоя и удерживается до `Close`; read-only API не вызывает lock. Lock-файл после `Close` не удаляется.
- Base hash — SHA-256 исходных bytes. При отличии повторного snapshot возвращается `ErrBaseHashConflict`; автоматического merge или overwrite нет.
- Temp создаётся через `os.CreateTemp` в parent directory target, то есть с `O_EXCL`, на той же ФС и с начальным режимом 0600.
- До rename обязательны полная запись, сохранение UID/GID/mode/всех доступных xattrs и file `fsync`; после rename обязателен parent directory `fsync` и повторная проверка SHA-256.
- Любая ошибка metadata/security xattr до rename запрещает замену. Ошибка после rename возвращается с `Replaced=true`; низкоуровневый writer не выполняет rollback самостоятельно.
- Cleanup не скрывает первичную ошибку: используется `errors.Join`, а `errors.Is` по первичной причине продолжает работать.
- Fault injection реализуется через per-call operations struct, а не через глобальные mutable seams. Тесты не используют `t.Parallel` там, где изменяют реальный temp target.
- План не создаёт backup, sidecar, transaction, automatic rollback, CLI, config, logger или dry-run оркестрацию.

---

## Структура файлов и внешние контракты

- `internal/writer/target.go` — canonical path, secure open, `Snapshot`, base hash conflict.
- `internal/writer/target_test.go` — exact bytes, hash conflict и запрещённые file types.
- `internal/writer/lock.go` — secure runtime directory, stable path, `flock`, timeout и release.
- `internal/writer/lock_test.go` — реальные flock на temp files, права, timeout, cancel и повторный acquire.
- `internal/writer/metadata.go` — descriptor-based UID/GID/mode/xattrs copy.
- `internal/writer/atomic.go` — temp lifecycle, sync/rename/sync, post-replace hash и cleanup.
- `internal/writer/atomic_test.go` — success, metadata/xattrs, order и fault injection до/после rename.
- `go.mod`, `go.sum` — единственная новая dependency `golang.org/x/sys v0.29.0`.
- `README.md` — реализованные гарантии и границы низкоуровневого writer.

Экспортируемые типы и sentinel errors:

```go
var (
	ErrInvalidTarget          = errors.New("недопустимый целевой файл")
	ErrBaseHashConflict       = errors.New("целевой файл изменился после базового чтения")
	ErrLockTimeout            = errors.New("истекло время ожидания блокировки")
	ErrPostReplaceVerification = errors.New("проверка файла после атомарной замены не пройдена")
)

type Snapshot struct {
	Path   string
	Data   []byte
	SHA256 [32]byte
}

func CanonicalTargetPath(path string) (string, error)
func ReadSnapshot(path string) (Snapshot, error)
func VerifyBaseHash(path string, expected [32]byte) (Snapshot, error)

type Lock struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	closed bool
}
func AcquireLock(ctx context.Context, targetPath string, timeout time.Duration) (*Lock, error)
func (l *Lock) Path() string
func (l *Lock) Close() error

type ReplaceResult struct {
	Path     string
	SHA256   [32]byte
	Replaced bool
}
func Replace(path string, candidate []byte) (ReplaceResult, error)
```

### Task 1: Secure target snapshot и контроль base SHA-256

**Files:**
- Create: `internal/writer/target.go`
- Create: `internal/writer/target_test.go`
- Modify: `go.mod`
- Create: `go.sum`

**Interfaces:**
- Consumes: `golang.org/x/sys/unix`, стандартную библиотеку.
- Produces: `Snapshot`, `CanonicalTargetPath`, `ReadSnapshot`, `VerifyBaseHash`, `ErrInvalidTarget`, `ErrBaseHashConflict` и внутренний `openTargetFile(path string) (*os.File, error)` для atomic writer.

- [ ] **Step 1: Написать failing tests snapshot и конфликта**

Создать тесты в package `writer`, каждый test и нетривиальная проверка получают русский comment. Базовый сценарий записывает binary bytes с invalid UTF-8 и без final newline, вызывает `ReadSnapshot` по относительному пути после `os.Chdir` в temp directory и проверяет absolute `Path`, exact `Data` и `sha256.Sum256`. `os.Chdir` восстанавливается через `t.Cleanup`; тесты не параллельны.

```go
func TestVerifyBaseHashDetectsExternalChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	// Изменение имеет ту же длину: проверяются bytes, а не size или mtime.
	if err := os.WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := VerifyBaseHash(path, before.SHA256)
	if !errors.Is(err, ErrBaseHashConflict) {
		t.Fatalf("ожидался конфликт base hash, получено: %v", err)
	}
	if string(current.Data) != "new\n" {
		t.Fatalf("current snapshot должен описывать фактическую версию: %q", current.Data)
	}
}
```

Дополнительные случаи: неизменённый target возвращает snapshot без ошибки; empty file получает SHA-256 пустого содержимого; missing path, пустой path, symlink на regular file, directory и FIFO возвращают ошибку с `errors.Is(err, ErrInvalidTarget)`; symlink не открывается и его destination не читается. Прямой вызов внутреннего `openTargetFile` с symlink path отдельно доказывает enforcement `O_NOFOLLOW` на уровне descriptor, а не только предварительной проверки `Lstat`. FIFO создаётся через `unix.Mkfifo`, поэтому тест доказывает отсутствие блокирующего open.

Run: `go test ./internal/writer -run='Test(ReadSnapshot|VerifyBaseHash|CanonicalTargetPath)' -v`
Expected: FAIL, package/API отсутствуют.

- [ ] **Step 2: Добавить dependency и реализовать canonical/secure read**

Добавить зафиксированную dependency, не обновляя остальные модули:

```bash
go mod edit -require=golang.org/x/sys@v0.29.0
```

`CanonicalTargetPath` отклоняет пустой путь, вызывает `filepath.Abs`, затем `os.Lstat`: symlink и любой non-regular mode оборачиваются в `ErrInvalidTarget`. `filepath.EvalSymlinks` разрешает symlink в parent components; повторный `Lstat` resolved path обязан показать regular file. Возвращается cleaned absolute path.

`openTargetFile` использует только canonical path и descriptor flags:

```go
fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
if err != nil {
	return nil, fmt.Errorf("%w: открыть target %s: %w", ErrInvalidTarget, path, err)
}
file := os.NewFile(uintptr(fd), path)
var stat unix.Stat_t
if err := unix.Fstat(fd, &stat); err != nil {
	file.Close()
	return nil, fmt.Errorf("прочитать metadata target: %w", err)
}
if stat.Mode&unix.S_IFMT != unix.S_IFREG {
	file.Close()
	return nil, fmt.Errorf("%w: target не является regular file", ErrInvalidTarget)
}
return file, nil
```

`ReadSnapshot` вызывает canonical/open, затем `io.ReadAll` именно из проверенного descriptor. Named return и deferred Close сохраняют первичную ошибку через `errors.Join`; успешный close error тоже возвращается. Результат содержит новый data slice, canonical path и `sha256.Sum256(data)`.

`VerifyBaseHash` повторно вызывает `ReadSnapshot`; если `current.SHA256 != expected`, возвращает current snapshot и ошибку `fmt.Errorf("%w: %s", ErrBaseHashConflict, current.Path)`. Current snapshot предназначен только для отображения конфликта/повторной загрузки и не должен использоваться как разрешение записи. Успех возвращает current snapshot и nil.

Run: `go mod tidy && gofmt -w internal/writer/target.go internal/writer/target_test.go && go test ./internal/writer -v`
Expected: PASS; go.sum содержит только x/sys checksum entries.

- [ ] **Step 3: Проверить пакет и закоммитить**

Run: `go test ./... && go test -race ./internal/writer && go vet ./...`
Expected: PASS.

```bash
git add go.mod go.sum internal/writer/target.go internal/writer/target_test.go
git commit -m "feat: add target snapshots and base hash checks"
```

### Task 2: Стабильный отдельный flock с timeout

**Files:**
- Create: `internal/writer/lock.go`
- Create: `internal/writer/lock_test.go`

**Interfaces:**
- Consumes: `CanonicalTargetPath` из Task 1.
- Produces: `DefaultLockTimeout = 10 * time.Second`, `AcquireLock`, `Lock.Path`, `Lock.Close`, `ErrLockTimeout`.
- Internal test seam: immutable per-call `lockConfig` и `acquireLockWithConfig`, чтобы проверять system/XDG/fallback paths без `/etc`, `/run/lock` и mutable globals.

- [ ] **Step 1: Написать failing tests lock path и взаимного исключения**

Internal config имеет точную форму:

```go
type lockConfig struct {
	systemTarget    string
	systemDirectory string
	runtimeDirectory string
	cacheDirectory  string
	uid             int
}
```

Тест создаёт target в temp dir и config, где target считается system target, а systemDirectory находится в том же temp dir. Первый `acquireLockWithConfig` обязан создать directory 0700 и regular lock 0600, принадлежащие `os.Geteuid`. Ожидаемое имя:

```go
canonical, err := CanonicalTargetPath(target)
if err != nil { t.Fatal(err) }
name := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(canonical)))
expected := filepath.Join(config.systemDirectory, name)
```

Проверить, что `lock.Path() == expected`, target bytes не изменились, lock path отличается от target. Второй acquire с timeout 30 ms возвращает `ErrLockTimeout`; после первого `Close` повторный acquire успешен. Два `Close` подряд безопасны и идемпотентны. Lock-файл остаётся на месте после release, сохраняя stable inode для будущих процессов.

Дополнительные tests:

1. Два разных canonical target получают разные lock paths.
2. Parent symlink canonicalizes до одного lock path для двух способов адресации одного target; final target symlink отклоняется ещё в Task 1.
3. Уже созданный lock directory 0755, lock file 0644, symlink lock file и lock directory с чужим simulated UID отклоняются без chmod/unlink.
4. XDG runtime directory 0700 используется как `$XDG_RUNTIME_DIR/fstab-edit`; небезопасный XDG directory возвращает ошибку, а не молча переключается на fallback.
5. При пустом XDG используется стабильный `<cache>/fstab-edit/runtime-<uid>` с owner/mode 0700; `os.TempDir` и общий world-writable directory не используются.
6. Отменённый context прекращает ожидание с `context.Canceled`; истёкший context — с `context.DeadlineExceeded`; timeout <= 0 выбирает default, что проверяется отдельным тестом нормализации без реального 10-секундного ожидания.
7. После failed timeout descriptor закрывается: повторное освобождение первого lock позволяет acquire без ложной занятости.

Run: `go test ./internal/writer -run='Test(Lock|AcquireLock)' -v`
Expected: FAIL, lock API отсутствует.

- [ ] **Step 2: Реализовать secure directory и stable open**

`AcquireLock` нормализует nil context к `context.Background`, timeout <= 0 к `DefaultLockTimeout`, вызывает `CanonicalTargetPath` и собирает production `lockConfig` из `/etc/fstab`, `/run/lock/fstab-edit`, `os.Getenv("XDG_RUNTIME_DIR")`, `os.UserCacheDir()` и `os.Geteuid()`. Ошибка отсутствия home/cache возвращается до создания файлов.

`acquireLockWithConfig` выбирает directory:

- canonical == systemTarget → systemDirectory; non-root получает понятную permission error;
- non-system с nonempty runtimeDirectory → `<runtime>/fstab-edit`;
- non-system без runtime → `<cache>/fstab-edit/runtime-<uid>`.

Для XDG base сначала `Lstat`: обязателен real directory, owner UID текущего пользователя и mode 0700. Затем `os.MkdirAll(child, 0700)` и отдельная проверка final directory через `Lstat` + `Stat_t`: no symlink, directory, exact owner и exact mode 0700. Существующие более широкие права не исправляются молча. Для system directory требуется UID 0 владельца и вызывающего процесса.

Lock filename — lowercase hex SHA-256 canonical target + `.lock`. Открытие выполняется `unix.Open(O_RDWR|O_CREAT|O_NOFOLLOW|O_CLOEXEC, 0600)`. `Fstat` подтверждает regular file, owner UID и exact mode 0600. При любой ошибке descriptor закрывается; symlink не удаляется и не перезаписывается.

- [ ] **Step 3: Реализовать flock wait и идемпотентный Close**

Неблокирующий цикл использует `unix.Flock(fd, LOCK_EX|LOCK_NB)`. `EINTR` повторяется немедленно; `EWOULDBLOCK`/`EAGAIN` ожидают ticker 20 ms, timer timeout или ctx.Done. Остальные ошибки возвращаются с операцией и lock path. Timer/ticker останавливаются. Timeout оборачивает `ErrLockTimeout`, context сохраняет `errors.Is` для canceled/deadline.

```go
type Lock struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	closed bool
}
```

`Close` под mutex проверяет `closed`, затем выполняет `LOCK_UN` и `file.Close` даже при ошибке unlock, объединяя ошибки через `errors.Join`. Поле `closed` выставляется при первом вызове, поэтому повторный Close возвращает nil и не закрывает чужой reused descriptor. `Path` возвращает сохранённый stable path. Lock не удаляется.

Run: `gofmt -w internal/writer/lock.go internal/writer/lock_test.go && go test ./internal/writer -run='Test(Lock|AcquireLock)' -v && go test -race ./internal/writer`
Expected: PASS, реальные ожидания ограничены десятками миллисекунд.

- [ ] **Step 4: Полные tests и commit**

Run: `go test ./... && go vet ./...`
Expected: PASS.

```bash
git add internal/writer/lock.go internal/writer/lock_test.go
git commit -m "feat: add stable flock target locks"
```

### Task 3: Atomic replace, metadata/xattrs и fault injection

**Files:**
- Create: `internal/writer/metadata.go`
- Create: `internal/writer/atomic.go`
- Create: `internal/writer/atomic_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `CanonicalTargetPath`, `openTargetFile`, `ReadSnapshot`, x/sys dependency.
- Produces: `Replace`, `ReplaceResult`, `ErrPostReplaceVerification`; будущая transaction обязана при `err != nil && result.Replaced` запустить rollback, а при `Replaced=false` считать target неизменённым.

- [ ] **Step 1: Написать failing success и metadata tests**

Success test создаёт target 0640 с bytes `old\x00\n`, добавляет `user.fstab_edit_test` со значением `context-v1`, вызывает `Replace` с candidate `new\xff\n` и проверяет:

- returned canonical Path, `Replaced=true`, SHA-256 candidate;
- target exact bytes и mode 0640;
- UID/GID равны `os.Geteuid` и исходному Stat_t;
- user xattr сохранён через `unix.Getxattr` с точным value;
- temp files `.fstab-edit-*` отсутствуют;
- исходный candidate slice не изменён.

Если temp FS возвращает `ENOTSUP`/`EOPNOTSUPP` при установке user xattr, отдельный xattr test явно пропускается с указанием среды; остальные tests не пропускаются. Отсутствие SELinux enforcing не используется как основание объявить security-context VM test выполненным.

```go
func TestReplacePreservesBytesModeAndUserXattr(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, []byte("old\x00\n"), 0o640); err != nil { t.Fatal(err) }
	if err := unix.Setxattr(path, "user.fstab_edit_test", []byte("context-v1"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) { t.Skip("temp FS без user xattrs") }
		t.Fatal(err)
	}
	candidate := []byte("new\xff\n")
	result, err := Replace(path, candidate)
	if err != nil { t.Fatal(err) }
	if !result.Replaced || result.SHA256 != sha256.Sum256(candidate) { t.Fatalf("result: %+v", result) }
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, candidate) { t.Fatalf("bytes %v, err %v", got, err) }
}
```

Отдельный event-order test оборачивает per-call operations и фиксирует `write → fchown → fchmod → fsetxattr → file sync → file close → rename → directory sync → post read`. Это запрещает rename до durable candidate и metadata.

Run: `go test ./internal/writer -run='TestReplace' -v`
Expected: FAIL, Replace отсутствует.

- [ ] **Step 2: Реализовать descriptor-based metadata copy**

`metadata.go` получает source Stat_t через уже открытый target descriptor. Перед записью temp уже содержит candidate; затем порядок строго:

1. `unix.Fchown(tempFD, int(stat.Uid), int(stat.Gid))`;
2. `unix.Fchmod(tempFD, stat.Mode&0o7777)`;
3. скопировать все перечисленные source xattrs через `Flistxattr/Fgetxattr` в temp через `Fsetxattr`.

Chown выполняется до xattrs, чтобы очистка `security.capability` при смене владельца не потеряла скопированное значение. Значения xattrs не включаются в errors/log messages; имя атрибута и операция допустимы для диагностики. Любые `EACCES`, `EPERM`, `ERANGE` после bounded retries и другие ошибки прерывают replace.

`Flistxattr(fd, nil)` и `Fgetxattr(fd, name, nil)` определяют sizes; при `ERANGE` выполняется не более четырёх повторных size/read попыток на operation, после чего возвращается ошибка. `ENOTSUP`/`EOPNOTSUPP` на пустом list означает отсутствие доступных xattrs; ошибка чтения или установки конкретного существующего атрибута не игнорируется. NUL-separated names разбираются без пустых элементов; zero-length values сохраняются.

Per-call operations struct содержит function fields для `fchown`, `fchmod`, `flistxattr`, `fgetxattr`, `fsetxattr`, temp/open/sync/rename/remove/post-read. Production constructor заполняет все fields прямыми os/unix функциями; `replaceWithOperations` не принимает incomplete config и возвращает ошибку программирования до mutation при nil field.

- [ ] **Step 3: Реализовать atomic replace и точный Replaced contract**

Внутренние interfaces позволяют внедрять ошибки без глобальных переменных:

```go
type atomicFile interface {
	io.Writer
	Sync() error
	Close() error
	Fd() uintptr
	Name() string
}

type syncCloser interface {
	Sync() error
	Close() error
}

type ReplaceResult struct {
	Path     string
	SHA256   [32]byte
	Replaced bool
}
```

`Replace` вызывает `replaceWithOperations` с production operations. Алгоритм:

1. Canonicalize и open source через `openTargetFile`; получить Stat_t через `Fstat` и повторно проверить regular.
2. Открыть parent directory и сохранить directory handle для последующего Sync.
3. `os.CreateTemp(parent, ".fstab-edit-*")`; начальный mode 0600 проверяется через Fstat.
4. Полностью записать candidate через `io.Copy(temp, bytes.NewReader(candidate))`; short write является ошибкой.
5. Применить metadata/xattrs из Step 2, выполнить `temp.Sync`, затем `temp.Close`; любая ошибка запрещает rename.
6. Выполнить `os.Rename(tempPath, canonical)`. Только после успешного rename установить `result.Replaced=true` и отменить remove temp path.
7. Выполнить directory.Sync; при ошибке вернуть result с `Replaced=true`.
8. Повторно вызвать `ReadSnapshot(canonical)` и сравнить с `sha256.Sum256(candidate)`. Read/hash mismatch оборачивает `ErrPostReplaceVerification`, сохраняя `Replaced=true` и фактический error для `errors.Is`.
9. Закрыть source/directory; close errors присоединяются без потери primary error. Успешный result содержит canonical path и expected hash.

Deferred cleanup действует только до успешного rename. Он закрывает temp, если тот ещё открыт, и вызывает remove; `os.ErrNotExist` игнорируется. Cleanup error присоединяется через `errors.Join(primary, cleanup)` и не заменяет primary. При успешном rename temp name уже отсутствует и не удаляется повторно. Writer не восстанавливает старую версию: это задача будущей transaction с backup текущей операции.

- [ ] **Step 4: Добавить fault injection до и после rename**

Table-driven tests используют реальные temp targets и обёртки operations. Для каждого сбоя проверяются target bytes, `Replaced`, вызов rename, cleanup и `errors.Is` первичной причины:

1. CreateTemp error → old target, `Replaced=false`, rename не вызван.
2. Write error `ENOSPC` → old target, temp cleanup, primary ENOSPC сохраняется даже при дополнительной cleanup error.
3. Fchown, Fchmod, Flistxattr, Fgetxattr и Fsetxattr errors → old target, rename не вызван; security context failure не игнорируется.
4. File Sync error и file Close error → old target, rename не вызван.
5. Rename error `EROFS` → old target, `Replaced=false`, temp cleanup.
6. Directory Sync error → target уже candidate, `Replaced=true`, automatic rollback не выполняется низкоуровневым writer.
7. Post-read error и специально подменённый wrong-hash snapshot → `ErrPostReplaceVerification`/`Replaced=true`.
8. Cleanup failure вместе с первичным write failure → обе ошибки доступны через `errors.Is`, исходный target сохранён.
9. Success без xattrs и success с zero-length xattr не создают temp leftovers.
10. Symlink, directory и FIFO target отклоняются до CreateTemp/rename.

Run: `gofmt -w internal/writer && go test ./internal/writer -run='TestReplace' -v && go test -race ./internal/writer`
Expected: PASS; ни один fault test не требует root и не обращается к `/etc/fstab`.

- [ ] **Step 5: Обновить README и выполнить полную приёмку**

В README отметить secure snapshot/base conflict, stable lock directories и defaults, atomic order и metadata preservation. Явно указать:

- lock создаётся только явным `AcquireLock` и не удерживается библиотекой во время prompt;
- `ReplaceResult.Replaced=true` при post-rename ошибке требует rollback будущей transaction;
- backup, sidecar, automatic rollback, transaction и CLI ещё не подключены;
- SELinux/AppArmor policy не изменяется; enforcing VM test остаётся отдельной release-проверкой.

```bash
gofmt -w internal/writer
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
go test ./... -cover
```

Expected: все PASS, lint 0 issues, writer coverage не ниже 80%; зафиксировать фактический процент. Проверить `git status --short`, отсутствие temp files и неизменность candidate inputs.

```bash
git add internal/writer go.mod go.sum README.md
git commit -m "feat: add atomic fstab replacement"
```

## Финальная приёмка

1. Snapshot читает exact bytes из regular descriptor и base hash обнаруживает внешнее изменение даже при неизменных size/mtime.
2. Lock path стабилен по SHA-256 canonical target; lock directory/file owner и modes проверены; timeout/cancel/release проходят на реальных temp flock.
3. Ни один read-only API не создаёт lock; target никогда не используется как lock-файл.
4. До rename target остаётся старым при create/write/metadata/fsync/rename failures; cleanup не скрывает primary error.
5. UID/GID/mode и доступные xattrs копируются через descriptors; ошибка security xattr запрещает replace.
6. После rename directory sync и expected SHA-256 проверяются; любая ошибка явно возвращает `Replaced=true` без ложного заявления об rollback.
7. `go test ./...`, race, vet, lint и coverage проходят; отдельный task review после каждой задачи и whole-branch review не содержат открытых замечаний.
8. После clean review выполняются fast-forward merge в `main`, post-merge `go test ./...`, удаление worktree/branch, push SSH `origin/main` и проверка clean synchronized status.
