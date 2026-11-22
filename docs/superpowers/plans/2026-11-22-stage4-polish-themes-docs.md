# Этап 4: подсветка, темы, техдолг, man, CHANGELOG, примеры, документация разработчика

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Закрыть остаток спецификации fstab-editor.md: (1) подсветка синтаксиса записей и цветовая подсветка ошибок/предупреждений (ТЗ §5.3), (2) темы `light` и `high-contrast` с выбором через конфиг (ТЗ §5.5, §13), (3) техдолг TUI из defer-списков этапов 2–3, (4) man-страница `fstab-edit.1` (ТЗ «man-страница»), (5) `CHANGELOG.md` (Keep a Changelog), каталог `examples/` и документация для разработчиков (ТЗ «Примеры конфигов», «Документация для разработчиков»).

**Architecture:** Подсветка — новые semantic-стили в `tui/theme.go` (цвета полей: source/mount/fstype/options; Severity-стили: error/warning), ячейки таблицы рендерятся отдельными lipgloss-стилями с фиксированной шириной; диагностики валидатора кэшируются в модели (map физ.строка → наихудшая severity) и пересчитываются на load/mutation/undo. Темы — палитры light/high-contrast рядом с dark/monochrome; выбор через `tui.theme` в YAML (уже резолвится в `config.Settings.TUITheme`), кастомные палитры через конфиг — defer (ТЗ §13 фиксирует только выбор встроенной темы). man — ручной groff-файл docs/man/fstab-edit.1 (английский, стандарт man) + `make install`.

**Tech Stack:** Go 1.22, существующие bubbletea/lipgloss; новых зависимостей нет.

## Global Constraints

- ТЗ §5.3: подсветка синтаксиса (разные цвета для разных полей записи); подсветка ошибок и предупреждений (красный — ошибка, жёлтый — предупреждение). Текстовые маркеры (`!`, `?`, `*`) сохраняются во всех темах (этап 2, ТЗ §9.6).
- ТЗ §5.5/§13: встроенные темы dark (по умолчанию), light, high-contrast, monochrome; выбор через `tui.theme` в конфиге. Кастомные палитры (переопределение отдельных цветов в YAML) — defer.
- ТЗ §5.3 «Предпросмотр итоговой строки перед сохранением» — уже закрыт формой (preview + diff), в этап 4 не входит.
- ТЗ: man-страница fstab-edit.1; CHANGELOG.md (Keep a Changelog); примеры конфигов в examples/; документация для разработчиков.
- Бизнес-логика не зависит от TUI; обратных импортов из internal в tui новых нет.
- Все строки интерфейса — каталоги RU/EN (`tui.*`); коды/severity не локализуются.
- Тесты: только tempdir и fake seams, без реального /etc/fstab; teatest для TUI-потоков.
- Каждая задача: `go test ./...`, `go vet ./...`, `golangci-lint run` чисто; коммит после задачи; review каждой задачи (фиксы при Critical/Important, re-review); финальный whole-branch review перед merge в dev.
- Решение по трактовке «кастомизации через конфиг» (ТЗ §5.5): выбор встроенной темы через `tui.theme` (схема §13) — это и есть кастомизация; переопределение отдельных цветов — defer.

## File Structure

- Modify: `tui/theme.go` — стили полей (FieldSource/FieldMount/FieldFSType/FieldOptions) и Severity-стили (Error/Warning); палитры light/high-contrast; тесты theme_test.go.
- Modify: `tui/table.go` — ячеечный рендер с подсветкой полей; колонка состояния по диагностикам (error красный `!`, warning жёлтый `?`).
- Modify: `tui/state.go` / `tui/app.go` — кэш диагностик (map[line]Severity), пересчёт на load/mutation/undo.
- Modify: `tui/screens.go`, `tui/save.go` — Severity-стили для статусов (где применимо).
- Modify: `internal/config/config.go` — валидация `tui.theme` ∈ {dark, light, high-contrast, monochrome}; тесты.
- Modify: `tui/app.go`, `tui/table.go`, `tui/blockdev.go`, `tui/history.go` — техдолг (ctrl+c, i на loadErr, r на B, restore-confirm).
- Modify: `internal/cliapp/` — маппинг FailureRollback → exit 7 (проверить факт, починить если подтвердится).
- Create: `docs/man/fstab-edit.1` (groff, английский); Modify: `Makefile` — `install` (PREFIX, бинарь + man), проверка `man --warnings` / `groff -man -z`.
- Create: `CHANGELOG.md` (Keep a Changelog, v1.0/v1.2), `examples/config-minimal.yaml`, `examples/config-full.yaml`, `docs/DEVELOPMENT.md`.
- Modify: `README.md` — темы, man, CHANGELOG, examples, ссылки.
- Modify: `internal/i18n/catalog_ru.go`, `catalog_en.go` — новые ключи парно.

## Общие типы (контракты между задачами)

```go
package tui

// Theme дополняется semantic-стилями подсветки (все темы, включая monochrome:
// в monochrome они совпадают с базовым стилем, подсветка — только маркерами).
type Theme struct {
    // ... существующие стили ...
    // Поля записи для подсветки синтаксиса (ТЗ §5.3).
    FieldSource  lipgloss.Style
    FieldMount   lipgloss.Style
    FieldFSType  lipgloss.Style
    FieldOptions lipgloss.Style
    // Severity-стили: ошибка — красный, предупреждение — жёлтый.
    SevError   lipgloss.Style
    SevWarning lipgloss.Style
}

// severityByLine — кэш диагностик: физическая строка → наихудшая severity.
// Пересчитывается на load/mutation/undo; рендер таблицы только читает.
```

## Task 1: подсветка синтаксиса и ошибок/предупреждений

- [ ] `tui/theme.go`: стили FieldSource/FieldMount/FieldFSType/FieldOptions/SevError/SevWarning для всех 4 тем (dark/light/high-contrast/monochrome; в monochrome — без цвета, подсветка маркерами как раньше).
- [ ] `tui/state.go`/`tui/app.go`: кэш severityByLine (validator.Validate(document) → map[line]worst severity), пересчёт при загрузке, applyOp, undo, reloadFromDisk.
- [ ] `tui/table.go`: ячеечный рендер (каждая колонка своим Field-стилем, ширина через lipgloss Width, Selected поверх строки); колонка состояния: error → SevError "!", warning → SevWarning "?", иначе "+" / "-" как раньше. Заголовок без изменений.
- [ ] Форма: preview-строка с подсветкой полей (source/mount/fstype/options разными стилями) — если не усложняет рендер; иначе defer (зафиксировать в отчёте).
- [ ] Тесты: кэш severityByLine (error побеждает warning, пересчёт после undo), рендер таблицы содержит стилизованные ячейки (через lipgloss-рендер строки, не ANSI-разбор), teatest-регрессия существующих сценариев.
- Коммит: `feat: add syntax and severity highlighting to table`

## Task 2: темы light и high-contrast

- [ ] `tui/theme.go`: палитры light и high-contrast (полные наборы semantic-стилей; high-contrast — усиленные цвета/яркость для плохого зрения/проектора).
- [ ] `internal/config/config.go`: валидация tui.theme ∈ {dark, light, high-contrast, monochrome} (неизвестное значение → ошибка конфигурации с кодом, как у прочих невалидных значений); тесты.
- [ ] `tui/...`: резолв темы по Settings.TUITheme (точка входа tui.Run/Options.Theme) — проверить, что выбор доезжает; i18n/help упоминания.
- [ ] Тесты: новые темы рендерятся (viewTable не падает, стили непустые), невалидная тема в YAML → ошибка конфигурации.
- Коммит: `feat: add light and high-contrast themes`

## Task 3: техдолг TUI и exit-код rollback

- [ ] ctrl+c на подэкранах = Esc (все update* экранов: diff, mounts, blockdev, history, log, help, save-result; на главной — прежний quit-flow).
- [ ] 'i'/Ins на loadErr-экране не открывает форму (document не загружен; статус-подсказка).
- [ ] Restore-confirm в истории: при dirty-сессии текст предупреждает, что несохранённые изменения будут потеряны (отдельный i18n-ключ, подставляется вместо/дополнением к history_confirm_restore).
- [ ] Клавиша 'r' на экране B — перечитывание списка устройств.
- [ ] `internal/cliapp`: проверить маппинг FailureRollback → exit code; если сейчас 6 (io) вместо 7 (rollback) — починить и добавить тест.
- [ ] Тесты на каждый пункт; teatest для ctrl+c на подэкране и r на B.
- Коммит: `fix: subscreen ctrl+c, restore warning, blockdev reload and rollback exit code`

## Task 4: man-страница + install

- [ ] `docs/man/fstab-edit.1` (groff man 1, английский): NAME, SYNOPSIS, DESCRIPTION, COMMANDS (все 11), GLOBAL OPTIONS, CONFIGURATION (YAML-ключи), FILES (config paths, log, backup dir), EXIT STATUS (коды 0–8), EXAMPLES, SEE ALSO. Свёрстан по образцу quality man-страниц (.TH, .SH, .B/.I, .TP списки).
- [ ] `Makefile`: цель `install` — PREFIX?=/usr/local: install -D бинаря в $(PREFIX)/bin и man-страницы в $(PREFIX)/share/man/man1 (с gzip). Проверка синтаксиса: `groff -man -z docs/man/fstab-edit.1` без ошибок (или man --warnings).
- [ ] README: раздел установки — make install, man fstab-edit.
- Коммит: `docs: add man page and install target`

## Task 5: CHANGELOG, examples, документация разработчика

- [ ] `CHANGELOG.md` — Keep a Changelog (Keepachangelog.com 1.1): заголовки [1.2], [1.0], типы Added/Changed/Fixed; содержимое — сверить с git log v1.0..v1.2 и релизными заметками.
- [ ] `examples/config-minimal.yaml` (только изменённые от дефолта ключи, с комментариями RU) и `examples/config-full.yaml` (все ключи, с пояснением каждого).
- [ ] `docs/DEVELOPMENT.md` (RU): архитектура (слои: parser/models/editor/validator/transaction/backup/sysutil/cliapp/cmd/tui и направление зависимостей), как собрать и протестировать, стандарты кода (русские комментарии, TDD, review-workflow), релизный процесс (dev → main, git describe, gh release).
- [ ] README: ссылки на CHANGELOG.md, examples/, docs/DEVELOPMENT.md; examples/ вернуть в структуру проекта.
- Коммит: `docs: add changelog, config examples and development guide`

## Task 6: финальный whole-branch review и merge

- [ ] Финальный review ветки: критерии ТЗ §5.3/§5.5 закрыты, man валиден, CHANGELOG сверен, i18n парность, тесты, vet/lint/gofmt, сборка.
- [ ] Фиксы → re-review → merge fast-forward в dev → post-merge проверки → удалить worktree/branch → push origin dev. Релиз v1.3 — по решению владельца после merge.

## Deferred (в merge не блокирует)

- Кастомные палитры: переопределение отдельных цветов темы через YAML.
- Описания назначения опций в подсказках формы.
- Английская версия руководства (user-guide.en.md).
- Перфоманс: updateScrollable O(n), loadLog читает файл целиком, синхронный restore/lsblk.
- Подсветка preview-строки в форме (если отложено в Task 1).
- .deb/.rpm/.pkg.tar.zst пакеты (ТЗ: «может быть добавлена в будущих версиях»).
