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
	"fstab-edit/internal/sysutil"
)

// main собирает production App и завершает процесс с кодом, который вернула
// команда. os.Exit вызывается только здесь, чтобы тесты cmd не затрагивали
// процесс test-runner.
func main() {
	stdin := bufio.NewReader(os.Stdin)
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   stdin,
		Version: "dev",
		Runner:  sysutil.NewRunner(),
		TTY:     stdinIsTTY,
		Chooser: interactiveChooser{stdin: stdin, stderr: os.Stderr},
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
type interactiveChooser struct {
	stdin  *bufio.Reader
	stderr *os.File
}

// Choose предлагает пользователю выбрать одну из candidate lines и возвращает
// её one-based номер. Некорректный номер, EOF и сбой ввода — ошибка: App не
// должен выбирать кандидата самостоятельно.
func (c interactiveChooser) Choose(candidates []int) (int, error) {
	_, _ = fmt.Fprintln(c.stderr, "селектор неоднозначен, выберите строку:")
	for _, line := range candidates {
		_, _ = fmt.Fprintf(c.stderr, "  %d\n", line)
	}
	_, _ = fmt.Fprint(c.stderr, "номер строки: ")
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
