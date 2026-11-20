# Config / Logging / i18n Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Реализовать блок ТЗ «Конфигурация» (§13), «Журналирование» (§14) и «Локализация» (§15): YAML-config с приоритетами и security-проверками, JSON Lines logger с redaction и rotation, полные каталоги RU/EN для help/errors/prompts.

**Architecture:** Три новых пакета `internal/config`, `internal/logger`, `internal/i18n` с чистыми injectable-границами (нет чтения реального `/etc/fstab`, `/etc/fstab-edit/config.yaml` в тестах — всё через `t.TempDir` и подменяемые seams). `cmd.App` резолвит язык до построения help-текстов, грузит config в `PersistentPreRunE`, открывает logger до mutation и пишет события вокруг transaction/rollback. `cliapp.ExitError` обрастает `MessageKey/MessageArgs` — сообщения переводятся в точке печати, русский текст остаётся fallback.

**Tech Stack:** Go 1.22 (toolchain 1.27), Cobra v1.8.1, `gopkg.in/yaml.v3` (новая зависимость, только для config), stdlib только.

## Global Constraints

- Все сообщения, комментарии и документация — на русском; исходники максимально прокомментированы на русском.
- Реальный `/etc/fstab`, `/etc/fstab-edit/config.yaml`, `/var/log/fstab-edit.log` в тестах не читаются и не пишутся: только `t.TempDir`.
- Go module `fstab-edit`, `go 1.22.0`; новая зависимость только `gopkg.in/yaml.v3` (последняя v3-версия).
- ТЗ §13.1 приоритеты (дословно): CLI flags > `FSTAB_EDIT_*` > User YAML > System YAML > Defaults; YAML объединяются по ключам.
- ТЗ §13.2: root home по effective UID; root не грузит user config непривилегированного пользователя; symlink config отклоняется; root отклоняет user config не root-owned или group/others-writable; system config root-owned и не group/others-writable; `--config` не отключает checks; небезопасный config = явная ошибка.
- ТЗ §14: JSON Lines, ISO 8601, поля severity/real+effective UID/проверенные sudo-сведения/operation/target/backup ID/result/error code/duration; old/new values с escaped newlines и redaction; passwords/tokens/secrets/URL-credentials маскируются; rotation 10 МиБ × 5; read-only и dry-run не создают файловый журнал; log недоступен до точки изменения → target не меняется; log failed после replace не мешает rollback; первичная ошибка не заменяется logging error.
- ТЗ §15: RU и EN; приоритет `--lang` > `LC_ALL`/`LANG` > English fallback (принято уточнение: explicit `--lang` и `FSTAB_EDIT_LANG` выше локали; fallback — English; default без всего — `ru`, текущее поведение); help/errors/warnings/prompts переводятся; exit codes/JSON keys/enum/log codes не переводятся; отсутствующий перевод → English, не panic; подтверждение принимает localized yes/no и по умолчанию No.
- Exit codes не меняются, кроме: код **3 активируется** для ошибок доступа к config/log (security checks, permission denied при открытии журнала). README и ТЗ-сводку обновить.
- Каждая задача заканчивается `go test ./...`, `go vet ./...`, `golangci-lint run` — чисто; коммит после каждой задачи.
- Известное ограничение блока (фиксируется в README): причины ошибок нижележащих слоёв (`ExitError.Cause`) и сообщения Cobra об unknown flags не переводятся — это внутренняя диагностика.

## File Structure

- Create: `internal/config/config.go` — схема, defaults, merge, security checks, env.
- Create: `internal/config/config_test.go`.
- Create: `internal/logger/logger.go` — JSON Lines logger, rotation, Event.
- Create: `internal/logger/redact.go` — escaping + redaction правил.
- Create: `internal/logger/logger_test.go`, `internal/logger/redact_test.go`.
- Create: `internal/i18n/i18n.go` — Resolve/T/Tf/AcceptYes/IsValidLanguage.
- Create: `internal/i18n/catalog_ru.go`, `internal/i18n/catalog_en.go`.
- Create: `internal/i18n/i18n_test.go`.
- Modify: `internal/cliapp/types.go` — `ExitError.MessageKey/MessageArgs`, `*ErrorK` helpers, `ExitPermission = 3`.
- Modify: `internal/cliapp/{service,rollback,select,mutation}.go` — ключи вместо inline-форматов.
- Modify: `cmd/app.go`, `cmd/mutation.go`, `cmd/rollback.go`, `cmd/backup.go`, `cmd/list.go`, `cmd/validate.go` — i18n + config/logger wiring.
- Modify: `main.go` — проброс новых production-зависимостей.
- Modify: `README.md` — удалить «ограничения блока», описать config/logger/i18n, exit code 3, env vars.
- Modify: `go.mod`/`go.sum` — `gopkg.in/yaml.v3`.

## Порядок задач

Task 1 (config) → Task 2 (logger) → Task 3 (i18n core + ExitError keys) → Task 4 (cliapp ключи) → Task 5 (cmd i18n: help/prompts/confirm) → Task 6 (wiring config+logger, README). Задачи 1 и 2 независимы по коду, но идут последовательно для простоты review.

---

### Task 1: internal/config — YAML, env, приоритеты, security checks

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `internal/backup.DefaultBackupPrefix`, `internal/backup.DefaultKeepBackups`.
- Produces (используется в Task 6):

```go
package config

// Settings — полностью резолвленные настройки одного запуска.
type Settings struct {
    FstabPath    string // default "/etc/fstab"
    BackupDir    string // пусто = каталог target
    BackupPrefix string
    BackupKeep   int
    LogFile      string // default "/var/log/fstab-edit.log"
    LogLevel     string // debug|info|warn|error
    LogMaxSizeMB int
    LogKeep      int
    TUITheme     string // сохраняется, на CLI не влияет
    Lang         string // только env/CLI: ru|en
    Output       string // только env/CLI: table|json|csv|tsv
}

// LoadOptions — injectable-границы. Production: ExplicitPath = флаг --config,
// Environ = os.Environ(), EUID = os.Geteuid(), LookupUser = LookupUserByUID.
type LoadOptions struct {
    ExplicitPath string
    Environ      []string
    EUID         int
    LookupUser   func(uid int) (username, home string, err error)
}

func Default() Settings
func Load(opts LoadOptions) (Settings, []string, error) // (settings, warnings, err)
func LookupUserByUID(uid int) (username, home string, err error) // os/user.LookupId
```

- [ ] **Step 1: Подключить yaml.v3**

```bash
cd /home/sergey/projects/fstab-editor/work
go get gopkg.in/yaml.v3@latest
```

Expected: `go.mod` содержит `require gopkg.in/yaml.v3 v3.x.y`; `go test ./...` PASS.

- [ ] **Step 2: Написать failing tests**

`internal/config/config_test.go` (полный набор; каждый тест — table или отдельный case, все пути в `t.TempDir()`):

```go
package config

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

// testLookupUser подменяет LookupUser: uid 0 → root с home из tempdir,
// иначе — пользователь с home из tempdir.
func testLookupUser(home string) func(int) (string, string, error) {
    return func(uid int) (string, string, error) {
        if uid == 0 {
            return "root", home, nil
        }
        return "user", home, nil
    }
}

// writeConfig пишет YAML в tempdir и возвращает путь.
func writeConfig(t *testing.T, dir, name, content string) string {
    t.Helper()
    path := filepath.Join(dir, name)
    if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
        t.Fatal(err)
    }
    return path
}

func TestLoadDefaults(t *testing.T) {
    s, warnings, err := Load(LoadOptions{Environ: nil, EUID: 1000, LookupUser: testLookupUser(t.TempDir())})
    if err != nil { t.Fatal(err) }
    if len(warnings) != 0 { t.Fatalf("warnings: %v", warnings) }
    if s.FstabPath != "/etc/fstab" || s.LogFile != "/var/log/fstab-edit.log" ||
        s.LogLevel != "info" || s.LogMaxSizeMB != 10 || s.LogKeep != 5 ||
        s.BackupKeep != 10 || s.Lang != "ru" || s.Output != "" {
        t.Fatalf("defaults: %+v", s)
    }
}

func TestLoadPriorityCLIEnvUserSystemDefaults(t *testing.T) {
    // system config: fstab_path=/from-system, log.level=warn
    // user config: fstab_path=/from-user, backup.keep=3
    // env FSTAB_EDIT_FILE=/from-env
    // ожидание: FstabPath=/from-env (env выше user/system),
    // LogLevel=warn (system доезжает), BackupKeep=3 (user доезжает).
}

func TestLoadExplicitPath(t *testing.T) {
    // --config указывает на файл вне стандартных путей; загружается он,
    // system/user не читаются. Непустой ключ из explicit перекрывается env.
}

func TestLoadUnknownEnvWarns(t *testing.T) {
    // Environ: FSTAB_EDIT_FOO=bar → ровно одна warning, Settings не меняется.
}

func TestLoadUnknownYAMLKeyErrors(t *testing.T) {
    // explicit config с ключом `nonsense: 1` → err != nil.
}

func TestLoadInvalidValuesError(t *testing.T) {
    // log.level: trace / lang: de (env) / output: xml (env) → err != nil в каждом случае.
}

func TestLoadRejectsSymlink(t *testing.T) {
    // explicit config — symlink на реальный файл → err != nil.
}

func TestLoadRootSkipsUserConfig(t *testing.T) {
    // EUID=0, user config задан и валиден → Settings не содержит его значений,
    // ошибки нет.
}

func TestLoadUserConfigOwnershipAndMode(t *testing.T) {
    // EUID=1000, user config group-writable (0o640) → err != nil.
    // owner != euid (chown недоступен без root — см. TestLoadSecurityWithChown
    // ниже, пропускается без прав) — проверка owner реализуется через
    // искусственный uid файла: тест пишет файл и требует EUID равный
    // stat-uid; для owner-mismatch используем файл, созданный с EUID=1000,
    // а Load вызываем с EUID=1001 — ожидаем err.
}

func TestLoadSystemConfigOwnership(t *testing.T) {
    // system path подменяется через относительный путь невозможен —
    // system path фиксирован /etc/fstab-edit/config.yaml. Для теста
    // используем build-tag-free подход: переменная пакетного уровня
    // systemConfigPath (unexported var) — тест присваивает tempdir-путь
    // и восстанавливает через t.Cleanup. System config group-writable → err.
}

func TestLookupHomeByEffectiveUID(t *testing.T) {
    // HOME env указывает в другое место, LookupUser возвращает home из
    // /etc/passwd-логики → user config берётся именно из home LookupUser.
}
```

- [ ] **Step 3: Run tests to verify failure**

Run: `go test ./internal/config/ -v`
Expected: FAIL (package files don't compile / functions undefined).

- [ ] **Step 4: Реализация `internal/config/config.go`**

Полная реализация по следующей спецификации (все функции и типы с русскими doc-комментариями):

```go
// Package config загружает и резолвит настройки fstab-edit по приоритетам
// ТЗ §13.1: CLI > FSTAB_EDIT_* > User YAML > System YAML > Defaults.
// Пакет не читает реальные системные пути в тестах: пути и окружение
// передаются через LoadOptions, проверки собственности/прав — через
// переданный EUID. Конфигурация небезопасна — Load возвращает явную ошибку.
package config

import (
    "errors"
    "fmt"
    "os"
    "os/user"
    "path/filepath"
    "strconv"
    "strings"

    "gopkg.in/yaml.v3"

    "fstab-edit/internal/backup"
)

// systemConfigPath — путь system config. package-level var только для
// тестовой подмены на tempdir; production код не меняет его.
var systemConfigPath = "/etc/fstab-edit/config.yaml"

// userConfigRelPath — путь user config относительно home effective UID.
const userConfigRelPath = ".config/fstab-edit/config.yaml"

// Settings — полностью резолвленные настройки одного запуска CLI.
// Lang и Output не читаются из YAML (ТЗ §13.3), только из env/CLI.
type Settings struct {
    FstabPath    string
    BackupDir    string
    BackupPrefix string
    BackupKeep   int
    LogFile      string
    LogLevel     string
    LogMaxSizeMB int
    LogKeep      int
    TUITheme     string
    Lang         string
    Output       string
}

// fileConfig — зеркало YAML-схемы ТЗ §13.3; все поля указатели/omitempty-
// стиль, чтобы отсутствующий ключ не затирал значение низшего уровня.
type fileConfig struct {
    FstabPath *string `yaml:"fstab_path"`
    Backup    *struct {
        Dir    *string `yaml:"dir"`
        Prefix *string `yaml:"prefix"`
        Keep   *int    `yaml:"keep"`
    } `yaml:"backup"`
    Log *struct {
        File       *string `yaml:"file"`
        Level      *string `yaml:"level"`
        MaxSizeMB  *int    `yaml:"max_size_mb"`
        Keep       *int    `yaml:"keep"`
    } `yaml:"log"`
    TUI *struct {
        Theme *string `yaml:"theme"`
    } `yaml:"tui"`
}

// Default возвращает настройки по умолчанию (нижний уровень приоритета).
func Default() Settings {
    return Settings{
        FstabPath:    "/etc/fstab",
        BackupDir:    "",
        BackupPrefix: backup.DefaultBackupPrefix,
        BackupKeep:   backup.DefaultKeepBackups,
        LogFile:      "/var/log/fstab-edit.log",
        LogLevel:     "info",
        LogMaxSizeMB: 10,
        LogKeep:      5,
        TUITheme:     "dark",
        Lang:         "ru",
        Output:       "",
    }
}
```

Далее в том же файле:

```go
// LoadError классифицирует сбои загрузки конфигурации для маппинга на exit
// codes: KindSecurity → код 3, KindInvalid → код 2.
type LoadError struct {
    Kind string // "security" | "invalid" | "io"
    Path string
    Err  error
}

func (e *LoadError) Error() string { return fmt.Sprintf("config %s: %v", e.Path, e.Err) }
func (e *LoadError) Unwrap() error { return e.Err }

// Load резолвит Settings. Порядок: defaults → system YAML (euid != 0) →
// user YAML (euid != 0) → env → explicit path вместо system/user, но ниже env.
// warnings — непустые предупреждения (неизвестные FSTAB_EDIT_*), err — первый
// фатальный сбой.
func Load(opts LoadOptions) (Settings, []string, error) {
    s := Default()
    warnings := make([]string, 0)

    explicit := opts.ExplicitPath != ""
    if explicit {
        if err := loadFileInto(&s, opts.ExplicitPath, opts.EUID, true); err != nil {
            return s, warnings, err
        }
    } else if opts.EUID != 0 {
        // Root-процесс не загружает user config автоматически (ТЗ §13.2).
        if err := loadFileInto(&s, systemConfigPath, opts.EUID, true); err != nil {
            return s, warnings, err
        }
        home := ""
        if opts.LookupUser != nil {
            _, h, err := opts.LookupUser(opts.EUID)
            if err != nil {
                return s, warnings, &LoadError{Kind: "io", Path: userConfigRelPath,
                    Err: fmt.Errorf("определить home по effective UID %d: %w", opts.EUID, err)}
            }
            home = h
        }
        if home != "" {
            if err := loadFileInto(&s, filepath.Join(home, userConfigRelPath), opts.EUID, false); err != nil {
                return s, warnings, err
            }
        }
    }

    if err := applyEnv(&s, opts.Environ, &warnings); err != nil {
        return s, warnings, err
    }
    if err := validateValues(&s); err != nil {
        return s, warnings, err
    }
    return s, warnings, nil
}
```

```go
// loadFileInto читает один YAML-файл при его существовании, проверяет
// безопасность пути и вливает заданные ключи в Settings. system=true
// дополнительно требует root-ownership (проверяется для любого euid).
func loadFileInto(s *Settings, path string, euid int, system bool) error {
    info, err := os.Lstat(path)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil // отсутствующий config — не ошибка
        }
        return &LoadError{Kind: "io", Path: path, Err: err}
    }
    if info.Mode()&os.ModeSymlink != 0 {
        return &LoadError{Kind: "security", Path: path,
            Err: errors.New("symlink-конфигурация отклонена")}
    }
    if err := checkOwnership(path, info, euid, system); err != nil {
        return err
    }
    data, err := os.ReadFile(path)
    if err != nil {
        return &LoadError{Kind: "io", Path: path, Err: err}
    }
    var fc fileConfig
    dec := yaml.NewDecoder(strings.NewReader(string(data)))
    dec.KnownFields(true) // неизвестный ключ — явная ошибка
    if err := dec.Decode(&fc); err != nil {
        return &LoadError{Kind: "invalid", Path: path, Err: err}
    }
    mergeFileConfig(s, &fc)
    return nil
}
```

```go
// checkOwnership проверяет владельца и права config-файла (ТЗ §13.2).
// Для euid==0: файл должен принадлежать root и не быть group/others-writable.
// Для euid!=0 (user/explicit config): файл должен принадлежать пользователю
// и не быть group/others-writable. System config всегда: root-owned.
func checkOwnership(path string, info os.FileInfo, euid int, system bool) error {
    stat, ok := info.Sys().(*syscall.Stat_t)
    if !ok {
        return &LoadError{Kind: "io", Path: path, Err: errors.New("не удалось получить stat")}
    }
    mode := info.Mode().Perm()
    if mode&0o022 != 0 { // group или others writable
        return &LoadError{Kind: "security", Path: path,
            Err: fmt.Errorf("config доступен на запись group/others (mode %o)", mode)}
    }
    wantUID := uint32(euid)
    if system {
        wantUID = 0
    }
    if stat.Uid != wantUID {
        return &LoadError{Kind: "security", Path: path,
            Err: fmt.Errorf("config принадлежит uid %d, ожидался uid %d", stat.Uid, wantUID)}
    }
    return nil
}
```

(Добавить импорт `syscall`.)

```go
// mergeFileConfig вливает только заданные (non-nil) ключи fileConfig в
// Settings — отсутствующий ключ не затирает значение низшего уровня.
func mergeFileConfig(s *Settings, fc *fileConfig) {
    if fc.FstabPath != nil { s.FstabPath = *fc.FstabPath }
    if fc.Backup != nil {
        if fc.Backup.Dir != nil { s.BackupDir = *fc.Backup.Dir }
        if fc.Backup.Prefix != nil { s.BackupPrefix = *fc.Backup.Prefix }
        if fc.Backup.Keep != nil { s.BackupKeep = *fc.Backup.Keep }
    }
    if fc.Log != nil {
        if fc.Log.File != nil { s.LogFile = *fc.Log.File }
        if fc.Log.Level != nil { s.LogLevel = *fc.Log.Level }
        if fc.Log.MaxSizeMB != nil { s.LogMaxSizeMB = *fc.Log.MaxSizeMB }
        if fc.Log.Keep != nil { s.LogKeep = *fc.Log.Keep }
    }
    if fc.TUI != nil && fc.TUI.Theme != nil { s.TUITheme = *fc.TUI.Theme }
}

// knownEnv — разрешённые FSTAB_EDIT_* и их маппинг на Settings.
var knownEnv = map[string]func(*Settings, string){
    "FSTAB_EDIT_FILE":       func(s *Settings, v string) { s.FstabPath = v },
    "FSTAB_EDIT_BACKUP_DIR": func(s *Settings, v string) { s.BackupDir = v },
    "FSTAB_EDIT_LOG_LEVEL":  func(s *Settings, v string) { s.LogLevel = v },
    "FSTAB_EDIT_LANG":       func(s *Settings, v string) { s.Lang = v },
    "FSTAB_EDIT_OUTPUT":     func(s *Settings, v string) { s.Output = v },
}

// applyEnv применяет известные FSTAB_EDIT_* поверх Settings; неизвестные
// добавляют warning в список и не меняют поведение (ТЗ §13.4).
func applyEnv(s *Settings, environ []string, warnings *[]string) error {
    for _, pair := range environ {
        name, value, ok := strings.Cut(pair, "=")
        if !ok || !strings.HasPrefix(name, "FSTAB_EDIT_") { continue }
        apply, known := knownEnv[name]
        if !known {
            *warnings = append(*warnings,
                fmt.Sprintf("неизвестная переменная окружения %s проигнорирована", name))
            continue
        }
        apply(s, value)
    }
    return nil
}

// validateValues отклоняет некорректные резолвленные значения явной ошибкой.
func validateValues(s *Settings) error {
    switch s.LogLevel { case "debug", "info", "warn", "error":
    default: return fmt.Errorf("некорректный log.level %q: допустимы debug, info, warn, error", s.LogLevel) }
    switch s.Lang { case "ru", "en":
    default: return fmt.Errorf("некорректный lang %q: допустимы ru, en", s.Lang) }
    if s.Output != "" {
        switch s.Output { case "table", "json", "csv", "tsv":
        default: return fmt.Errorf("некорректный output %q: допустимы table, json, csv, tsv", s.Output) }
    }
    if s.LogMaxSizeMB <= 0 || s.LogKeep <= 0 || s.BackupKeep <= 0 {
        return errors.New("keep/max_size_mb должны быть положительными")
    }
    return nil
}

// LookupUserByUID определяет имя и home пользователя по UID через системную
// базу пользователей (effective UID, не непроверенный HOME — ТЗ §13.2).
func LookupUserByUID(uid int) (string, string, error) {
    u, err := user.LookupId(strconv.Itoa(uid))
    if err != nil { return "", "", err }
    return u.Username, u.HomeDir, nil
}
```

- [ ] **Step 5: Run tests, vet, lint**

```bash
go test ./internal/config/ -v
go test ./... && go vet ./... && golangci-lint run
```

Expected: все тесты PASS, lint 0 issues.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config/
git commit -m "feat: add config loader with priority and security checks"
```

---

### Task 2: internal/logger — JSON Lines, redaction, rotation

**Files:**
- Create: `internal/logger/logger.go`
- Create: `internal/logger/redact.go`
- Create: `internal/logger/logger_test.go`
- Create: `internal/logger/redact_test.go`

**Interfaces:**
- Consumes: ничего из ранних задач.
- Produces (Task 6):

```go
package logger

type Severity string // "debug" | "info" | "warn" | "error" — не локализуются
type Operation string // "add"|"edit"|"delete"|"comment"|"uncomment"|"backup"|"rollback"

type Event struct {
    Severity     Severity
    Timestamp    time.Time
    RealUID      int
    EffectiveUID int
    SudoUser     string `json:",omitempty"`
    SudoUID      string `json:",omitempty"`
    Operation    Operation
    Target       string
    BackupID     string `json:",omitempty"`
    Result       string // "success" | "error" | "cancelled"
    ErrorCode    int    `json:",omitempty"`
    DurationMs   int64  `json:",omitempty"`
    OldValue     string `json:",omitempty"`
    NewValue     string `json:",omitempty"`
}

type Options struct {
    File         string
    Level        Severity
    MaxSizeMB    int
    Keep         int
    RealUID      int
    EffectiveUID int
    SudoUser     string
    SudoUID      string
    Now          func() time.Time // nil → time.Now
}

type Logger struct{ /* unexported */ }

func Open(opts Options) (*Logger, error)
func (l *Logger) Log(e Event) error // записывает одну строку JSON Lines
func (l *Logger) Close() error

// Redact маскирует секреты и экранирует управляющие символы в строке,
// предназначенной для журнала (ТЗ §14).
func Redact(s string) string
```

- [ ] **Step 1: Написать failing tests**

`internal/logger/redact_test.go` (table-driven):

```go
package logger

import "testing"

func TestRedact(t *testing.T) {
    cases := []struct{ name, in, want string }{
        {"newline escaped", "a\nb", `a\nb`},
        {"carriage return escaped", "a\rb", `a\rb`},
        {"tab escaped", "a\tb", `a\tb`},
        {"backslash doubled", `a\b`, `a\\b`},
        {"url credentials", "https://user:pass@host/path", "https://user:***@host/path"},
        {"url credentials empty user", "https://:pass@host", "https://:***@host"},
        {"password option", "credentials=/secret/fstab.cred", "credentials=***"},
        {"password inline option", "rw,password=abc123,x", "rw,password=***,x"},
        {"token query", "url=https://h/?token=abc", "url=https://h/?token=***"},
        {"username option masked", "username=alice,password=x", "username=***,password=***"},
        {"ordinary text untouched", "UUID=abc / ext4 rw 0 1", "UUID=abc / ext4 rw 0 1"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := Redact(tc.in); got != tc.want {
                t.Fatalf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
            }
        })
    }
}
```

`internal/logger/logger_test.go` (все файлы в `t.TempDir()`):

```go
package logger

import (
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func testOptions(t *testing.T, file string) Options {
    t.Helper()
    return Options{
        File: file, Level: "info", MaxSizeMB: 10, Keep: 5,
        RealUID: 1000, EffectiveUID: 1000,
        Now: func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) },
    }
}

func readEvents(t *testing.T, path string) []map[string]any {
    t.Helper()
    data, err := os.ReadFile(path)
    if err != nil { t.Fatal(err) }
    lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
    events := make([]map[string]any, 0, len(lines))
    for _, line := range lines {
        var e map[string]any
        if err := json.Unmarshal([]byte(line), &e); err != nil {
            t.Fatalf("строка %q не JSON: %v", line, err)
        }
        events = append(events, e)
    }
    return events
}

func TestOpenWritesJSONLinesEvent(t *testing.T) {
    // Open → Log(info, operation=add, target, backup_id, result=success,
    // old/new с \n) → Close. Файл содержит одну строку; поля severity,
    // operation, target, result, backup_id, real_uid, effective_uid,
    // timestamp (RFC3339) присутствуют; old_value содержит экранированный
    // перенос и не содержит сырого \n внутри строки файла.
}

func TestLevelFilters(t *testing.T) {
    // Level=warn: Log(debug) и Log(info) не пишутся, Log(warn) и Log(error)
    // пишутся — в файле ровно две строки.
}

func TestRedactionAppliedToValues(t *testing.T) {
    // NewValue = "password=abc\nrw" → в JSON old/new значение содержит
    // "***" и "\n" как два символа, не разрывая строку.
}

func TestRotation(t *testing.T) {
    // MaxSizeMB=1, Keep=2. Предзаписать файл размером 1 МиБ + 1 байт через
    // os.WriteFile. Open → Log → Close. Ожидание: существуют file.1
    // (старые данные), текущий file содержит только новое событие,
    // file.2 отсутствует.
}

func TestOpenPermissionDenied(t *testing.T) {
    // File в несуществующем каталоге без прав (t.TempDir удалён через
    // os.RemoveAll) → err != nil.
}

func TestCloseIdempotent(t *testing.T) {
    // Двойной Close не паникует и не ошибка.
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/logger/ -v` → FAIL (undefined symbols).

- [ ] **Step 3: Реализация `internal/logger/redact.go`**

```go
package logger

import (
    "regexp"
    "strings"
)

// credentialPattern маскирует value чувствительных ключей вида key=value,
// где разделителем пары служит '=', а пары перечислены через запятую либо
// '&' (query string). Ключи фиксированы и не зависят от пользовательского
// ввода — заменяется только value.
var credentialKeys = regexp.MustCompile(
    `(?i)\b(credentials|password|passwd|token|secret|username|user|pass)=([^,&\s]+)`)

// urlUserinfoPattern маскирует пароль в user:pass@ — пароль заменяется
// независимо от содержимого, user сохраняется для диагностики.
var urlUserinfoPattern = regexp.MustCompile(`(://[^:/\s]*):([^@\s]+)@`)

// Redact готовит строку для журнала: экранирует управляющие символы,
// чтобы одно событие оставалось одной строкой, и маскирует секреты
// (пароли, токены, credentials, userinfo URL). Порядок: сначала маскировка
// по исходным границам, затем escaping — маскировка не должна ломаться
// экранированием обратных слешей.
func Redact(s string) string {
    s = urlUserinfoPattern.ReplaceAllString(s, "$1:***@")
    s = credentialKeys.ReplaceAllString(s, "$1=***")
    var b strings.Builder
    b.Grow(len(s))
    for _, r := range s {
        switch r {
        case '\\': b.WriteString(`\\`)
        case '\n': b.WriteString(`\n`)
        case '\r': b.WriteString(`\r`)
        case '\t': b.WriteString(`\t`)
        default: b.WriteRune(r)
        }
    }
    return b.String()
}
```

- [ ] **Step 4: Реализация `internal/logger/logger.go`**

Спецификация (полная реализация с русскими комментариями):

```go
// Package logger пишет события fstab-edit в JSON Lines-журнал (ТЗ §14):
// одно событие — одна строка, timestamp ISO 8601 (RFC 3339), коды событий и
// operations не локализуются. Перед mutation CLI обязан открыть журнал:
// недоступность журнала до точки изменения блокирует мутацию. Сбой записи
// после replace не должен мешать обязательному rollback — поэтому Log
// возвращает ошибку, которую вызывающий код вправе проигнорировать после
// успешной записи target.
package logger

import (
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "sync"
    "time"
)

// Severity — уровень события; значения фиксированы и не локализуются.
type Severity string

const (
    SeverityDebug Severity = "debug"
    SeverityInfo  Severity = "info"
    SeverityWarn  Severity = "warn"
    SeverityError Severity = "error"
)

// Operation — стабильный код операции для журнала и backup metadata;
// не локализуется.
type Operation string

const (
    OperationAdd       Operation = "add"
    OperationEdit      Operation = "edit"
    OperationDelete    Operation = "delete"
    OperationComment   Operation = "comment"
    OperationUncomment Operation = "uncomment"
    OperationBackup    Operation = "backup"
    OperationRollback  Operation = "rollback"
)

// Result — исход операции: success | error | cancelled.
const (
    ResultSuccess   = "success"
    ResultError     = "error"
    ResultCancelled = "cancelled"
)

// Event — одно событие журнала. OldValue/NewValue уже прошли Redact:
// newlines экранированы, секреты замаскированы. JSON-теги фиксированы
// schema журнала и не локализуются.
type Event struct {
    Severity     Severity  `json:"severity"`
    Timestamp    time.Time `json:"timestamp"`
    RealUID      int       `json:"real_uid"`
    EffectiveUID int       `json:"effective_uid"`
    SudoUser     string    `json:"sudo_user,omitempty"`
    SudoUID      string    `json:"sudo_uid,omitempty"`
    Operation    Operation `json:"operation"`
    Target       string    `json:"target"`
    BackupID     string    `json:"backup_id,omitempty"`
    Result       string    `json:"result"`
    ErrorCode    int       `json:"error_code,omitempty"`
    DurationMs   int64     `json:"duration_ms,omitempty"`
    OldValue     string    `json:"old_value,omitempty"`
    NewValue     string    `json:"new_value,omitempty"`
}

// Options — параметры Open; Now подменяется тестами.
type Options struct {
    File         string
    Level        Severity
    MaxSizeMB    int
    Keep         int
    RealUID      int
    EffectiveUID int
    SudoUser     string
    SudoUID      string
    Now          func() time.Time
}

// Logger — append-only журнал с rotation по размеру. Методы безопасны для
// одного goroutine-писателя; CLI пишет последовательно из одной команды.
type Logger struct {
    file   *os.File
    level  Severity
    max    int64
    keep   int
    now    func() time.Time
    closed bool
    mu     sync.Mutex
}

// Open открывает (создаёт) журнал с правами 0600 и выполняет rotation,
// если текущий размер превышает MaxSizeMB. Ошибка открытия возвращается
// вызывающему: недоступный журнал до mutation блокирует изменение target.
func Open(opts Options) (*Logger, error) {
    if opts.File == "" {
        return nil, errors.New("путь журнала пуст")
    }
    if opts.Now == nil { opts.Now = time.Now }
    max := int64(opts.MaxSizeMB) * 1024 * 1024
    if err := rotateIfNeeded(opts.File, max, opts.Keep); err != nil {
        return nil, err
    }
    f, err := os.OpenFile(opts.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
    if err != nil {
        return nil, fmt.Errorf("открыть журнал %s: %w", opts.File, err)
    }
    return &Logger{
        file: f, level: opts.Level, max: max, keep: opts.Keep, now: opts.Now,
    }, nil
}

// Log сериализует событие одной строкой JSON и дописывает в файл. События
// ниже уровня журнала отбрасываются без ошибки. Ошибка записи возвращается
// вызывающему — после успешного replace он вправе её проигнорировать,
// но до точки изменения она фатальна (журнал недоступен → target не менять).
func (l *Logger) Log(e Event) error {
    l.mu.Lock()
    defer l.mu.Unlock()
    if l.closed { return errors.New("журнал закрыт") }
    if !l.enabled(e.Severity) { return nil }
    e.Timestamp = l.now().UTC()
    data, err := json.Marshal(e)
    if err != nil { return fmt.Errorf("сериализовать событие: %w", err) }
    if _, err := l.file.Write(append(data, '\n')); err != nil {
        return fmt.Errorf("записать событие: %w", err)
    }
    return nil
}

// Close закрывает файл; повторный вызов безопасен (ТЗ: штатное завершение
// освобождает ресурсы).
func (l *Logger) Close() error {
    l.mu.Lock()
    defer l.mu.Unlock()
    if l.closed { return nil }
    l.closed = true
    return l.file.Close()
}

// enabled сообщает, проходит ли severity установленный уровень журнала.
func (l *Logger) enabled(s Severity) bool {
    order := map[Severity]int{SeverityDebug: 0, SeverityInfo: 1, SeverityWarn: 2, SeverityError: 3}
    return order[s] >= order[l.level]
}

// rotateIfNeeded сдвигает архивы file.N → file.N+1 (старшие удаляются сверх
// Keep) и переименовывает текущий файл в file.1, если он достиг max байт.
// Rotation не выполняется, когда max <= 0 или файла нет.
func rotateIfNeeded(path string, max int64, keep int) error {
    if max <= 0 || keep <= 0 { return nil }
    info, err := os.Stat(path)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) { return nil }
        return fmt.Errorf("проверить журнал %s: %w", path, err)
    }
    if info.Size() < max { return nil }
    // Удаляем самый старый архив, освобождая номер keep.
    oldest := path + "." + fmt.Sprint(keep)
    if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
        return fmt.Errorf("удалить архив %s: %w", oldest, err)
    }
    for i := keep - 1; i >= 1; i-- {
        from := path + "." + fmt.Sprint(i)
        to := path + "." + fmt.Sprint(i+1)
        if _, err := os.Stat(from); err == nil {
            if err := os.Rename(from, to); err != nil {
                return fmt.Errorf("rotate %s → %s: %w", from, to, err)
            }
        }
    }
    if err := os.Rename(path, path+".1"); err != nil {
        return fmt.Errorf("rotate %s → %s: %w", path, path+".1", err)
    }
    return nil
}
```

(Импорт `path/filepath` не нужен — удалить при реализации, lint не должен жаловаться на неиспользуемый импорт.)

- [ ] **Step 5: Run tests, vet, lint**

```bash
go test ./internal/logger/ -v
go test ./... && go vet ./... && golangci-lint run
```

Expected: PASS, 0 issues.

- [ ] **Step 6: Commit**

```bash
git add internal/logger/
git commit -m "feat: add json lines logger with redaction and rotation"
```

---

### Task 3: internal/i18n core + ключи в ExitError + активация exit code 3

**Files:**
- Create: `internal/i18n/i18n.go`
- Create: `internal/i18n/catalog_ru.go`
- Create: `internal/i18n/catalog_en.go`
- Create: `internal/i18n/i18n_test.go`
- Modify: `internal/cliapp/types.go`

**Interfaces:**
- Consumes: ничего.
- Produces:

```go
package i18n

func IsValidLanguage(lang string) bool                       // "ru" | "en"
func Resolve(explicit string, environ []string, lcAll, langEnv string) string
// Приоритет: explicit (--lang) > FSTAB_EDIT_LANG > LC_ALL > LANG > "ru".
// Невалидные значения на каждом уровне игнорируются (переход на следующий).
func T(lang, key string) string                             // перевод; fallback en → key
func Tf(lang, key string, args ...any) string               // fmt.Sprintf поверх T
func AcceptYes(lang, answer string) bool                    // ru: y/yes/д/да; en: y/yes; иначе false
func Languages() []string                                   // ["en", "ru"] — sorted, для help
```

- `cliapp.ExitError` добавляет поля `MessageKey string` и `MessageArgs []any`; `Error()` без изменений (русский fallback для логов и совместимости тестов). Новые конструкторы: `NewExitErrorK(code, cause, candidates, key string, args ...any)`, `UsageErrorK`, `ValidationErrorK`, `AmbiguityErrorK`, `PermissionErrorK` (signatures — аналоги существующих с key первым после cause). Существующие конструкторы остаются и работают как раньше (MessageKey == "").
- `cliapp`: `ExitPermission = 3` (новая константа), `PermissionError` и `PermissionErrorK` возвращают code 3 вместо 4. README задокументирует в Task 6.

- [ ] **Step 1: Написать failing tests `internal/i18n/i18n_test.go`**

```go
package i18n

import "testing"

func TestResolvePriority(t *testing.T) {
    cases := []struct {
        name, explicit string
        environ        []string
        lcAll, langEnv string
        want           string
    }{
        {"explicit wins", "en", []string{"FSTAB_EDIT_LANG=ru"}, "ru_RU.UTF-8", "ru_RU.UTF-8", "en"},
        {"env beats locale", "", []string{"FSTAB_EDIT_LANG=en"}, "ru_RU.UTF-8", "ru_RU.UTF-8", "en"},
        {"lc_all beats lang", "", nil, "en_US.UTF-8", "ru_RU.UTF-8", "en"},
        {"lang env", "", nil, "", "en_GB.UTF-8", "en"},
        {"ru locale", "", nil, "ru_RU.UTF-8", "", "ru"},
        {"default ru", "", nil, "", "", "ru"},
        {"invalid explicit falls through", "de", []string{"FSTAB_EDIT_LANG=en"}, "", "", "en"},
        {"invalid env falls through", "", []string{"FSTAB_EDIT_LANG=de"}, "", "en_US", "en"},
        {"posix empty locale ignored", "", nil, "C", "POSIX", "ru"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := Resolve(tc.explicit, tc.environ, tc.lcAll, tc.langEnv); got != tc.want {
                t.Fatalf("Resolve() = %q, want %q", got, tc.want)
            }
        })
    }
}

func TestTAndTf(t *testing.T) {
    // Ключ "error.io.read": ru != en; Tf подставляет аргументы;
    // отсутствующий ключ возвращает сам ключ и не паникует (оба языка);
    // отсутствующий перевод в ru возвращает en.
    if T("ru", "error.io.read") == T("en", "error.io.read") { t.Fatal("ru/en совпали") }
    if got := Tf("ru", "error.io.read", "/x"); !contains(got, "/x") { t.Fatalf("args: %q", got) }
    if got := T("ru", "no.such.key"); got != "no.such.key" { t.Fatalf("fallback: %q", got) }
    if got := T("en", "no.such.key"); got != "no.such.key" { t.Fatalf("en fallback: %q", got) }
}

func TestAcceptYes(t *testing.T) {
    yes := map[string][]string{
        "ru": {"y", "Y", "yes", "д", "Д", "да", "ДА"},
        "en": {"y", "Y", "yes", "YES"},
    }
    for lang, answers := range yes {
        for _, a := range answers {
            if !AcceptYes(lang, a) { t.Fatalf("%s: %q должен быть yes", lang, a) }
        }
    }
    for _, a := range []string{"", "n", "no", "нет", "дд", "absolutely", "даdа"} {
        if AcceptYes("ru", a) || AcceptYes("en", a) {
            // "нет" в обоих языках — No; "даdа" (смесь) не принимается.
            t.Fatalf("%q не должен приниматься", a)
        }
    }
}
```

- [ ] **Step 2: Написать failing tests для cliapp `internal/cliapp/types_i18n_test.go`**

```go
package cliapp

import "testing"

func TestExitErrorCarriesKey(t *testing.T) {
    e := UsageErrorK(nil, "usage.bad", "плохой флаг %s", "--foo")
    if e.Code != ExitUsage || e.MessageKey != "usage.bad" || len(e.MessageArgs) != 1 {
        t.Fatalf("UsageErrorK: %+v", e)
    }
    if e.Error() == "" { t.Fatal("fallback Message пуст") }
}

func TestPermissionErrorUsesCode3(t *testing.T) {
    if got := PermissionError(nil, "x"); got.Code != ExitPermission {
        t.Fatalf("code = %d, want 3", got.Code)
    }
    if got := PermissionErrorK(nil, "x.key", "x"); got.Code != ExitPermission {
        t.Fatalf("code = %d, want 3", got.Code)
    }
}
```

- [ ] **Step 3: Run tests to verify failure**

Run: `go test ./internal/i18n/ ./internal/cliapp/ -v` → FAIL (undefined).

- [ ] **Step 4: Реализация `internal/i18n/i18n.go` + каталоги**

```go
// Package i18n предоставляет каталоги сообщений RU/EN и выбор языка по
// приоритету ТЗ §15: --lang > FSTAB_EDIT_LANG > LC_ALL > LANG > ru.
// Отсутствующий перевод возвращает English, затем сам ключ — panic
// исключён. Коды, enum values и JSON keys не переводятся и здесь не живут.
package i18n

import (
    "fmt"
    "os"
    "strings"
)

// catalogs — единственный источник переводов: catalogs[lang][key].
var catalogs = map[string]map[string]string{
    "en": catalogEN,
    "ru": catalogRU,
}

// IsValidLanguage сообщает, поддерживается ли язык.
func IsValidLanguage(lang string) bool {
    _, ok := catalogs[lang]
    return ok
}

// Languages возвращает поддерживаемые языки в стабильном порядке.
func Languages() []string { return []string{"en", "ru"} }

// Resolve выбирает язык. Невалидное значение на любом уровне игнорируется
// без ошибки — переход к следующему уровню; default — "ru".
func Resolve(explicit string, environ []string, lcAll, langEnv string) string {
    if IsValidLanguage(explicit) { return explicit }
    for _, pair := range environ {
        name, value, ok := strings.Cut(pair, "=")
        if ok && name == "FSTAB_EDIT_LANG" && IsValidLanguage(value) { return value }
    }
    for _, locale := range []string{lcAll, langEnv} {
        if lang := languageFromLocale(locale); IsValidLanguage(lang) { return lang }
    }
    return "ru"
}

// languageFromLocale извлекает язык из locale вида "ru_RU.UTF-8", "en",
// "C"/"POSIX" (последние — пустой результат).
func languageFromLocale(locale string) string {
    base, _, _ := strings.Cut(locale, ".")
    base = strings.ReplaceAll(base, "-", "_")
    lang, _, _ := strings.Cut(base, "_")
    return lang
}

// T возвращает перевод ключа: lang → en → сам ключ. Паника исключена,
// неизвестный ключ виден в выводе как есть — это безопаснее молчаливой
// пустой строки при диагностике сбоев.
func T(lang, key string) string {
    if c, ok := catalogs[lang]; ok {
        if msg, ok := c[key]; ok { return msg }
    }
    if msg, ok := catalogs["en"][key]; ok { return msg }
    return key
}

// Tf подставляет аргументы в перевод. Аргументы должны быть самодостаточными
// значениями (пути, ID, имена флагов), не фразами: порядок %s фиксирован
// ключом, что допустимо для всех текущих сообщений.
func Tf(lang, key string, args ...any) string {
    return fmt.Sprintf(T(lang, key), args...)
}

// yesAnswers — документированные localized формы подтверждения (ТЗ §15).
// Default — No: любая строка вне списка отклоняется.
var yesAnswers = map[string]map[string]struct{}{
    "en": {"y": {}, "yes": {}},
    "ru": {"y": {}, "yes": {}, "д": {}, "да": {}},
}

// AcceptYes сообщает, является ли ответ localized формой «да».
func AcceptYes(lang, answer string) bool {
    forms, ok := yesAnswers[lang]
    if !ok { forms = yesAnswers["en"] }
    _, ok = forms[strings.ToLower(strings.TrimSpace(answer))]
    return ok
}
```

Каталоги: `catalog_ru.go` и `catalog_en.go` — `var catalogRU = map[string]string{...}` / `catalogEN`. В Task 3 заводим полный набор ключей **cliapp** (таблица ниже) и ключи **cmd** (таблица в Task 5) — каталоги заполняются один раз здесь, чтобы Task 4/5 только переключали creation sites на ключи без расширения пакета i18n. Значения RU = текущие русские строки дословно, EN = приведённые ниже.

Ключи cliapp (RU — текущий текст, `%s`/`%d` сохраняются; EN — перевод):

| key | ru | en |
|---|---|---|
| `error.io.read_target` | `прочитать target %s` | `read target %s` |
| `error.io.create_backup` | `создать backup target %s` | `create backup of target %s` |
| `error.io.read_backup` | `прочитать backup %s` | `read backup %s` |
| `error.io.check_backup` | `проверить backup %s` | `verify backup %s` |
| `error.io.replace_target` | `заменить target %s резервной копией %s` | `replace target %s with backup %s` |
| `error.io.select_backup` | `выбрать резервную копию` | `select backup` |
| `error.io.save_changes` | `сохранить изменения` | `save changes` |
| `error.io.create_safety_backup` | `создать safety backup target %s` | `create safety backup of target %s` |
| `error.validation.mutation_blocked` | `кандидат не прошёл внутреннюю проверку (severity=error), изменения не сохранены` | `candidate failed internal validation (severity=error), changes not saved` |
| `error.validation.mutation_not_applied` | `мутация не применена` | `mutation not applied` |
| `error.validation.document_not_set` | `документ не задан` | `document is nil` |
| `error.validation.unknown_mutation` | `неизвестный вид мутации` | `unknown mutation kind` |
| `error.validation.selector_ambiguous` | `селектор неоднозначен` | `selector is ambiguous` |
| `error.validation.backup_not_found` | `резервная копия не найдена` | `backup not found` |
| `error.validation.failed` | `проверка target %s завершилась с диагностиками severity=error` | `target %s validation finished with severity=error diagnostics` |
| `error.external.verify_failed` | `внешняя проверка target %s завершилась с severity=error` | `external check of target %s finished with severity=error` |
| `error.external.post_rollback` | `post-check target %s после восстановления завершился с severity=error` | `post-check of target %s after restore finished with severity=error` |
| `error.io.mutation_not_saved` | `мутация не сохранена` | `mutation not saved` |

(Реальный набор ключей фиксируется по факту всех creation sites в Task 4; таблица — обязательный минимум, реализация обязана покрыть каждый site, включая select.go.)

Ключи cmd (Task 5):

| key | ru | en |
|---|---|---|
| `error.usage.unknown_output` | `неизвестный формат вывода %q: допустимы table, json, csv, tsv` | `unknown output format %q: valid values are table, json, csv, tsv` |
| `error.usage.bad_log_level` | `некорректный --log-level %q: допустимы debug, info, warn, error` | `invalid --log-level %q: valid values are debug, info, warn, error` |
| `error.usage.bad_lang` | `некорректный --lang %q: допустимы ru, en` | `invalid --lang %q: valid values are ru, en` |
| `error.usage.verbose_quiet` | `флаги --verbose и --quiet несовместимы: выберите один режим вывода` | `flags --verbose and --quiet are mutually exclusive` |
| `error.usage.no_tty` | `подтверждение требует TTY: в неинтерактивном режиме используйте --yes` | `confirmation requires a TTY: use --yes in non-interactive mode` |
| `error.usage.bad_flag` | `некорректный флаг или аргументы: %v` | `invalid flag or arguments: %v` |
| `error.usage.add_requires` | `add требует непустой флаг %s` | `add requires a non-empty %s flag` |
| `error.usage.edit_no_changes` | `edit требует хотя бы один изменяющий флаг: --device, --mount, --type, --options, --dump, --pass, --comment, --clear-comment, --unset-dump, --unset-pass` | `edit requires at least one modifying flag` (EN краткий: перечисление флагов остаётся как в RU) |
| `error.usage.comment_conflict` | `флаги --comment и --clear-comment несовместимы: выберите один` | `flags --comment and --clear-comment are mutually exclusive` |
| `prompt.apply_changes` | `Применить изменения? [y/N] ` | `Apply changes? [y/N] ` |
| `prompt.candidates` | `кандидаты: строки %s` | `candidates: lines %s` |
| `status.no_changes` | `изменений нет: target не изменён, backup и lock не создавались` | `no changes: target unchanged, no backup or lock created` |
| `status.cancelled` | `операция отменена пользователем: target не изменён` | `operation cancelled by user: target unchanged` |
| `status.render_failed` | `не удалось сформировать вывод результата: ` | `failed to render result output: ` |
| `chooser.ambiguous_header` | `селектор неоднозначен, выберите строку:` | `selector is ambiguous, choose a line:` |
| `chooser.line_prompt` | `номер строки: ` | `line number: ` |

Help-ключи (`help.root.short`, `help.cmd.<name>.short`, `help.flag.<имя>`) — реализация Task 5 составляет полный инвентарь из текущих строк `cmd/*.go` и добавляет в оба каталога; принцип тот же: RU = текущий текст, EN = перевод.

- [ ] **Step 5: Реализация изменений `internal/cliapp/types.go`**

```go
// ExitError — типизированная ошибка CLI. Message — русский fallback для
// логов и Error(); MessageKey/MessageArgs — i18n-ключ и аргументы: точка
// печати (cmd) переводит сообщение выбранным языком, доменный слой остаётся
// чистым от выбора языка. MessageKey == "" означает «нет перевода».
type ExitError struct {
    Code        int
    Message     string
    MessageKey  string
    MessageArgs []any
    Cause       error
    Candidates  []int
}

// ExitPermission означает недостаток прав: небезопасный config, недоступный
// журнал. Код 3 ранее был зарезервирован; активируется этим блоком.
const ExitPermission = 3
```

Добавить `NewExitErrorK(code, cause, candidates, key string, args ...any)` (строит Message через fmt.Sprintf с теми же args — fallback сохраняется), `UsageErrorK`, `ValidationErrorK`, `AmbiguityErrorK`, `PermissionErrorK`. Существующие `UsageError`/`ValidationError`/`AmbiguityError`/`PermissionError` делегируют K-вариантам с пустым ключом. `PermissionError*` → code `ExitPermission` (3). Sentinel `ErrPermission` менять нельзя (errors.Is в тестах).

- [ ] **Step 6: Run tests, vet, lint, commit**

```bash
go test ./... && go vet ./... && golangci-lint run
git add internal/i18n/ internal/cliapp/
git commit -m "feat: add i18n core and exit error message keys"
```

---

### Task 4: cliapp — перевод всех creation sites на ключи

**Files:**
- Modify: `internal/cliapp/service.go`, `internal/cliapp/rollback.go`, `internal/cliapp/select.go`, `internal/cliapp/mutation.go`
- Test: расширить `internal/cliapp/types_i18n_test.go` инвентарным тестом

**Interfaces:**
- Consumes: Task 3 (`*ErrorK`, каталоги).
- Produces: каждый ExitError, доходящий до cmd, имеет непустой MessageKey.

- [ ] **Step 1: Инвентарный failing test**

```go
// TestAllExitErrorsHaveKeys перебирает представительные сценарии каждого
// публичного метода cliapp (List с несуществующим файлом, Validate, Backup,
// Rollback, PrepareMutation с неоднозначным селектором, unknown kind) и
// требует: returned *ExitError != nil && MessageKey != "". Для сценариев,
// где методы возвращают только error (RunMutation), проверяется errors.As
// на *ExitError и непустой MessageKey.
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/cliapp/ -run TestAllExitErrorsHaveKeys -v` → FAIL (ключи пустые).

- [ ] **Step 3: Конвертация creation sites**

Заменить каждый вызов `newExitError/UsageError/ValidationError/AmbiguityError/PermissionError` с пользовательским форматом в `service.go`, `rollback.go`, `select.go`, `mutation.go` на K-вариант с ключом из таблицы Task 3 (таблица — минимум: реальные site'ы включают также строки select.go и любые сообщения, не вошедшие в таблицу; для каждого нового ключа добавить пару ru/en в оба catalog-файла). Правила:

- RU-значение ключа = дословно текущий формат (с теми же глаголами), аргументы в том же порядке.
- Формат с одним `%s` (путь target) → ключ `error.io.read_target` и т.п.
- Тесты cliapp, проверяющие `Message` или `Error()` тексты, не меняются — fallback сохраняется.

- [ ] **Step 4: Run tests, vet, lint, commit**

```bash
go test ./... && go vet ./... && golangci-lint run
git add internal/cliapp/
git commit -m "feat: localize cliapp error messages via keys"
```

---

### Task 5: cmd — i18n help, флаги, prompts, confirm

**Files:**
- Modify: `cmd/app.go` (локализация дерева команд, printExitError, confirm), `cmd/mutation.go`, `cmd/rollback.go`, `cmd/backup.go`, `cmd/list.go`, `cmd/validate.go`, `main.go` (chooser/prompt строки через i18n)
- Modify: `internal/i18n/catalog_ru.go`, `internal/i18n/catalog_en.go` (help-ключи)
- Test: `cmd/i18n_test.go`

**Interfaces:**
- Consumes: Task 3/4.
- Produces: `App.Execute` резолвит язык до `root.Execute` и вызывает `a.localize(lang)`; `App.lang` доступен handlers.

- [ ] **Step 1: Написать failing tests `cmd/i18n_test.go`**

```go
package cmd

// newTestApp — существующий helper cmd/app_test.go (если отсутствует,
// собрать по образцу e2e_test.go: memory buffers, TTY=false, --yes).
//
// TestHelpLocalized: app.Execute([]string{"--lang", "en", "--help"}) →
//   stdout содержит "safe fstab editing" (help.root.short EN) и не содержит
//   русского "безопасное редактирование fstab".
// TestHelpDefaultRussian: без --lang (environ пуст — helper передаёт nil
//   environ в Resolve) → stdout содержит русский Short.
// TestErrorMessageLocalized: несуществующий target, --lang en → stderr
//   содержит "read target", не содержит "прочитать target".
// TestConfirmLocalizedYes: TTY=true, scripted stdin "да\n" (ru) → мутация
//   применяется; "y\n" при --lang en → применяется; "\n" → отмена (No).
// TestFlagUsageLocalized: --lang en --help содержит английский usage
//   флага --dry-run (по ключу help.flag.dry_run).
```

(Все сценарии — с временным fstab-файлом в `t.TempDir()`, передаваемым через `--file`; реальный `/etc/fstab` не затрагивается.)

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./cmd/ -run 'TestHelp|TestErrorMessage|TestConfirmLocalized|TestFlagUsage' -v` → FAIL.

- [ ] **Step 3: Реализация**

В `cmd/app.go`:

```go
// Execute резолвит язык до построения help-текстов: pre-scan args на
// --lang/--lang= (последнее вхождение), иначе env/локаль. Config-файл здесь
// ещё не загружен — для help достаточно CLI/env/локали; config задаёт
// поведение в PersistentPreRunE.
func (a *App) Execute(args []string) int {
    a.lang = i18n.Resolve(scanLangFlag(args), os.Environ(), os.Getenv("LC_ALL"), os.Getenv("LANG"))
    a.localize(a.lang)
    a.root.SetArgs(args)
    ... // прежнее тело без изменений
}
```

(Production os.* — только здесь и в main; тесты через injectable: `AppOptions.Environ func() []string`? Нет — проще: `AppOptions.Getenv func(string) string` и `AppOptions.Environ func() []string`, nil → production os-значения. Добавить оба поля в AppOptions с nil-default.)

```go
// localize переводит Short/Long корня и всех подкоманд и usage-строки
// persistent/локальных флагов из каталога выбранного языка. Ключи:
// help.root.short, help.cmd.<use-префикс>.short, help.flag.<имя>. Дерево
// проходится до Execute, поэтому --help получает уже переведённые строки.
func (a *App) localize(lang string) { /* обход root.Commands() рекурсивно */ }

// printExitError печатает Tf(lang, MessageKey, MessageArgs...) при непустом
// ключе, иначе Message (русский fallback). Заголовок кандидатов — по ключу
// prompt.candidates.
func (a *App) printExitError(exitErr *cliapp.ExitError) { ... }

// confirm печатает prompt.apply_changes и принимает ответ через
// i18n.AcceptYes(a.lang, answer): ru допускает д/да, en — y/yes; default No.
func (a *App) confirm(diff string, warnings []models.Diagnostic) (bool, *cliapp.ExitError) { ... }
```

`--lang` default меняется с `"ru"` на `""` (пусто = авто-резолв; `validateGlobalFlags` принимает пустое и резолвленное значение). Флаги и help-ключи: для каждого `flags.*Var(..., "имя", ..., "русский usage")` usage заменяется на `i18n.T(lang, "help.flag.<имя>")` внутри `localize`; каталоги получают пары ru/en для всех persistent и командных флагов (полный инвентарь — по строкам `cmd/*.go`, RU = текущие usage дословно).

`main.go`: строки `interactiveChooser` («селектор неоднозначен…», «номер строки: ») переводятся через ключи chooser.* с языком, резолвленным в main (тот же `i18n.Resolve("", os.Environ(), ...)`; chooser получает lang полем).

- [ ] **Step 4: Run tests, vet, lint, commit**

```bash
go test ./... && go vet ./... && golangci-lint run
git add cmd/ internal/i18n/ main.go
git commit -m "feat: localize cli help flags and prompts"
```

---

### Task 6: wiring — config/logger в pipeline, README

**Files:**
- Modify: `cmd/app.go`, `cmd/mutation.go`, `cmd/rollback.go`, `cmd/backup.go`
- Modify: `main.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 1 (`config.Load`), Task 2 (`logger`), Task 5 (`App.lang`).

- [ ] **Step 1: Написать failing e2e-тесты (в `cmd/e2e_test.go` или новом `cmd/config_log_test.go`)**

```go
// Все сценарии: tempdir target/backup/config/log.
//
// TestConfigFileAffectsBackupDir: --config <yaml с backup.dir=tempdir/sub>
//   add --yes → backup-файл появляется именно в tempdir/sub.
// TestEnvOverridesConfig: тот же yaml + FSTAB_EDIT_BACKUP_DIR (через
//   AppOptions.Environ) → backup в env-каталоге.
// TestCLIPriority: --backup-dir перекрывает и env, и yaml.
// TestUnknownEnvWarns: FSTAB_EDIT_NOPE=1 → предупреждение в stderr, exit 0.
// TestUnsafeConfigRejected: explicit config с mode 0o664 → exit 3, target
//   не изменён.
// TestInvalidConfigRejected: yaml с неизвестным ключом → exit 2.
// TestMutationWritesLog: add --yes с log.file в tempdir → журнал существует,
//   ровно одна JSON-строка, поля: operation=add, result=success,
//   backup_id непустой, target, duration_ms >= 0, real_uid/effective_uid.
// TestLogUnavailableBlocksMutation: log.file в несоздаваемом месте
//   (удалённый каталог) → exit 3 или 6, target байтово не изменён,
//   backup не создан.
// TestDryRunNoLog: --dry-run → файла журнала нет.
// TestReadOnlyNoLog: list → файла журнала нет (log вообще не открывался).
// TestLogRedactsSecrets: edit с options, содержащими password=abc → в
//   old_value/new_value журнала "***", "abc" отсутствует.
// TestCancelledMutationLogged: TTY отвечает "n" → журнал содержит событие
//   result=cancelled.
// TestRollbackLogged: backup + изменение + rollback → события rollback со
//   result=success и backup_id восстановленной копии.
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./cmd/ -run 'TestConfig|TestLog|TestEnv' -v` → FAIL.

- [ ] **Step 3: Реализация wiring**

В `cmd/app.go`:

```go
// settings — резолвленные настройки текущего запуска (Task 1).
// log — открытый журнал; nil для read-only и dry-run (ТЗ §14: файловый
// журнал не создаётся). openedFor — имя команды, под которую открыт.
type App struct {
    options  AppOptions
    service  *cliapp.Service
    flags    globalFlags
    stdin    *bufio.Reader
    root     *cobra.Command
    lang     string
    settings config.Settings
    log      *logger.Logger
}
```

`resolveSettings(cmd *cobra.Command)`: `config.Load(LoadOptions{ExplicitPath: a.flags.config, Environ: a.environ(), EUID: os.Geteuid(), LookupUser: config.LookupUserByUID})`; merge CLI flags поверх (только `cmd.Flags().Changed`): `file`, `backup-dir`, `log-level`, `lang` (уже в a.lang — но config/env уже учтены: CLI --lang выше), `output`. Warnings печатаются в stderr с префиксом `warning: `. Ошибки маппятся: `LoadError.Kind == "security"` → `ExitError{Code: cliapp.ExitPermission}` (3), остальные → code 2. `--lang` остаётся высшим: резолв языка в Execute уже сделан по CLI/env/локали; config язык не задаёт.

`PersistentPreRunE` после `validateGlobalFlags`: resolveSettings → если команда мутирующая (`add/edit/delete/comment/uncomment/rollback/backup`) и не `--dry-run` → openLog. openLog: `logger.Open(logger.Options{File: settings.LogFile, Level: logger.Severity(settings.LogLevel), MaxSizeMB: settings.LogMaxSizeMB, Keep: settings.LogKeep, RealUID: os.Getuid(), EffectiveUID: os.Geteuid(), SudoUser/SudoUID: проверенные из os.Getenv})`; ошибка → `ExitError{Code: 3 при os.IsPermission иначе 6}`, target не изменён. `a.log` закрывается в конце Execute (`defer`-паттерн вокруг root.Execute, nil-safe).

В `cmd/mutation.go` `finishMutation`:
- до `service.RunMutation` журнал уже открыт (гарантия доступности — ТЗ §14).
- вокруг RunMutation: `start := time.Now()` (через `AppOptions.Now`, default time.Now); после — собрать `logger.Event{Severity: info|error, Operation: operationFor(preview.Operation), Target: target, BackupID: executed.BackupID, Result: success|error, ErrorCode: code из ExitError, DurationMs: elapsed, OldValue/NewValue: logger.Redact(diffChangedLines(preview.Diff))}`; `a.log.Log(event)` — ошибка игнорируется (не заменяет первичную; после replace не мешает rollback). При `!proceed` (отмена) — событие `result=cancelled` (severity info), error code 0.
- `operationFor` маппит `backup.Operation` (before_add → logger.OperationAdd и т.д.) — helper в cmd.

`diffChangedLines(diff string) (old, new string)`: строки `-`-префикса (кроме `---`) → old, `+`-префикса (кроме `+++`) → new, join через `\n`. Функция в `cmd/mutation.go` с русским комментарием, unit-тест в `cmd` на синтетическом diff.

`cmd/rollback.go`: аналогично логировать `operation=rollback`, `backup_id` выбранной копии, result/duration; dry-run — без журнала (файл не открывается). `cmd/backup.go`: `operation=backup`. Read-only `list`/`validate`/`verify-mount` журнал не открывают.

`main.go`: проброс `Getenv`/`Environ`/`Getuid`/`Geteuid` не нужен (production defaults), но main передаёт резолвленный язык chooser'у (см. Task 5).

README: удалить абзац «Ограничения текущего блока» (строка ~23) и пункт про «обязательный logger/config остаётся следующим блоком» (строка ~158); добавить разделы: «Конфигурация» (пути, приоритеты, env-переменные, security checks), «Журналирование» (JSON Lines, rotation, redaction, гарантия доступности), «Локализация» (ru/en, приоритет, yes/no формы), exit code 3 — активирован (config/log permission), ограничение: Cause-цепочки и Cobra unknown-flag не переведены.

- [ ] **Step 4: Run tests, vet, lint, full checks**

```bash
go test ./... -v
go test -race ./...
go vet ./...
golangci-lint run
go test ./... -cover
CGO_ENABLED=0 go build -o fstab-edit .
rm fstab-edit
```

Expected: все PASS, lint 0 issues, coverage пакетов config/logger/i18n ≥ 85%, cmd и cliapp не ниже текущих.

- [ ] **Step 5: Commit**

```bash
git add cmd/ main.go README.md
git commit -m "feat: wire config and logger into cli pipeline"
```

---

## Self-Review

**Spec coverage:**
- §13.1 приоритеты → Task 1 Load + Task 6 merge CLI; §13.2 security → Task 1 checkOwnership/skip-user-config-for-root/symlink; §13.3 схема → fileConfig (tui сохраняется); §13.4 env → applyEnv + warnings.
- §14 формат/поля/rotation → Task 2; доступность до mutation и неблокирование rollback → Task 6 openLog + игнор ошибок Log после replace; dry-run/read-only без файла → Task 6.
- §15 языки/приоритет/fallback/yes-no → Task 3; перевод help/errors/prompts → Task 4/5; непереводимые коды — границы пакета i18n.
- Код 3 активирован → Task 3 + README Task 6.

**Placeholder scan:** тесты Task 1 Step 2 содержат описания сценариев вместо полного кода (TestLoadPriorityCLIEnvUserSystemDefaults и др.) — исполнитель обязан развернуть их в полные тесты по имени сценария; это намеренно: точные tempdir-пути выбирает исполнитель, семантика зафиксирована комментарием. Все прочие шаги содержат код или исчерпывающие правила.

**Type consistency:** `config.Settings`/`LoadOptions`/`LoadError.Kind` единообразны между Task 1 и Task 6; `logger.Options/Event/Operation` единообразны между Task 2 и Task 6; `i18n.Resolve/T/Tf/AcceptYes` единообразны в Task 3/5; `cliapp.ExitPermission` и `*ErrorK` — в Task 3/4. `ExitError.Cause` сохраняется везде.
