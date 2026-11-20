# fstab-edit

Утилита для безопасного редактирования `/etc/fstab` в Linux.

Проект утилиты с будущими CLI для автоматизации и TUI для интерактивной работы. Основные требования: транзакционная запись, статическая проверка файла-кандидата, резервные копии, rollback и побайтовое сохранение неизменённого содержимого.

## Разработка с использованием ИИ

Эта программа разрабатывается с использованием искусственного интеллекта **Kimi Code CLI от Moonshot AI (月之暗面)**. ИИ помогает проектировать архитектуру, писать код, тесты и документацию. Каждый реализованный блок проходит автоматизированные проверки и отдельный code review; требования и окончательные решения по проекту остаются за автором.

## Статус

- Текущий этап: **1 — ядро и CLI**. CLI реализован и собирается в статический бинарь `fstab-edit`; TUI остаётся будущим этапом.
- Реализованы модель данных, lossless parser, selectors и in-memory mutations: Add, Edit, Delete, Comment и Uncomment, включая связанные comment blocks.
- Реализованы публичная модель diagnostics и внутренний read-only validator: проверки полей, source/mount/FS type, опций, duplicates и специальные предупреждения работают детерминированно с документом в памяти.
- Реализован secure runner: исполняемые файлы разрешаются только в фиксированных системных каталогах, аргументы передаются без shell, локаль фиксируется как `LC_ALL=C`. По умолчанию действуют timeout **10 секунд** и ограничение **64 KiB на каждый поток** stdout/stderr.
- Реализованы `findmnt --verify --tab-file` и private candidate check. Содержимое кандидата записывается без нормализации и добавления newline в файл `0600` внутри временного каталога `0700`; после проверки предпринимается попытка `RemoveAll` при любом исходе. Ошибка удаления добавляет `findmnt.candidate_cleanup/warning`, сохраняя первичные diagnostics и `Execution.Err`. Внутренний validator и внешний отчёт объединяются в порядке internal → external.
- Классификация вывода проверена на fixtures наблюдённого формата **util-linux 2.42.4**. `findmnt` только статически проверяет файл-кандидат и ничего не монтирует.
- Raw stdout/stderr потенциально содержат sensitive-данные и не предназначены для записи в журнал без redaction; публичный `Report` содержит только обобщённые диагностики, а исходный вывод хранится отдельно в `Execution`.
- Реализованы secure snapshot/base-hash conflict, стабильные flock и низкоуровневый атомарный `Replace` с сохранением metadata/xattrs. Реализованы backup и ротация: `Create`, `Open`, `List`, `Select` и `Rotate` в `internal/backup`.
- Реализована доменная transaction в `internal/transaction`: internal validation, lock, повторный base SHA-256, `findmnt` candidate check, backup текущей операции, atomic replace, post-check, один automatic rollback и ротация после полного успеха.
- Реализован CLI на Cobra (`cmd/`, `main.go`): десять команд `list`, `add`, `edit`, `delete`, `comment`, `uncomment`, `validate`, `verify-mount`, `backup`, `rollback`; dry-run, подтверждения `[y/N]`, unified diff, селекторы записей с кодом неоднозначности 8 и форматы вывода `table`/`json`/`csv`/`tsv` со `schema_version: 1`. Тесты CLI работают только с временными файлами, fake runner и scripted stdin; реальный `/etc/fstab` и findmnt не затрагиваются.
- **Ограничения текущего блока**: флаги `--log-level`, `--lang` и `--config` принимаются и валидируются, но logger, YAML-config loader и полные каталоги i18n RU/EN реализуются следующим блоком; текущие пользовательские сообщения — на русском. Обязательный structured logger в `validate`/`verify-mount` и команда `tui` пока не подключены.
- Актуальная основа разработки: [ТЗ версии 2.0](../fstab-editor-v2.md).
- Исходная редакция сохранена: [ТЗ версии 1.0](../fstab-editor.md).

## CLI

Сборка и справка:

```bash
go build -o fstab-edit .
./fstab-edit --help
./fstab-edit --version
```

По умолчанию целевой файл — `/etc/fstab`; флаг `--file=<path>` переопределяет его (удобно для chroot и тестов). Флаги `--help` и `--version` не читают target и не запускают `findmnt`.

Команды:

| Команда | Назначение |
|---------|-----------|
| `list` | Показывает активные и закомментированные записи |
| `add` | Добавляет новую запись |
| `edit` | Изменяет выбранные поля/comment |
| `delete` | Удаляет запись |
| `comment` | Комментирует активную запись |
| `uncomment` | Включает закомментированную запись |
| `validate` | Проверяет документ (`--offline` — только внутренний validator) |
| `verify-mount` | Статический `findmnt --verify` по фактическим байтам target |
| `backup` | Создаёт ручную копию без изменения target |
| `rollback` | Восстанавливает выбранную копию |

Селектор записи — позиционный `<id>` (номер строки, mount point или source) либо ровно один из `--line`, `--mount-point`, `--device`; несколько совпадений дают exit code **8** со списком кандидатов. `add` требует `--device`, `--mount`, `--type`, `--options` (dump/pass по умолчанию `0`); `edit` требует селектор и хотя бы один изменяющий флаг.

Подтверждение `[y/N]` (по умолчанию отказ) обязательно для мутаций и `rollback`: без TTY требуется `--yes`, при этом `--yes` не обходит ошибки валидации, неоднозначность селектора и внешние сбои. Интерактивный diff и warning-preview печатаются в stderr только в prompt-режиме: `--yes` пропускает prompt целиком и ничего не печатает, а diff и diagnostics остаются доступными в полях machine-JSON (`diff`, `diagnostics`). `--dry-run` показывает diff и diagnostics, ничего не меняя и не требуя `--yes`.

Форматы вывода `--output=table|json|csv|tsv`; JSON — `schema_version: 1`. Обычные данные — stdout (ровно один structured document для machine-форматов), диагностика, diff и prompts — stderr. Exit codes: 0 успех (warnings допустимы), 1 validation, 2 usage, 3 **зарезервирован** (mapping недостатка прав и root-проверки реализуется следующим блоком; текущая версия этот код не генерирует), 4 lock/base conflict, 5 внешняя утилита, 6 I/O/backup/write, 7 критический rollback (наивысший приоритет), 8 неоднозначный селектор.

Примеры:

```bash
fstab-edit list
fstab-edit list -o json

fstab-edit add --device UUID=abc-123 --mount /mnt/data --type ext4 \
    --options defaults,noatime --dump 0 --pass 2

fstab-edit edit --mount-point /mnt/data --options defaults,noatime,nofail
fstab-edit delete --mount-point /mnt/data --yes

fstab-edit comment --line 12
fstab-edit uncomment --line 12

fstab-edit validate --file ./fstab.test --offline
fstab-edit verify-mount --file ./fstab.test

fstab-edit backup --file ./fstab.test
fstab-edit rollback --dry-run
fstab-edit rollback --restore=2026-11-18_15-30-45.123456789Z
```

Тесты CLI не используют реальный `/etc/fstab`: все сценарии работают с временными файлами, fake runner и scripted stdin.

## Структура проекта

```text
work/
├── cmd/                  # Команды Cobra: флаги, prompt, exit mapping
├── main.go               # Production wiring процессовых зависимостей
├── internal/
│   ├── cliapp/           # Доменный слой CLI: selectors, preview, service, rollback
│   ├── diff/             # Line-based unified diff для preview
│   ├── output/           # Renderers table/json/csv/tsv, schema v1
│   ├── parser/           # Lossless parser и побайтовое сохранение
│   ├── editor/           # Selectors и in-memory mutations
│   ├── validator/        # Синтаксическая и семантическая проверка
│   ├── writer/           # Блокировки, контроль версии и атомарная запись
│   ├── backup/           # Backup, метаданные, выбор и ротация
│   ├── transaction/      # Доменная оркестрация записи и automatic rollback
│   ├── config/           # YAML-конфигурация и приоритеты
│   ├── logger/           # Структурный журнал
│   ├── i18n/             # Локализация RU/EN
│   └── sysutil/          # Безопасный запуск внешних утилит
├── pkg/
│   └── models/           # Публичные модели данных
├── tui/                  # TUI-интерфейс
├── docs/                 # Документация проекта
├── examples/             # Примеры конфигурации
├── LICENSE.en            # Лицензия MIT, английский текст
├── LICENSE.ru            # Неофициальный перевод лицензии MIT
└── README.md             # Этот файл
```

## Безопасность при разработке

Никогда не используйте реальный `/etc/fstab` основной системы для разработки и ручных экспериментов. Тесты findmnt используют fake runner и не запускают внешнюю утилиту; candidate tests работают только с приватными временными файлами; CLI- и файловые тесты используют временные файлы и опцию `--file`; разрушительные и привилегированные проверки выполняются только в изолированных VM.

## Требования

- Go 1.22 или новее.
- Linux.
- Для обычных тестов root не требуется.
- `findmnt` обязателен для проверки перед сохранением.
- `blkid` и `lsblk` потребуются на этапе 3.
- `golangci-lint` используется для локальной проверки и CI.

## Проверки

```bash
go test ./... -v
go test ./... -cover
go test -race ./...
go vet ./...
golangci-lint run
```

## Документация по безопасности

Текущие гарантии низкоуровневого writer:

1. `ReadSnapshot` читает точные байты только из безопасно открытого regular-file descriptor. `VerifyBaseHash` повторно читает target и обнаруживает внешнее изменение по SHA-256, даже если size и mtime не изменились; read-only API не создают lock.
2. Lock создаётся только явным `AcquireLock` в отдельном стабильном файле, имя которого содержит SHA-256 canonical target. Проверяются владелец и modes каталога `0700` и файла `0600`; используются защищённые system/XDG/cache-каталоги. Стандартный timeout ожидания — **10 секунд**. Библиотека не удерживает lock во время prompt: его lifetime принадлежит будущей transaction.
3. `Replace` записывает candidate в temp `0600` в parent directory, затем строго выполняет `fchown` → `fchmod` → копирование xattrs через descriptors → file sync → file close → rename → directory sync → повторное чтение и проверку SHA-256. До успешного rename ошибка оставляет target старым и запускает cleanup temp; cleanup errors присоединяются через `errors.Join` без потери первичной причины.
4. UID/GID, permission bits и доступные xattrs, включая zero-length values, сохраняются. Ошибка чтения или установки конкретного security xattr запрещает replace. SELinux/AppArmor policy не изменяется; enforcing VM test остаётся отдельной release-проверкой и не считается выполненным на обычной temp FS.
5. Ошибка directory sync или post-rename verification возвращается с `ReplaceResult.Replaced=true`. Низкоуровневый writer сам не выполняет automatic rollback: доменная transaction при `err != nil && result.Replaced` восстанавливает backup текущей операции. При `Replaced=false` target считается неизменённым.

## Транзакция сохранения

Гарантии `internal/transaction.Execute`:

1. No-op candidate, байтово равный target, завершается до lock, validation, backup, `findmnt` и write с `Changed=false`; внутренние ошибки candidate запрещают lock и изменения.
2. Lock удерживается от повторного чтения target и base SHA-256 через backup, atomic replace, post-check, возможный rollback и разрешённую после успеха rotation. Внешнее изменение target запрещает backup/write и завершается классом `conflict`.
3. Backup создаётся только из snapshot, повторно прочитанного под lock, и заново проверяется через `backup.Open` до replace.
4. Post-write warnings допустимы; errors вызывают ровно одну попытку restore backup именно текущей transaction. `Replaced=false` никогда не вызывает rollback.
5. Успешный rollback возвращает primary error с `RolledBack=true`. Rollback failure возвращает класс `rollback`, сохраняет primary и rollback causes и recovery paths target/backup/metadata.
6. Rotation выполняется только после полного успешного post-check; backup текущей transaction передаётся protected. Ошибка rotation возвращает класс `rotation_after_success`, но не откатывает уже успешно записанный target.
7. CLI, prompt, diff rendering, dry-run и manual rollback подключены в `cmd/` и `main.go`; обязательный logger/config остаётся следующим блоком; transaction возвращает typed `FailureKind`, который CLI отображает на стабильные exit codes.

## Резервные копии и ротация

Реализованные гарантии `internal/backup` (`Create`, `Open`, `List`, `Select`, `Rotate`):

1. Backup ID — ядро имени файла в UTC строго формата `YYYY-MM-DD_HH-MM-SS.nnnnnnnnnZ` (ровно девять цифр nanoseconds и литеральный `Z`); `created_at` в sidecar обязан обозначать то же UTC-время, включая nanoseconds.
2. Имена файлов: `<prefix>.<backup ID>` для data и `<prefix>.<backup ID>.json` для sidecar. Default prefix — `fstab.bak`; default keep для ротации — **10** newest-копий. Каталог по умолчанию — каталог target.
3. Collision semantics: если имя занято, `Create` перебирает числовые суффиксы `.1`, `.2`, … (до 1000 попыток); symlink под занятым именем — небезопасная подмена и отклоняется без перехода на суффикс.
4. Sidecar schema v1 (`schema_version: 1`) — строгий JSON без unknown fields и trailing values: `backup_id`, `created_at`, `source_path` (canonical absolute clean target), `source_sha256`, `backup_sha256` (64 lowercase hex), `uid`, `gid`, `mode` (не выше `07777`), `operation` (whitelist: `before-add`, `before-edit`, `before-delete`, `before-comment`, `before-uncomment`, `before-rollback`, `manual`), `program_version`, `source_verification` (whitelist: `not_checked`, `valid`, `invalid`, `unavailable`).
5. `Create` никогда не изменяет target и возвращает `Backup` только после записи обоих файлов с mode `0600` через `O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC`, fsync каждого файла, повторного чтения и совпадения SHA-256 data с metadata. Backup гарантирует побайтовое восстановление данных копии, но `source_verification` не заявляет работоспособность повреждённого исходника: `invalid`/`unavailable` фиксируют факт проверки, а не исправляют документ.
6. `Open`/`List`/`Select` отклоняют symlink-подмену любого уровня, mode отличный от `0600`, corrupt schema, hash mismatch, неверные modes/enums/UTC, чужой `source_path` и чужой prefix. `List` возвращает verified копии текущего target в детерминированном newest-first порядке; `Select` различает not found (`ErrBackupNotFound`) и ambiguous collision ID (`ErrBackupAmbiguous`).
7. `Rotate` удаляет только собственные verified пары (sidecar, затем data): чужие, повреждённые, symlink и непроверенные записи каталога не входят в candidates и никогда не удаляются; `os.RemoveAll` не вызывается. `keep <= 0` заменяется default 10, protected ID сохраняются даже в зоне удаления, `os.ErrNotExist` при удалении не считается сбоем, а ошибка удаления останавливает ротацию с partial `RotationResult` (`Failed`) и сохранённой первичной ошибкой через `errors.Is`. Ошибки rotation содержат пути, но не данные backup, и не влияют на target.
8. Automatic rollback transaction использует backup текущей операции напрямую и не переопределяет выбор через повторный `Select`. Manual rollback подключён в `cmd/rollback.go`: выбор копии выполняется до safety backup через positional ID или `--restore` (по умолчанию — newest), подтверждение с diff обязательно (non-TTY требует `--yes`), а `--dry-run` показывает выбранную копию и diff без создания safety copy и без записи target.

## Лицензия

- [MIT License — English](LICENSE.en)
- [Лицензия MIT — русский перевод](LICENSE.ru)

Русский текст является неофициальным переводом; юридически приоритетной считается английская версия.
