# Этап 5: CI, PARTUUID/PARTLABEL, пользовательские темы

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Четыре направления: (1) CI на GitHub Actions (lint → тесты с race → порог покрытия 80% → статическая сборка; релизный workflow по тегу с бинарём и SHA-256 — критерии приёмки v2 №11/13), (2) автоподстановка PARTUUID/PARTLABEL и обработка повторяющихся идентификаторов (ТЗ v2 этап 3), (3) пользовательские темы — переопределение цветов встроенной палитры через YAML (ТЗ v2 этап 4), (4) английская документация: основной README на английском с оглавлением, README.ru.md, user-guide.en.md (PDF обеих версий).

**Проверка ранее предложенных пунктов (уже реализовано, в этап 5 НЕ входит):**
- SELinux/xattrs: `internal/writer` переносит все xattrs (flistxattr/fgetxattr/fsetxattr) через `copyMetadata` (metadata.go) при atomic replace — ТЗ v2 §6.5 покрыто на уровне кода; enforcing-тесты — в VM-этапе.
- Таймауты: `sysutil.Runner` уже имеет `Timeout` (DefaultTimeout 10 с) для всех внешних утилит, включая lsblk/blkid.
- Fuzz-тесты: `internal/parser/fuzz_test.go` (FuzzRoundTrip, FuzzEncodeField) и `internal/validator/fuzz_test.go` уже существуют.

**Architecture:** CI — два workflow (ci.yml на push/PR, release.yml на теги `v*`); порог покрытия проверяется скриптом по `go tool cover -func`. PARTUUID/PARTLABEL — расширение `sysutil.BlockDevice` и `lsblk --output`; логика выбора идентификатора в `tui/complete.go`. Пользовательские темы — новый опциональный блок `tui.colors` в YAML поверх `tui.theme`: переопределение ролей палитры; резолв в `tui.LoadTheme`.

**Tech Stack:** Go 1.22, существующие зависимости; GitHub Actions (actions/setup-go, golangci/golangci-lint-action); новых Go-зависимостей нет.

## Global Constraints

- ТЗ v2 критерии приёмки: №11 (покрытие ≥80%, CI и линтер успешны), №13 (статический бинарник linux/amd64, документация, SHA-256).
- ТЗ v2 этап 3: «явная автоподстановка UUID, PARTUUID, LABEL и PARTLABEL», «обработка отсутствующих и повторяющихся идентификаторов».
- ТЗ v2 этап 4: «темы light, high-contrast и пользовательские темы».
- CI-задачи не выполняют root-операций: тесты уже работают на tempdir/fake-seams; CI не должен трогать реальный /etc/fstab.
- i18n: новые строки — парно RU/EN (tui-ключи через Tr/Trf без префикса).
- Тесты: только tempdir и fake-seams, без реальных утилит.
- Каждая задача: `go test ./...`, `go vet ./...`, `golangci-lint run` чисто; коммит после задачи; review каждой задачи (фиксы при Critical/Important → re-review); финальный whole-branch review перед merge в dev.
- Релиз v1.4 — по решению владельца после merge этапа 5.

## File Structure

- Create: `.github/workflows/ci.yml` — линт/тесты/race/покрытие/сборка на push и PR в main и dev.
- Create: `.github/workflows/release.yml` — по тегу `v*`: статическая сборка, SHA-256, GitHub Release с бинарём и `SHA256SUMS`.
- Modify: `internal/sysutil/blockdev.go` — BlockDevice + PARTUUID/PARTLABEL; lsblk --output расширение; тесты/fixtures.
- Modify: `tui/complete.go` — `deviceHints`: приоритет UUID → PARTUUID → LABEL → PARTLABEL → Path; пометка дубликатов в Info.
- Modify: `internal/config/config.go` — `tui.colors` (map роль→цвет) + валидация ролей/формата цветов.
- Modify: `tui/theme.go` — переопределения палитры поверх встроенной темы; `tui/app.go`/`cmd/tui.go` — проброс в Options.
- Modify: `README.md` — становится английским с оглавлением; Create: `README.ru.md` — русская версия с оглавлением; Create: `docs/user-guide.en.md` — английское руководство.
- Modify: `README.md`, `docs/user-guide.md`, `docs/DEVELOPMENT.md` — CI-бейдж, релизный процесс (автоматический), автоподстановка, `tui.colors`.
- Modify: `internal/i18n/catalog_ru.go`, `catalog_en.go` — новые ключи парно (пометка дубликата).

## Общие типы (контракты между задачами)

```go
package sysutil

// BlockDevice расширяется PARTUUID/PARTLABEL (lsblk --output добавляет
// PARTUUID,PARTLABEL; пустые значения — "", как прочие поля).
type BlockDevice struct {
    // ... существующие поля ...
    PartUUID  string
    PartLabel string
}
```

```go
package tui

// Приоритет подстановки в форме (ТЗ v2): UUID → PARTUUID → LABEL → PARTLABEL → путь.
// При повторяющемся идентификаторе (одинаковый Value у >1 устройства) Info помечается
// маркером дубликата (i18n tui.hint_duplicate) — выбор остаётся, но пользователь видит,
// что идентификатор не уникален.
```

```yaml
# Пользовательские переопределения палитры (ТЗ v2, пользовательские темы):
tui:
  theme: dark            # встроенная база: dark|light|high-contrast|monochrome
  colors:                # необязательно; поверх встроенной темы
    title: "#00ffff"     # hex #rrggbb
    selected_fg: "0"     # ANSI-номер 0–255 (строка)
    error: "red"         # имя ANSI-цвета
```

## Task 1: CI и релизные артефакты

- [ ] `.github/workflows/ci.yml`: триггеры push/PR в main и dev; steps: checkout → setup-go (версия из go.mod) → golangci-lint-action → `go test -race -coverprofile=cover.out ./...` → порог: `go tool cover -func` total ≥ 80% (скрипт awk, завершается ненулевым при недоборе) → `CGO_ENABLED=0 go build -o fstab-edit .` (smoke: `./fstab-edit --version`). Опционально: fuzz-smoke `go test -run=Fuzz -fuzz=FuzzRoundTrip -fuzztime=10s ./internal/parser/` (короткий прогон как дополнительная проверка, не блокер).
- [ ] `.github/workflows/release.yml`: триггер на push тега `v*`; сборка статического бинаря с версией из тега (Makefile уже это делает через git describe — на checkout тега), `sha256sum fstab-edit > SHA256SUMS`, `softprops/action-gh-release` (или `gh release upload`/`create` через GH CLI с GITHUB_TOKEN): создать release с бинарём, SHA256SUMS и заметками из CHANGELOG-секции версии (или `--generate-notes` — выбрать простой вариант: gh release create с generate-notes + бинарём и SHA256SUMS).
- [ ] Проверка локально: `make build` и sha256sum — команды из workflow воспроизводимы; actionlint если доступен (не обязательно).
- [ ] README: бейдж CI; DEVELOPMENT.md §4: релизный процесс — автоматический release.yml, ручной gh release больше не нужен (или остаётся как запасной вариант).
- Коммит: `ci: add github actions for tests, coverage and tagged releases`

## Task 2: PARTUUID/PARTLABEL и дубликаты идентификаторов

- [ ] `internal/sysutil/blockdev.go`: поля PartUUID/PartLabel; lsblk argv расширен на PARTUUID,PARTLABEL (точный argv в тестах обновить); fixtures — устройства с PARTUUID/PARTLABEL.
- [ ] `tui/complete.go` deviceHints: приоритет UUID → PARTUUID → LABEL → PARTLABEL → Path; при повторяющемся выбранном идентификаторе среди устройств — Info получает пометку дубликата (i18n tui.hint_duplicate, RU/EN).
- [ ] Тесты: полный приоритет (устройство со всеми идентификаторами → UUID; без UUID → PARTUUID; без обоих → LABEL; только PARTLABEL → PARTLABEL; ничего → Path), дубликат UUID двух устройств → пометка, отсутствие всех → Path без пометки; teatest-регрессия формы.
- [ ] user-guide/README: перечень автоподстановки и приоритет обновить.
- Коммит: `feat: add partuuid/partlabel completion and duplicate id hint`

## Task 3: пользовательские темы (tui.colors)

- [ ] `internal/config/config.go`: `tui.colors` — map[string]string; валидация: известные роли (title, header, selected_fg, selected_bg, muted, info, dirty, error, warning, field_source, field_mount, field_fstype, field_options — зафиксировать полный список по palette), форматы цветов (hex #rrggbb, ANSI-номер 0–255, имена ANSI-цветов red/green/...); неизвестная роль или невалидный цвет → отказ конфигурации (exit 2, сообщение с ролью/значением).
- [ ] `tui/theme.go`: LoadTheme применяет переопределения поверх выбранной встроенной темы (role → lipgloss.Color); monochrome + colors: цвета применяются (пользователь явно попросил) — задокументировать.
- [ ] Проброс: config.Settings → cmd/tui.go → tui.Options (ThemeOverrides) → LoadTheme.
- [ ] Тесты: переопределение одной роли меняет только её; невалидная роль/цвет → ошибка; hex/номер/имя всех форматов; monochrome+colors.
- [ ] user-guide/README: раздел тем — пользовательские переопределения с примером; examples/config-full.yaml — закомментированный блок colors с примером.
- Коммит: `feat: support custom palette overrides via tui.colors`

## Task 4: английская документация и оглавление README

Решение владельца: основной README — английский; русская версия — отдельным файлом. Man-страница уже английская (стандарт man) — без изменений, только сверка актуальности.

- [ ] `README.md` → **английский** (полный перевод текущего содержимого: статус этапов, установка, CLI-таблица, TUI, конфигурация, журнал, backup/rollback, примеры, структура проекта, ссылки, лицензия); **оглавление** (Table of Contents) со ссылками на все разделы H2; ссылка-переключатель на русскую версию в начале.
- [ ] `README.ru.md` — русская версия (текущее содержимое + те же обновления статуса этапа 5), тоже с оглавлением; ссылка-переключатель на английскую. Оба README синхронизированы по содержимому (проверка разделов паритетом).
- [ ] `docs/user-guide.en.md` — полный английский перевод руководства пользователя (все разделы §1–§10; терминология консистентна с man). Русский user-guide.md остаётся; ссылки-переключатели в начале обоих.
- [ ] `Makefile`: `pdf` собирает оба PDF — `docs/user-guide.pdf` (RU) и `docs/user-guide.en.pdf` (EN); `.gitignore` покрывает оба.
- [ ] `README` ссылки: user-guide RU/EN, man, spec v1/v2 — проверить валидность всех.
- [ ] AGENTS.md и DEVELOPMENT.md: правило «документация RU» уточнить — README и user-guide двуязычные (EN основной README), остальная документация RU.
- Коммит: `docs: english readme and user guide with toc`

## Task 5: финальный whole-branch review и merge

- [ ] Финальный review: CI-файлы валидны (actionlint или ручная проверка YAML), критерии ТЗ v2 №11/13 закрыты, i18n парность, тесты, vet/lint/gofmt, сборка; паритет README/README.ru.md и user-guide RU/EN (разделы соответствуют), оглавление работает (якоря GitHub).
- [ ] Фиксы → re-review → merge fast-forward в dev → post-merge проверки → удалить worktree/branch → push origin dev. Первый прогон CI на GitHub — проверить фактический статус workflow после push (gh run list / gh run watch).

## Deferred (в merge не блокирует)

- `apply-mounts` (ТЗ v2 этап 4): явная команда монтирования с таймаутом и отчётом — отдельный этап 6.
- QEMU/VM-матрица и SELinux enforcing-тесты (критерии №14/15) — инфраструктурный этап.
- Автоматическое извлечение заметок релиза из CHANGELOG (release.yml использует generate-notes).
- Описания опций в подсказках; пакеты .deb/.rpm.
