# Этап 3: блочные устройства, автодополнение, PDF-руководство

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Этап 3 плана разработки: (1) экран блочных устройств `B` (lsblk) в TUI, (2) автодополнение в форме записи — устройство из списка lsblk с автоподстановкой UUID=/LABEL= (blkid), тип ФС из поддерживаемого перечня, опции монтирования контекстно от типа ФС, (3) руководство пользователя `docs/user-guide.md` со сборкой в PDF через `make pdf`.

**Architecture:** Парсеры `lsblk --json` и `blkid -o export` — в `internal/sysutil` (рядом с findmnt-verifier, через injectable `sysutil.CommandRunner`). Список поддерживаемых типов ФС становится экспортируемым (`validator.KnownFSTypes`) — единый источник для валидации и автодополнения, дополненный недостающими типами из ТЗ §6.2. TUI получает новый seam `Deps.ListBlockDevices`; экран `B` и подсказки под полем устройства идут через него (тесты — fake runner + tempdir). Автодополнение — панель подсказок под активным полем формы: Ctrl+N/Ctrl+P листают варианты, префикс ввода фильтрует список. PDF — Markdown-источник + `make pdf` (pandoc + xelatex, шрифты FreeSerif/FreeSans).

**Tech Stack:** Go 1.22 (toolchain 1.27), существующий `sysutil.CommandRunner`; новых Go-зависимостей нет. Документация: pandoc 3.11 + xelatex (уже установлены).

## Global Constraints

- ТЗ §5.3: выбор устройства из списка доступных блочных устройств (lsblk/blkid); выпадающие списки типа ФС из поддерживаемого перечня; автодополнение опций монтирования (контекстно-зависимое, с учётом типа ФС).
- ТЗ §5.4: экран «Блочные устройства» — список устройств (lsblk), горячая клавиша `B`.
- ТЗ §6.2 — полный перечень типов ФС: локальные (ext2, ext3, ext4, xfs, btrfs, f2fs, reiserfs), Windows-совместимые (ntfs, ntfs-3g, vfat, exfat, msdos), оптические/съёмные (iso9660, udf), сетевые (nfs, nfs4, cifs, smbfs, sshfs), псевдо-ФС (tmpfs, proc, sysfs, devpts, swap), специальные (overlay, bind, auto).
- ТЗ п.10: автоподстановка UUID/LABEL через blkid при добавлении записи.
- ТЗ §6.3: популярные опции — defaults, noatime, nofail, ro, rw, user, auto, noauto, exec, noexec, sync, async, relatime, nodev, nosuid и др.; строгая валидация опций в зависимости от типа ФС (discard не для всех ФС) — на уровне подсказок: опция не предлагается для типа, где она бессмысленна.
- Решения владельца (зафиксированы перед планом): выбор устройства — подсказки под полем в форме (не отдельный экран-вставка); PDF — Markdown + `make pdf`, без CI.
- Бизнес-логика не зависит от TUI: `internal/sysutil` парсит, TUI отображает; обратных импортов из `internal/*` в `tui` нет (tui → internal разрешён, как раньше).
- Все строки интерфейса — через каталоги RU/EN (`internal/i18n`, namespace `tui.*`); коды/severity не локализуются.
- Тесты не выполняют реальные lsblk/blkid и не читают реальный `/etc/fstab`: только fake `sysutil.CommandRunner` с fixture-выводом и `t.TempDir()`.
- Каждая задача: `go test ./...`, `go vet ./...`, `golangci-lint run` чисто; коммит после задачи. Порядок: Task 1 → 4 последовательно. Каждая задача — review (см. workflow этапа 2), фиксы при Critical/Important, re-review; финальный whole-branch review перед merge в `dev`.

## File Structure

- Modify: `internal/sysutil/` — новый `blockdev.go` (BlockDevice, ListBlockDevices, ProbeDevice) + `blockdev_test.go`.
- Modify: `internal/validator/entry.go` — экспортируемый `KnownFSTypes` (полный перечень ТЗ §6.2 + существующие), `validateFSType` ходит по нему.
- Modify: `tui/deps.go` — seam `ListBlockDevices`.
- Create: `tui/blockdev.go` — экран `B` (ScreenBlockDevices) + view + update + i18n-ключи.
- Modify: `tui/app.go` (навигация экрана B), `tui/table.go` (клавиша `B`), `tui/screens.go` (viewHelp + keys_table).
- Modify: `tui/form.go` — панель подсказок (устройство/тип ФС/опции), клавиши Ctrl+N/Ctrl+P, формат подстановки UUID=/LABEL=/путь.
- Create: `tui/complete.go` — фильтрация/ранжирование вариантов (чистая логика, unit-тесты) + `complete_test.go`.
- Modify: `internal/i18n/catalog_ru.go`, `catalog_en.go` — новые ключи `tui.*` (инвентарь в задачах).
- Create: `docs/user-guide.md` — руководство пользователя (RU, детальное).
- Modify: `Makefile` — цель `pdf` (pandoc + xelatex + шрифты FreeSerif/FreeSans, `-V lang=ru-RU`).
- Modify: `README.md` — статус этапа 3, клавиша `B`, автодополнение, ссылка на руководство.
- Tests: `internal/sysutil/blockdev_test.go`, `tui/complete_test.go`, `tui/blockdev_test.go`, расширение `tui/form_test.go`, `tui/app_test.go` (teatest).

## Общие типы (контракты между задачами)

```go
package sysutil

// BlockDevice — одна строка lsblk: идентификация, размер, ФС, метки.
// Пустые поля (неприменимо/не заполнено) — ""; UUID/LABEL — из lsblk
// (без отдельного вызова blkid; blkid используется для устройств, где
// lsblk не показал UUID/LABEL — ProbeDevice).
type BlockDevice struct {
    Name       string // sda1
    Path       string // /dev/sda1
    Type       string // disk, part, lvm, crypt, rom, loop
    FSType     string
    Size       string // как выдал lsblk (17.4G) — без пересчёта
    MountPoint string
    Label      string
    UUID       string
    Model      string
    Children   []BlockDevice // вложенные (partitions of disk, lv of vg)
}

// ListBlockDevices возвращает дерево блочных устройств через
// `lsblk --json --output NAME,PATH,TYPE,FSTYPE,SIZE,MOUNTPOINT,LABEL,UUID,MODEL`.
// Ненулевой exit code — ошибка; malformed JSON — ошибка.
func ListBlockDevices(ctx context.Context, runner CommandRunner) ([]BlockDevice, error)

// ProbeDevice возвращает карту свойств устройства через `blkid -o export <path>`
// (DEVNAME, UUID, LABEL, TYPE, PARTUUID, ...). Устройство без ФС — пустая карта
// без ошибки (blkid exit 2 по spec: "no attributes"? на практике exit 0/1/2
// обрабатываем: код 2 = не найдено свойств, не ошибка).
func ProbeDevice(ctx context.Context, runner CommandRunner, path string) (map[string]string, error)
```

```go
package validator

// KnownFSTypes — полный перечень поддерживаемых типов ФС (ТЗ §6.2 плюс
// существующие в валидаторе: ntfs3, devtmpfs, cgroup, cgroup2, squashfs,
// fuse, devpts, sshfs, overlay...). Единый источник для валидации
// (validateFSType) и автодополнения в TUI; форма не дублирует список.
var KnownFSTypes = []string{...}
```

```go
package tui

// Deps дополняется одним seam:
// ListBlockDevices возвращает дерево устройств для экрана B и подсказок формы.
ListBlockDevices func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error)

// deviceHint — подсказка для поля устройства: что подставить и что показать.
type deviceHint struct {
    Value string // подставляемое значение: UUID=<uuid> | LABEL=<label> | /dev/...
    Info  string // правая колонка: TYPE FSTYPE SIZE [MOUNTPOINT]
}

// formModel дополняется:
// hints []string   — текущие варианты подстановки для активного поля
// hintIdx int      — выбранный индекс (-1 — ничего)
```

## Task 1: sysutil — парсеры lsblk/blkid + единый перечень типов ФС

- [ ] `internal/sysutil/blockdev.go`: `BlockDevice`, `ListBlockDevices` (lsblk --json, дерево через `children`), `ProbeDevice` (blkid -o export, exit 2 = пустая map без ошибки), flatten-помощник `Flatten(devs)` (устройства экрана B — плоский список в порядке lsblk).
- [ ] `internal/validator/entry.go`: экспортируемый `KnownFSTypes` (ТЗ §6.2 + существующие типы), `validateFSType` идёт по нему; deprecated smbfs и `fuse.*` — прежние исключения.
- [ ] Тесты `blockdev_test.go`: fixture-JSON (диск с partitions, LVM, null-поля), malformed JSON, ненулевой exit code; `ProbeDevice` с UUID/LABEL и с exit 2; `Flatten` порядок. Тест `KnownFSTypes` содержит все типы ТЗ §6.2.
- **i18n:** не требуется (пакет sysutil/validator без строк интерфейса).
- Проверки: `go test ./...`, `go vet ./...`, `golangci-lint run` чисто.
- Коммит: `feat: add lsblk/blkid parsers and shared known fs types`

## Task 2: экран блочных устройств (B)

- [ ] `tui/deps.go`: seam `ListBlockDevices`; `ProductionDeps` — `sysutil.ListBlockDevices`.
- [ ] `tui/blockdev.go`: `ScreenBlockDevices`, загрузка при входе (через seam, ошибка — экран с load_error-статусом, повторный вход перечитывает), view — плоская таблица: PATH, TYPE, FSTYPE, SIZE, MOUNTPOINT, LABEL, UUID (truncate по ширине), Esc — назад. Скролл по образцу diff-экрана, если строк > высоты.
- [ ] `tui/table.go`: клавиша `B` (верхний регистр, как M/H/L); `tui/screens.go`: строка в viewHelp, `tui.keys_table` RU/EN.
- [ ] i18n-ключи (RU/EN, парно): `tui.blockdev_title`, `tui.blockdev_error`, `tui.blockdev_empty`, `tui.blockdev_header` (PATH TYPE FSTYPE SIZE MOUNT LABEL UUID), `tui.help_key_blockdev` ("B"), `tui.help_desc_blockdev`.
- [ ] Тесты `tui/blockdev_test.go`: fake runner с fixture (подмена seam, не CommandRunner — seam возвращает данные), view содержит колонки; ошибка seam — статус ошибки; teatest: `B` из таблицы → экран с устройством → Esc назад.
- Проверки и коммит: `feat: add block devices screen (B) with lsblk data`

## Task 3: автодополнение в форме (устройство, тип ФС, опции)

- [ ] `tui/complete.go`: чистая логика подсказок — `filterHints(prefix, candidates) []string` (prefix-match, регистронезависимо), `optionHints(fsType, current string) []string` (последний токен после запятой — префикс; базовый набор + per-FS исключения: discard не предлагается для nfs/cifs/sshfs/overlay и псевдо-ФС; fs-specific опции: btrfs → compress, subvol; xfs → allocsize; ext* → commit, data; nfs → rsize, wsize, timeo), `deviceHints(devs []sysutil.BlockDevice) []deviceHint` (UUID= → LABEL= → Path приоритет; Info-колонка).
- [ ] `tui/form.go`: панель подсказок под формой для полей device (0), fs_type (2), options (3); Ctrl+N/Ctrl+P листают, подстановка в textinput; префикс-фильтрация при печати; для device подстановка `UUID=<uuid>` из lsblk, fallback `LABEL=`, fallback путь; для пустого UUID у выбранного устройства — ProbeDevice (blkid) лениво при подстановке? Нет: lsblk уже отдаёт UUID/LABEL; blkid-ProbeDevice использовать, только если lsblk пуст (Task 1 API) — реализовать fallback в подстановке.
- [ ] Устройства для подсказок — тот же seam `Deps.ListBlockDevices`, загрузка при открытии формы (add/edit), ошибка/пусто — панель подсказок не показывается (форма работает как раньше).
- [ ] Тип ФС: подсказки из `validator.KnownFSTypes` (без загрузки устройств).
- [ ] i18n-ключи (RU/EN, парно): `tui.hints_keys` ("Ctrl+N/Ctrl+P — листать подсказки"), `tui.hint_no_devices`? (нет — при недоступности lsblk панель скрыта), `tui.form_hint_fs`? Не требуется: панель без заголовка, варианты сами говорящие. Минимум: `tui.hints_keys`.
- [ ] Тесты `tui/complete_test.go`: filterHints (префикс, регистр, пустой префикс → все), optionHints (токенизация, discard исключён для nfs, btrfs compress предложен), deviceHints (приоритет UUID>LABEL>Path, Info-формат); `tui/form_test.go`: подстановка Ctrl+N в поле device/типа/опций, фильтрация при вводе; teatest в `tui/app_test.go`: Ins → форма → Ctrl+N в поле устройства → подставился UUID=.
- Проверки и коммит: `feat: add completion hints for device, fs type and options`

## Task 4: руководство пользователя + PDF

- [ ] `docs/user-guide.md` (RU): назначение, установка (make build / готовый бинарник), быстрый старт, полный CLI (все команды с примерами), TUI (экраны, все клавиши, автодополнение, темы), конфигурация YAML, журнал, backup/rollback и восстановление, диагностика ошибок (коды выхода), FAQ. Скриншоты не требуются (терминальная утилита).
- [ ] `Makefile`: цель `pdf`: `pandoc docs/user-guide.md -o docs/user-guide.pdf --pdf-engine=xelatex -V lang=ru-RU -V mainfont="FreeSerif" -V sansfont="FreeSans" -V monofont="FreeMono" --toc --metadata title="fstab-edit — руководство пользователя"`. Цель `docs: pdf`. Проверить сборку фактическим запуском (xelatex в TZ с кириллицей).
- [ ] `README.md`: статус этапа 3, клавиша `B`, автодополнение, ссылка `docs/user-guide.md` (PDF собирается `make pdf`).
- [ ] `.gitignore`: `docs/user-guide.pdf` — артефакт сборки, не исходник? Решение: PDF коммитить НЕ будем (собирается make pdf); добавить в `.gitignore` с комментарием.
- [ ] Проверки: `make pdf` собирает без ошибок; `go test ./...` по-прежнему зелёный (код не меняется, кроме README/Makefile).
- Коммит: `docs: add user guide with pdf build target`

## Task 5: финальный whole-branch review и merge

- [ ] Финальный review всей ветки (аналог этапа 2): критерии ТЗ §5.3/§5.4/§6.2/п.10 покрыты, i18n парность, тесты, vet/lint/gofmt, сборка.
- [ ] Фиксы по review → re-review → merge fast-forward в `dev` → post-merge проверки → удалить worktree/branch → `git push origin dev`.

## Deferred (в merge не блокирует)

- Перечитывание списка устройств по клавише на экране B (сейчас — при каждом входе на экран).
- Автодополнение опций с документацией назначения опции (сейчас — только список).
- Английская версия руководства (user-guide.en.md).
- Экран B: детали выбранного устройства (blkid properties).
