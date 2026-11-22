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

.PHONY: build install test clean version pdf docs

# build собирает статический бинарь с версией из git.
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# install собирает и устанавливает бинарь в /usr/local/bin.
install: build
	sudo cp $(BINARY) /usr/local/bin/$(BINARY)
	sudo chmod 755 /usr/local/bin/$(BINARY)

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
