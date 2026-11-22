# AGENTS.md — fstab-edit

## Проект

`fstab-edit` — Go-утилита безопасного редактирования `/etc/fstab`: CLI (Cobra) для
скриптинга + TUI (bubbletea) для интерактивной работы. Транзакционная запись,
статическая проверка через `findmnt --verify`, резервные копии со sidecar-metadata,
автоматический rollback, локализация RU/EN.

- **Язык:** Go 1.22+ (toolchain в go.mod). **Лицензия:** MIT (LICENSE.ru / LICENSE.en).
- **Дистрибуция:** один статически слинкованный бинарь (linux/amd64).
- **Разработчик:** Сергей (Sergey).

## Карта каталога fstab-editor/

| Путь | Что это |
|------|---------|
| `fstab-editor.md` | Исходная спецификация (ТЗ 1.0) — также в репозитории: `work/docs/spec/fstab-editor.md` |
| `fstab-editor-v2.md` | Актуальная спецификация (ТЗ 2.0, основа разработки) — также в репозитории: `work/docs/spec/fstab-editor-v2.md` |
| `work/` | Git-репозиторий проекта — вся разработка здесь |

## Репозиторий (work/)

- Ветки: `main` — только продукт (релизные теги); `dev` — рабочая ветка
  (продукт + docs/, включая планы разработки `docs/superpowers/plans/`).
- Релизы: **v1.0** (2026-11-21, ядро+CLI), **v1.2** (2026-11-22, TUI + устройства +
  автодополнение + руководство), **v1.3** (2026-11-22, подсветка + темы light/high-contrast +
  техдолг + man + CHANGELOG + examples).
- Worktree'ы этапов создаются как `.worktrees/stage-N` на время работы и удаляются
  после merge. GitHub: `github.com/SergeyPoleckiy/fstab-edit`.

## Команды (cwd = work/)

```bash
make            # сборка ./fstab-edit (версия из git describe через -ldflags)
make test       # go test ./...
make install    # бинарь + man-страница (gzip) в $(DESTDIR)$(PREFIX), PREFIX=/usr/local
make uninstall  # обратная операция
make pdf        # docs/user-guide.md → PDF (pandoc + xelatex, шрифты Free*)
go vet ./... && golangci-lint run && gofmt -l .   # обязательные проверки
```

## Архитектура

Направление зависимостей: `cmd → tui → internal/* → pkg/models`
(`cmd` — wiring поверх обоих интерфейсов). Подробности — `docs/DEVELOPMENT.md`.

```
cmd/            # Cobra: list add edit delete comment uncomment validate verify-mount backup rollback tui
tui/            # bubbletea: таблица, формы, экраны (diff/монтирования/устройства B/история/журнал/помощь),
                # подсветка синтаксиса и severity, 4 темы, автодополнение (Ctrl+N/Ctrl+P)
internal/
  parser/       # lossless-парсер fstab (побайтовое сохранение, комментарии)
  editor/       # доменные мутации: Add/Edit/Delete/Comment/Uncomment + селекторы
  validator/    # валидация полей; KnownFSTypes — единый перечень типов ФС (ТЗ §6.2)
  writer/       # flock, SHA-256, atomic replace
  transaction/  # оркестрация записи: backup → findmnt → replace → verify → auto-rollback
  backup/       # копии со sidecar JSON (backup_id, sha256, operation), ротация (keep 10)
  sysutil/      # CommandRunner (seam), findmnt-verifier, lsblk/blkid-парсеры
  config/       # YAML: fstab_path, backup.*, log.*, tui.theme; security-checks
  logger/       # JSON Lines-журнал, rotation, redaction
  i18n/         # каталоги RU/EN (парность проверяет TestCatalogParity)
  cliapp/       # сервисный слой CLI, exit codes 0–8, интерактивный chooser
  diff/ output/ # unified diff и форматы table/json/csv/tsv
pkg/models/     # модель данных (Entry, Node, Diagnostic, Severity)
```

## Ключевые решения

- Запись в target — ТОЛЬКО через `transaction.Execute` (CLI и TUI, отдельной
  небезопасной записи в TUI нет); restore — через `cliapp.Service.Rollback`.
- TUI не запускается без TTY (usage error, exit 2); без аргументов открывает TUI.
- Интерактивный chooser при неоднозначном селекторе (код 8 при отказе/вне TTY с --yes).
- Темы: dark (default), light, high-contrast, monochrome; выбор через `tui.theme`
  (валидация значения, невалидное → отказ конфигурации, exit 2).
- Версия бинаря — только через `git describe` (Makefile -ldflags); ручной ввод не поддерживается.

## Правила разработки

- **Все комментарии, сообщения тестов и документация — на русском** (man-страница —
  английский по стандарту man). Код максимально прокомментирован.
- Новые строки интерфейса — только через каталоги i18n, ключи парно RU/EN
  (tui-ключи вызывать через Tr/Trf без ручного префикса `tui.`).
- Тесты: только `t.TempDir()` и fake-seams (`sysutil.CommandRunner`, `tui.Deps`);
  НИКОГДА не читать/писать реальный `/etc/fstab` и не выполнять реальные
  lsblk/blkid/findmnt из тестов. TUI-флоу — через `teatest`.
- Workflow задачи: реализация → review (фиксы при Critical/Important) → re-review →
  по готовности этапа финальный whole-branch review → merge fast-forward в `dev` →
  post-merge прогоны → удалить worktree/ветку → `git push origin dev`.
- Релиз: merge dev → main (предпочтительно ff; при расхождении — с разрешением),
  убрать из main `docs/superpowers/` (только продукт), тег `vX.Y`, `git push origin main`
  и тег, `make build`, `gh release create` с бинарём и заметками, перенести
  `[Unreleased]` → `[X.Y]` в CHANGELOG.md. Полный чек-лист — `docs/DEVELOPMENT.md` §4.

## Отложено (defer, не блокирует релизы)

Кастомные палитры цветов через YAML; английская версия руководства (user-guide.en.md);
описания назначения опций в подсказках формы; пакеты .deb/.rpm/.pkg.tar.zst;
перфоманс (кэш рендера таблицы, updateScrollable O(n), loadLog целиком,
синхронные lsblk/restore без таймаута).

## Секреты

В `~/projects` (корне) лежат файлы с токенами (`fstab-editor-github-token.txt`,
`key-aitunnel.txt` и др.). Не читать, не выводить, не коммитить.
