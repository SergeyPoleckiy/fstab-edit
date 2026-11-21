// Command fstab-edit — CLI безопасного редактирования fstab: валидация,
// backup, подтверждения и rollback. main только связывает процессовые
// зависимости (потоки, runner, TTY, интерактивный chooser) с cmd.App и
// переводит exit code в код завершения процесса; вся логика — в пакетах
// cmd и internal.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"fstab-edit/cmd"
	"fstab-edit/internal/i18n"
	"fstab-edit/internal/sysutil"
)

// version — версия сборки. По умолчанию "dev" для разработческих сборок;
// release-сборки переопределяют значение через -ldflags:
//
//	go build -ldflags "-X main.version=1.0.0" -o fstab-edit .
var version = "dev"

// main собирает production App и завершает процесс с кодом, который вернула
// команда. os.Exit вызывается только здесь, чтобы тесты cmd не затрагивали
// процесс test-runner.
func main() {
	// Единственный buffered reader на stdin: его разделяют prompt-подтверждения
	// (через App) и интерактивный chooser, поэтому NewApp получает именно
	// *bufio.Reader и не создаёт второй буфер поверх него.
	stdin := bufio.NewReader(os.Stdin)
	// Язык chooser резолвится в main по env/локали — App применит тот же
	// резолв и выберет --lang из argv, если он задан; chooser без --lang
	// показывает prompt на языке окружения (приоритет ТЗ §15).
	lang := i18n.Resolve("", os.Environ(), os.Getenv("LC_ALL"), os.Getenv("LANG"))
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   stdin,
		Version: version,
		Runner:  sysutil.NewRunner(),
		TTY:     stdinIsTTY,
		Chooser: interactiveChooser{stdin: stdin, stderr: os.Stderr, lang: lang},
	})
	os.Exit(app.Execute(os.Args[1:]))
}

// stdinIsTTY сообщает, доступен ли интерактивный ввод подтверждений:
// подтверждения читают именно stdin, поэтому проверяется его терминальность.
// Терминал определяется возможностью прочитать termios: у pipe и файла
// запрос завершится ошибкой.
func stdinIsTTY() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}

// interactiveChooser реализует cliapp.Chooser для неоднозначных селекторов:
// печатает candidate lines в stderr и читает выбор пользователя из stdin.
// Посторонний ввод отклоняется ошибкой, поэтому cmd слой сохранит code 8.
// Строки prompt переводятся ключами chooser.* на языке, выбранном в main.
type interactiveChooser struct {
	stdin  *bufio.Reader
	stderr *os.File
	lang   string
}

// Choose предлагает пользователю выбрать одну из candidate lines и возвращает
// её one-based номер. Некорректный номер, EOF и сбой ввода — ошибка: App не
// должен выбирать кандидата самостоятельно.
func (c interactiveChooser) Choose(candidates []int) (int, error) {
	_, _ = fmt.Fprintln(c.stderr, i18n.T(c.lang, "chooser.ambiguous_header"))
	for _, line := range candidates {
		_, _ = fmt.Fprintf(c.stderr, "  %d\n", line)
	}
	_, _ = fmt.Fprint(c.stderr, i18n.T(c.lang, "chooser.line_prompt"))
	answer, err := c.stdin.ReadString('\n')
	if err != nil && strings.TrimSpace(answer) == "" {
		return 0, fmt.Errorf("выбор не получен: %w", err)
	}
	picked, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil {
		return 0, fmt.Errorf("некорректный номер строки: %w", err)
	}
	return picked, nil
}
