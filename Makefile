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

MANPAGE_EN := docs/man/fstab-edit.1
MANPAGE_RU := docs/man/fstab-edit.ru.1

.PHONY: build install uninstall test clean version pdf docs

# gzip-установка man-страницы: $(1) — исходный файл, $(2) — путь назначения
# (с суффиксом .gz). gzip идёт во временный файл (mktemp), чтобы обрыв не
# оставлял мусорный .gz и install не ставил пустую страницу.
define install_man
	test -f $(1)
	gztmp=$$(mktemp -t fstab-edit-man.XXXXXX.gz); \
	gzip -9c $(1) > $$gztmp && \
	install -Dm644 $$gztmp $(2); \
	status=$$?; rm -f $$gztmp; exit $$status
endef

# build собирает статический бинарь с версией из git.
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# install собирает и устанавливает бинарь и обе man-страницы (с gzip, как
# принято в дистрибутивах) в $(DESTDIR)$(PREFIX): английская — в
# share/man/man1, русская — в share/man/ru/man1 (man выбирает язык по локали).
install: build
	install -Dm755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	$(call install_man,$(MANPAGE_EN),$(DESTDIR)$(PREFIX)/share/man/man1/fstab-edit.1.gz)
	$(call install_man,$(MANPAGE_RU),$(DESTDIR)$(PREFIX)/share/man/ru/man1/fstab-edit.1.gz)

# uninstall удаляет установленные файлы (обратная операция install).
uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	rm -f $(DESTDIR)$(PREFIX)/share/man/man1/fstab-edit.1.gz
	rm -f $(DESTDIR)$(PREFIX)/share/man/ru/man1/fstab-edit.1.gz

# test прогоняет весь набор тестов проекта.
test:
	go test ./...

# clean удаляет локально собранный бинарник.
clean:
	rm -f $(BINARY)

# version печатает версию, которую получит следующая сборка.
version:
	@echo $(VERSION)

# pdf собирает руководство пользователя в обеих языковых версиях — RU
# (docs/user-guide.pdf) и EN (docs/user-guide.en.pdf) — через pandoc + xelatex;
# требуются установленные pandoc, xelatex и шрифты GNU FreeFont.
pdf: docs/user-guide.pdf docs/user-guide.en.pdf

docs/user-guide.pdf: docs/user-guide.md
	pandoc $< -o $@ --pdf-engine=xelatex \
		-V lang=ru-RU -V mainfont="FreeSerif" -V sansfont="FreeSans" \
		-V monofont="FreeMono" --toc \
		--metadata title="fstab-edit — руководство пользователя"

docs/user-guide.en.pdf: docs/user-guide.en.md
	pandoc $< -o $@ --pdf-engine=xelatex \
		-V lang=en-US -V mainfont="FreeSerif" -V sansfont="FreeSans" \
		-V monofont="FreeMono" --toc \
		--metadata title="fstab-edit — User Guide"

# docs — сборка всей генерируемой документации (сейчас это PDF руководства).
docs: pdf
