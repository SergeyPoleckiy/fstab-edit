// Файл runner.go реализует безопасный запуск внешних системных утилит проекта.
// Он отделяет разрешение имени исполняемого файла от самого запуска: публичный Run
// использует только фиксированные системные каталоги, а внутренний runAbsolute
// дополнительно позволяет тестам запускать собственный Go helper process.
// Shell здесь не применяется, поэтому аргументы никогда не интерпретируются как команда.
package sysutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultTimeout ограничивает полное время выполнения одной внешней утилиты.
	DefaultTimeout = 10 * time.Second
	// DefaultOutputLimit ограничивает сохраняемый размер каждого потока вывода.
	// stdout и stderr учитываются независимо, чтобы один поток не вытеснял другой.
	DefaultOutputLimit = 64 * 1024
)

var (
	// ErrInvalidExecutable означает, что имя утилиты пусто или содержит путь.
	// Передача собственного пути запрещена, чтобы вызывающий код не обошёл fixed directories.
	ErrInvalidExecutable = errors.New("некорректное имя исполняемого файла")
	// ErrExecutableNotFound означает отсутствие executable-файла во всех разрешённых каталогах.
	ErrExecutableNotFound = errors.New("исполняемый файл не найден")
)

// executableDirectories задаёт приоритет поиска системных утилит. Пользовательский PATH
// намеренно отсутствует: текущий каталог и настройки shell не должны подменять программу.
var executableDirectories = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// CommandRunner описывает минимальный контракт запуска внешней утилиты.
// FindmntVerifier зависит от этого интерфейса, поэтому тесты могут подменить процесс
// без изменения production-кода. Реализации не обязаны быть потокобезопасными.
type CommandRunner interface {
	// Run выполняет утилиту с отдельными argv и возвращает полный результат запуска.
	Run(ctx context.Context, name string, args ...string) RunResult
}

// Runner настраивает ограничения выполнения внешней команды. Нулевые и отрицательные
// значения полей заменяются безопасными defaults, поэтому нулевой Runner пригоден к использованию.
type Runner struct {
	// Timeout задаёт максимальную длительность запуска, включая ожидание завершения.
	Timeout time.Duration
	// OutputLimit задаёт максимальное число сохраняемых bytes в stdout и в stderr.
	OutputLimit int
}

// RunResult содержит наблюдаемый результат процесса. Stdout и Stderr являются raw
// потенциально sensitive данными: их нельзя без redaction записывать в журнал.
// Err описывает сбой запуска, отмену или ненулевой exit code и не включается в JSON diagnostics.
type RunResult struct {
	// Path — абсолютный путь исполняемого файла, если resolution или вызов дошёл до запуска.
	Path string
	// ExitCode равен коду завершения процесса; -1 означает отсутствие успешного exit status.
	ExitCode int
	// Stdout и Stderr содержат сохранённую часть соответствующих потоков.
	Stdout string
	Stderr string
	// Duration измеряет фактическое время Run, но не resolution внешнего имени.
	Duration time.Duration
	// TimedOut отличает deadline от обычной отмены parent context.
	TimedOut bool
	// StdoutTruncated и StderrTruncated показывают потерю части соответствующего вывода.
	StdoutTruncated bool
	StderrTruncated bool
	// Err сохраняет техническую причину сбоя для классификатора и controlled diagnostics.
	Err error
}

// NewRunner создаёт Runner с timeout 10 секунд и лимитом 64 KiB на каждый поток.
func NewRunner() *Runner {
	return &Runner{Timeout: DefaultTimeout, OutputLimit: DefaultOutputLimit}
}

// ResolveExecutable находит исполняемый файл только в fixed directories, не используя PATH.
// Возвращается первый подходящий абсолютный путь либо typed error.
func ResolveExecutable(name string) (string, error) {
	return resolveInDirectories(name, executableDirectories)
}

// resolveInDirectories реализует resolution с инъектируемым списком каталогов для тестов.
// Symlink на regular executable разрешён через os.Stat, поскольку на поддерживаемых Linux
// системах /bin и /sbin могут быть штатными ссылками на /usr/bin и /usr/sbin.
func resolveInDirectories(name string, directories []string) (string, error) {
	if name == "" || name == "." || filepath.Base(name) != name || filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidExecutable, name)
	}

	var statErr error
	for _, directory := range directories {
		candidate, err := filepath.Abs(filepath.Join(directory, name))
		if err != nil {
			if statErr == nil {
				statErr = err
			}
			continue
		}

		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if statErr == nil {
				statErr = err
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}

	if statErr != nil {
		return "", fmt.Errorf("разрешение исполняемого файла %q: %w", name, statErr)
	}
	return "", fmt.Errorf("%w: %q", ErrExecutableNotFound, name)
}

// Run разрешает name в fixed directories и запускает найденный absolute path.
// Аргументы передаются отдельным slice без shell. Ошибка resolution возвращается как RunResult,
// чтобы единый внешний классификатор обрабатывал все сбои одинаково.
func (r *Runner) Run(ctx context.Context, name string, args ...string) RunResult {
	path, err := ResolveExecutable(name)
	if err != nil {
		return RunResult{ExitCode: -1, Err: err}
	}
	return r.runAbsolute(ctx, path, args...)
}

// settings возвращает эффективные ограничения даже для nil или zero-value Runner.
// Это исключает случайное отключение timeout и ограничения памяти при zero values.
func (r *Runner) settings() (time.Duration, int) {
	timeout, limit := DefaultTimeout, DefaultOutputLimit
	if r == nil {
		return timeout, limit
	}
	if r.Timeout > 0 {
		timeout = r.Timeout
	}
	if r.OutputLimit > 0 {
		limit = r.OutputLimit
	}
	return timeout, limit
}

// limitedBuffer сохраняет не более limit bytes и сообщает записывающей стороне полный успех.
// Возврат исходного len(p) важен: os/exec не должен получить short write только потому,
// что дальнейший вывод намеренно отбрасывается для ограничения памяти.
type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

// Write сохраняет доступный остаток p, отбрасывает избыток и всегда возвращает len(p), nil.
// Каждый поток имеет отдельный экземпляр, поэтому конкурентные stdout/stderr не разделяют buffer.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	originalLength := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		toWrite := len(p)
		if toWrite > remaining {
			toWrite = remaining
		}
		_, _ = b.buffer.Write(p[:toWrite])
		b.truncated = b.truncated || toWrite < originalLength
	} else if originalLength > 0 {
		b.truncated = true
	}
	return originalLength, nil
}

// runAbsolute выполняет уже разрешённый absolute path и применяет все ограничения Runner.
// Метод не вызывает shell. Тесты используют его для запуска текущего Go test binary,
// а production-код получает path исключительно через ResolveExecutable.
func (r *Runner) runAbsolute(ctx context.Context, path string, args ...string) RunResult {
	result := RunResult{Path: path, ExitCode: -1}
	if !filepath.IsAbs(path) {
		result.Err = fmt.Errorf("%w: требуется абсолютный путь", ErrInvalidExecutable)
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout, limit := r.settings()
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: limit}
	command := exec.CommandContext(runContext, path, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = localeEnvironment()

	started := time.Now()
	err := command.Run()
	result.Duration = time.Since(started)
	result.Stdout = stdout.buffer.String()
	result.Stderr = stderr.buffer.String()
	result.StdoutTruncated = stdout.truncated
	result.StderrTruncated = stderr.truncated
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}

	// Только неуспешный запуск классифицируется по состоянию context. Иначе успешное
	// завершение на границе deadline не должно превращаться в ложный timeout.
	if err != nil && errors.Is(runContext.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.Err = context.DeadlineExceeded
	} else if err != nil && errors.Is(runContext.Err(), context.Canceled) {
		result.Err = context.Canceled
	} else {
		result.Err = err
	}
	return result
}

// localeEnvironment копирует окружение и заменяет все LC_ALL на одно значение C.
// Сохранение остальных переменных необходимо системным утилитам и Go helper process;
// единственная LC_ALL гарантирует стабильный машинный разбор сообщений findmnt.
func localeEnvironment() []string {
	environment := os.Environ()
	result := make([]string, 0, len(environment)+1)
	for _, variable := range environment {
		if !strings.HasPrefix(variable, "LC_ALL=") {
			result = append(result, variable)
		}
	}
	return append(result, "LC_ALL=C")
}
