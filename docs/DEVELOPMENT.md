# Руководство для разработчиков fstab-edit

Документ для тех, кто вносит изменения в код проекта: архитектура и границы
пакетов, сборка и тесты, стандарты кода и review-workflow, релизный процесс.

## 1. Архитектура

### Слои и направление зависимостей

Зависимости идут в одну сторону, обратных импортов нет:

```text
cmd/, tui/  →  internal/*  →  pkg/models
```

- `cmd` и `tui` — два интерфейса (CLI на Cobra и TUI на bubbletea) поверх
  одного и того же доменного ядра. Отдельной реализации записи в TUI нет:
  сохранение и restore идут через те же `internal/transaction` и
  `internal/backup`, что и CLI.
- `internal/*` — доменная логика и инфраструктура; пакеты внутри слоя
  не образуют циклов (например, `transaction` использует `backup`, `writer`,
  `validator` и `sysutil`, но не наоборот).
- `pkg/models` — публичные модели данных (`Document`, `Entry`, `Diagnostic`),
  нижний уровень; ничего не импортирует из проекта.

### Пакеты

**`pkg/models`** — публичный API данных: документ, записи fstab, диагностики
с severity (error/warning). Сообщения диагностик — русские строки нижележащего
слоя, они не локализуются.

**`internal/parser`** — lossless-парсер fstab: комментарии, spacing и порядок
строк сохраняются; рендер документа побайтово воспроизводит неизменённые
участки исходного файла.

**`internal/editor`** — селекторы записей (по строке, mount point, source)
и in-memory мутации: Add, Edit, Delete, Comment/Uncomment, включая связанные
блоки комментариев. Ничего не пишет на диск.

**`internal/validator`** — статическая проверка документа в памяти: поля,
source, mount point, типы ФС (экспортируемый `KnownFSTypes` — единый перечень,
используемый также автодополнением TUI), опции, дубликаты. Детерминирован,
не запускает внешние утилиты.

**`internal/writer`** — низкоуровневая безопасная запись: `ReadSnapshot`
(точные байты + SHA-256), `AcquireLock` (flock в отдельном файле, каталог 0700,
файл 0600), `VerifyBaseHash` (обнаружение внешнего изменения target) и
атомарный `Replace` (temp 0600 → fchown/fchmod → xattrs → fsync → rename →
directory sync → повторное чтение с проверкой SHA-256). Сам по себе rollback
не выполняет — это зона `transaction`.

**`internal/backup`** — резервные копии: `Create`, `Open`, `List`, `Select`,
`Rotate`. Имена `<prefix>.<backup ID>` + sidecar `.json` (schema v1, строгий
JSON), mode 0600, верификация по SHA-256, отказ от symlink-подмены, ротация
только собственных проверенных пар. Константы дефолтов: `DefaultBackupPrefix`,
`DefaultKeepBackups`.

**`internal/sysutil`** — безопасный запуск внешних утилит. Граница отказа —
интерфейс `CommandRunner`: производственный код вызывает `findmnt --verify`
(проверка кандидата), `lsblk` и `blkid` только через него; в тестах runner
подменяется fixture-выводом, реальные утилиты не запускаются. Исполняемые
файлы разрешаются только в фиксированных системных каталогах, shell не
используется, timeout 10 с и лимит 64 KiB на поток stdout/stderr.

**`internal/transaction`** — доменная оркестрация записи: внутренняя валидация
→ lock → повторный base SHA-256 → backup операции → findmnt candidate check →
atomic replace → post-check → ровно один automatic rollback при ошибке после
replace → ротация после полного успеха. No-op кандидат (байтово равен target)
завершается до lock и записи. Возвращает типизированный `FailureKind`,
который CLI маппит на стабильные exit codes.

**`internal/config`** — YAML-конфигурация и приоритеты (ТЗ §13):
CLI > `FSTAB_EDIT_*` env > user YAML > system YAML > defaults. Security-checks:
symlink отклоняется, group/others-writable отклоняется, system config —
root-ownership. Неизвестные ключи и некорректные значения — явная ошибка.
Пакет не читает реальные системные пути в тестах: пути, окружение и EUID
передаются через `LoadOptions`.

**`internal/logger`** — структурный JSON Lines-журнал с rotation и redaction
секретов (`password=`, `credentials=`, `token=`, userinfo URL → `***`).

**`internal/i18n`** — локализация RU/EN: каталоги строк, резолв языка
(`--lang` > `FSTAB_EDIT_LANG` > `LC_ALL` > `LANG` > ru).

**`internal/diff`** — line-based unified diff для preview перед мутациями.

**`internal/output`** — рендереры `table`/`json`/`csv`/`tsv` (JSON —
`schema_version: 1`).

**`internal/cliapp`** — доменный слой CLI: селекторы записей, preview/diff,
service-обработчики команд, rollback-flow. Сюда же вынесены зависимости
(`cliapp.Dependencies`) — границы отказа CLI.

**`cmd`** — команды Cobra: флаги, prompt-подтверждения `[y/N]`, маппинг
`FailureKind` на exit codes 0–8, подключение config/logger/i18n.

**`tui`** — TUI на bubbletea/lipgloss: таблица записей с подсветкой синтаксиса
и severity, формы add/edit с автодополнением, undo, экраны diff/монтирований/
устройств/истории/журнала/помощи, темы dark/light/high-contrast/monochrome.
Граница отказа — `tui.Deps` (зеркало `cliapp.Dependencies` плюс журнал):
тесты подменяют fake-реализациями чтение target, транзакцию, список копий,
устройства и журнал. TUI не импортируется из `cmd` или `internal` —
направление зависимости только `tui → internal`.

### Транзакционная запись

Единственный безопасный путь изменения target — `internal/transaction.Execute`
(и обратная операция restore через `internal/backup` + `writer.Replace` под
контролем transaction-подобного flow). Любая новая точка записи (новая команда
CLI, новый экран TUI) обязана проходить через них, а не писать файл напрямую:
только так сохраняются backup, lock, проверка кандидата findmnt, атомарность
и automatic rollback.

### Границы отказа (seams для тестов)

- `sysutil.CommandRunner` — все внешние утилиты (findmnt/lsblk/blkid);
  в тестах подменяется fixture-выводом.
- `tui.Deps` / `cliapp.Dependencies` — чтение target, транзакция, backup,
  устройства, журнал; fake-реализации живут в тестах.
- `config.LoadOptions` — пути конфигов, окружение, EUID и lookup-пользователя
  передаются извне.
- `systemConfigPath` — package-level var, подменяется тестами на tempdir;
  production-код её не меняет.

## 2. Сборка и тесты

Требования: Go ≥ 1.22, Linux, `make`. Сборка и тесты не требуют root.

```bash
make            # сборка статического бинаря ./fstab-edit
make build      # то же явно; версия из git describe
make version    # версия, которую получит следующая сборка
make test       # go test ./...
make install    # сборка + установка бинаря и man-страницы (PREFIX, DESTDIR)
make uninstall  # удаление установленных файлов
make pdf        # PDF руководства пользователя (pandoc + xelatex)
make clean      # удалить локальный бинарь
```

Проверки перед коммитом (должны проходить чисто):

```bash
go test ./... -count=1
go vet ./...
golangci-lint run
gofmt -l .        # пустой вывод = всё отформатировано
go test -race ./...   # рекомендуется для изменений в tui/concurrency
```

## 3. Стандарты кода

- **Язык:** все комментарии, сообщения об ошибках и пользовательский текст
  (RU-часть) — на русском. Каталоги i18n (`tui.*`, CLI-сообщения) добавляются
  парно RU/EN; коды, severity и поля machine-форматов не локализуются.
- **TDD:** сначала тест, потом реализация. Каждый фикс бага начинается с
  падающего теста.
- **Тесты изолированы:** только временные каталоги (`t.TempDir()`) и fake seams
  (CommandRunner, Deps, LoadOptions). Запрещено: читать/писать реальный
  `/etc/fstab`, запускать реальные `findmnt`/`lsblk`/`blkid`, зависеть от
  системной конфигурации. Разрушительные и привилегированные проверки —
  только в изолированных VM.
- **Детерминизм:** доменная логика не зависит от текущего времени, окружения и
  порядка обхода map; время и случайность (backup ID и т.п.) — через
  инъектируемые параметры или контролируемые значения.
- **gofmt/vet/lint:** код проходит `gofmt`, `go vet` и `golangci-lint` без
  замечаний.

### Review-workflow

1. Реализатор выполняет задачу (TDD) и проводит проверки из раздела 2.
2. Отдельный reviewer проверяет код. Замечания классифицируются: **Critical**
   (безопасность, потеря данных, нарушение контрактов) и **Important**
   (архитектура, тестируемость, ясность) подлежат исправлению; после фиксов —
   повторный review затронутых мест.
3. После закрытия этапа/ветки — финальный whole-branch review всей ветки:
   критерии ТЗ, парность i18n, покрытие тестами, vet/lint/gofmt, сборка.
4. Затем ветка merge'ится в `dev` fast-forward (без merge-коммитов).

## 4. Релизный процесс

1. Финальный whole-branch review и merge ветки этапа в `dev` (fast-forward).
2. Подготовка релиза: `dev` проверен и готов; версия берётся из git, ручной
   ввод не нужен (`git describe --tags`).
3. Fast-forward merge `dev` → `main`.
4. Тег релиза на `main`: `git tag -a vX.Y -m "release vX.Y"`.
5. Сборка из тега: `make build` — версия бинаря будет ровно `vX.Y`
   (проверить `make version` и `./fstab-edit --version`).
6. Публикация релиза с бинарём:

   ```bash
   gh release create vX.Y ./fstab-edit \
       --title "vX.Y — <краткое название>" --notes-file <release-notes.md>
   ```

7. Обновить `CHANGELOG.md`: перенести записи из `[Unreleased]` в новый
   раздел `[X.Y]` с датой тега.

Ветки: `main` — релизная линия (теги), `dev` — основа разработки;
feature-ветки этапов создаются от `dev` и merge'ятся обратно в `dev`.
Релизные номера решает владелец проекта после merge этапа.

## 5. Структура каталогов

```text
├── main.go               # Production wiring процессовых зависимостей
├── cmd/                  # Команды Cobra: флаги, prompt, маппинг exit codes
├── pkg/
│   └── models/           # Публичные модели: Document, Entry, Diagnostic
├── internal/
│   ├── parser/           # Lossless-парсер и побайтовое сохранение
│   ├── editor/           # Селекторы и in-memory мутации
│   ├── validator/        # Статическая проверка, KnownFSTypes
│   ├── writer/           # Snapshot, flock, base hash, атомарный Replace
│   ├── backup/           # Backup, sidecar-метаданные, Select, ротация
│   ├── transaction/      # Доменная оркестрация записи и automatic rollback
│   ├── sysutil/          # CommandRunner, безопасный запуск findmnt/lsblk/blkid
│   ├── config/           # YAML-конфигурация, приоритеты, security-checks
│   ├── logger/           # JSON Lines-журнал, rotation, redaction
│   ├── i18n/             # Локализация RU/EN
│   ├── diff/             # Line-based unified diff для preview
│   ├── output/           # Рендереры table/json/csv/tsv
│   └── cliapp/           # Доменный слой CLI, Dependencies
├── tui/                  # TUI на bubbletea: таблица, формы, экраны, темы
│   ├── deps.go           # Границы отказа TUI (fake-реализации в тестах)
│   └── theme.go          # Палитры тем и semantic-стили подсветки
├── docs/
│   ├── user-guide.md     # Руководство пользователя (PDF: make pdf)
│   ├── DEVELOPMENT.md    # Этот документ
│   └── man/
│       └── fstab-edit.1  # Man-страница (make install ставит её в man1)
├── examples/
│   ├── config-minimal.yaml  # Минимальный пример конфигурации
│   └── config-full.yaml     # Все ключи конфигурации с пояснениями
├── CHANGELOG.md          # История изменений (Keep a Changelog)
├── Makefile              # build/install/uninstall/test/pdf/version
└── README.md             # Обзор проекта для пользователей
```
