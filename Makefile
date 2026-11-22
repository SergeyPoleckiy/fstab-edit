# Makefile для сборки fstab-edit.
# Версия вычисляется из git автоматически (git describe): сборка из чистого
# тега даёт "v1.0", из коммитов после тега — "v1.0-3-ga1b2c3d", из грязного
# дерева — с суффиксом "-dirty". Ручной ввод версии не требуется и не
# поддерживается: версия отражает реальное состояние репозитория.

# VERSION — строка версии для -ldflags; fallback "dev" вне git-репозитория.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# LDFLAGS передаёт версию в main.version (переменная объявлена в main.go).
LDFLAGS := -X main.version=$(VERSION)

# BINARY — имя собираемого бинарника.
BINARY := fstab-edit

# PREFIX — базовый каталог установки; DESTDIR — staging-каталог (для пакетов).
PREFIX ?= /usr/local
DESTDIR ?=

MANPAGE := docs/man/fstab-edit.1

.PHONY: build install uninstall test clean version pdf docs

# build собирает статический бинарь с версией из git.
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# install собирает и устанавливает бинарь и man-страницу (с gzip, как
# принято в дистрибутивах) в $(DESTDIR)$(PREFIX). gzip идёт во временный
# файл (mktemp), чтобы обрыв не оставлял мусорный fstab-edit.1.gz в корне
# дерева и install не ставил пустую страницу.
install: build
	install -Dm755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	test -f $(MANPAGE)
	gztmp=$$(mktemp -t fstab-edit-man.XXXXXX.gz); \
	gzip -9c $(MANPAGE) > $$gztmp && \
	install -Dm644 $$gztmp $(DESTDIR)$(PREFIX)/share/man/man1/fstab-edit.1.gz; \
	status=$$?; rm -f $$gztmp; exit $$status

# uninstall удаляет установленные файлы (обратная операция install).
uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	rm -f $(DESTDIR)$(PREFIX)/share/man/man1/fstab-edit.1.gz

# test прогоняет весь набор тестов проекта.
test:
	go test ./...

# clean удаляет локально собранный бинарник.
clean:
	rm -f $(BINARY)

# version печатает версию, которую получит следующая сборка.
version:
	@echo $(VERSION)

# pdf собирает руководство пользователя (docs/user-guide.md) в PDF через
# pandoc + xelatex; требуются установленные pandoc, xelatex и шрифты GNU FreeFont.
pdf:
	pandoc docs/user-guide.md -o docs/user-guide.pdf --pdf-engine=xelatex \
		-V lang=ru-RU -V mainfont="FreeSerif" -V sansfont="FreeSans" \
		-V monofont="FreeMono" --toc \
		--metadata title="fstab-edit — руководство пользователя"

# docs — сборка всей генерируемой документации (сейчас это PDF руководства).
docs: pdf
