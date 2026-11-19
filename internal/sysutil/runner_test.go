package sysutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestResolveInDirectoriesFirstDirectoryWins проверяет детерминированный приоритет fixed directories.
func TestResolveInDirectoriesFirstDirectoryWins(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	// Оба каталога содержат executable: должен победить первый fixed directory,
	// независимо от PATH и порядка чтения directory entries.
	firstTool := filepath.Join(first, "tool")
	secondTool := filepath.Join(second, "tool")
	if err := os.WriteFile(firstTool, []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondTool, []byte("second"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveInDirectories("tool", []string{first, second})
	if err != nil || got != firstTool {
		t.Fatalf("resolution: got %q, err=%v", got, err)
	}
	// Возвращаемый путь обязан быть абсолютным для безопасного запуска без PATH.
	if !filepath.IsAbs(got) {
		t.Fatalf("resolution returned relative path %q", got)
	}
}

// TestResolveInDirectoriesSkipsNonExecutable проверяет пропуск обычного файла без битов исполнения.
func TestResolveInDirectoriesSkipsNonExecutable(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	// Первый файл не исполняемый, поэтому resolver должен перейти ко второму каталогу.
	if err := os.WriteFile(filepath.Join(first, "tool"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	secondTool := filepath.Join(second, "tool")
	if err := os.WriteFile(secondTool, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveInDirectories("tool", []string{first, second})
	if err != nil || got != secondTool {
		t.Fatalf("resolution: got %q, err=%v", got, err)
	}
}

// TestResolveInDirectoriesSkipsDirectory проверяет, что каталог не считается executable-файлом.
func TestResolveInDirectoriesSkipsDirectory(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	// Каталог с исполняемыми битами в первом пути должен быть пропущен.
	if err := os.Mkdir(filepath.Join(first, "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	secondTool := filepath.Join(second, "tool")
	if err := os.WriteFile(secondTool, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveInDirectories("tool", []string{first, second})
	if err != nil || got != secondTool {
		t.Fatalf("resolution: got %q, err=%v", got, err)
	}
}

// TestResolveInDirectoriesFollowsSymlink проверяет разрешение symlink на regular executable через os.Stat.
func TestResolveInDirectoriesFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	// Цель находится вне candidate path, а symlink повторяет поведение системного /bin.
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(dir, "tool")
	if err := os.Symlink(target, tool); err != nil {
		t.Fatal(err)
	}

	got, err := resolveInDirectories("tool", []string{dir})
	if err != nil || got != tool {
		t.Fatalf("resolution: got %q, err=%v", got, err)
	}
}

// TestResolveInDirectoriesRejectsInvalidNames проверяет отказ от имён с путями и специальных значений.
func TestResolveInDirectoriesRejectsInvalidNames(t *testing.T) {
	tests := []struct {
		name string
	}{
		// Пустое имя не задаёт утилиту.
		{name: ""},
		// Текущий каталог не является именем утилиты.
		{name: "."},
		// Родительский каталог также запрещён как специальное имя.
		{name: ".."},
		// Относительный путь с разделителем запрещён.
		{name: "tool/name"},
		// Выход в родительский каталог запрещён.
		{name: "../tool"},
		// Абсолютный путь нельзя подменять вместо fixed directories.
		{name: filepath.Join(string(filepath.Separator), "tmp", "tool")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveInDirectories(tt.name, []string{t.TempDir()})
			if !errors.Is(err, ErrInvalidExecutable) {
				t.Fatalf("expected ErrInvalidExecutable for %q, got %v", tt.name, err)
			}
		})
	}
}

// TestResolveInDirectoriesNotFound проверяет отдельную ошибку отсутствия утилиты.
func TestResolveInDirectoriesNotFound(t *testing.T) {
	// Пустой fixed directory не содержит искомого executable.
	_, err := resolveInDirectories("missing", []string{t.TempDir()})
	if !errors.Is(err, ErrExecutableNotFound) {
		t.Fatalf("expected ErrExecutableNotFound, got %v", err)
	}
}

// TestResolveInDirectoriesPreservesStatError проверяет возврат ошибки Stat, отличной от not-exist.
func TestResolveInDirectoriesPreservesStatError(t *testing.T) {
	dir := t.TempDir()
	// Циклический symlink гарантированно даёт ошибку Stat ELOOP без проверок прав root.
	tool := filepath.Join(dir, "tool")
	if err := os.Symlink("tool", tool); err != nil {
		t.Fatal(err)
	}

	_, err := resolveInDirectories("tool", []string{dir})
	if err == nil || errors.Is(err, ErrExecutableNotFound) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected preserved Stat error, got %v", err)
	}
}

// TestResolveExecutableDoesNotUsePATH проверяет public API и независимость от PATH.
func TestResolveExecutableDoesNotUsePATH(t *testing.T) {
	const name = "definitely-not-present-fstab-edit"
	// Даже executable с искомым именем в PATH не должен участвовать в resolution.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	_, err := ResolveExecutable(name)
	if !errors.Is(err, ErrExecutableNotFound) {
		t.Fatalf("expected ErrExecutableNotFound, got %v", err)
	}
}

// TestRunnerHelperProcess реализует отдельный процесс с управляемым выводом и завершением.
func TestRunnerHelperProcess(t *testing.T) {
	// В parent suite нет разделителя аргументов, поэтому helper немедленно возвращается.
	mode, pidFile := "", ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			if i+2 < len(os.Args) {
				pidFile = os.Args[i+2]
			}
			break
		}
	}
	if mode == "" {
		return
	}

	switch mode {
	// Режим success проверяет сырой вывод обоих потоков и принудительную локаль C.
	case "success":
		fmt.Print("out")
		fmt.Fprint(os.Stderr, "err")
		fmt.Printf("LC_ALL=%s\n", os.Getenv("LC_ALL"))
		os.Exit(0)
	// Режим exit7 сохраняет вывод и возвращает ненулевой код для проверки ExitError.
	case "exit7":
		fmt.Print("out")
		fmt.Fprint(os.Stderr, "err")
		os.Exit(7)
	// Режим sleep даёт parent context достаточно времени сработать раньше процесса.
	case "sleep":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	// Режим large заполняет оба потока данными, превышающими тестовый лимит.
	case "large":
		fmt.Print(strings.Repeat("A", 128))
		fmt.Fprint(os.Stderr, strings.Repeat("B", 128))
		os.Exit(0)
	// Режим descendant оставляет после смерти helper долгоживущий child с его stdout/stderr.
	case "descendant":
		if pidFile == "" {
			os.Exit(2)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestRunnerHelperProcess$", "--", "descendant-child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(2)
		}
		time.Sleep(5 * time.Second)
		os.Exit(0)
	// Короткое время жизни child ограничивает RED-тест даже без WaitDelay.
	case "descendant-child":
		time.Sleep(3 * time.Second)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

// runHelper запускает текущий test binary напрямую, минуя fixed-directory resolver.
func runHelper(t *testing.T, runner *Runner, ctx context.Context, mode string) RunResult {
	t.Helper()
	return runner.runAbsolute(ctx, os.Args[0], "-test.run=^TestRunnerHelperProcess$", "--", mode)
}

// TestRunnerSuccess проверяет успешный запуск, сырой вывод и LC_ALL=C.
func TestRunnerSuccess(t *testing.T) {
	// Унаследованная локаль намеренно отличается от C и должна быть заменена.
	t.Setenv("LC_ALL", "ru_RU.UTF-8")
	result := runHelper(t, NewRunner(), context.Background(), "success")

	if result.Path != os.Args[0] {
		t.Fatalf("Path: got %q, want %q", result.Path, os.Args[0])
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", result.ExitCode)
	}
	if result.Stdout != "outLC_ALL=C\n" {
		t.Fatalf("Stdout: got %q", result.Stdout)
	}
	if result.Stderr != "err" {
		t.Fatalf("Stderr: got %q", result.Stderr)
	}
	if result.Duration < 0 {
		t.Fatalf("Duration: got %v", result.Duration)
	}
	if result.Err != nil {
		t.Fatalf("Err: got %v", result.Err)
	}
	if result.TimedOut || result.StdoutTruncated || result.StderrTruncated {
		t.Fatalf("unexpected flags: %+v", result)
	}
}

// TestRunnerExitCode проверяет сохранение вывода и exec.ExitError при коде 7.
func TestRunnerExitCode(t *testing.T) {
	result := runHelper(t, NewRunner(), context.Background(), "exit7")

	if result.ExitCode != 7 {
		t.Fatalf("ExitCode: got %d, want 7", result.ExitCode)
	}
	if result.Stdout != "out" || result.Stderr != "err" {
		t.Fatalf("output: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
	var exitErr *exec.ExitError
	if !errors.As(result.Err, &exitErr) {
		t.Fatalf("expected exec.ExitError, got %T: %v", result.Err, result.Err)
	}
}

// TestRunnerTimeout проверяет завершение процесса по deadline и отдельный признак TimedOut.
func TestRunnerTimeout(t *testing.T) {
	runner := NewRunner()
	runner.Timeout = 30 * time.Millisecond
	result := runHelper(t, runner, context.Background(), "sleep")

	if !result.TimedOut {
		t.Fatal("expected TimedOut=true")
	}
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", result.Err)
	}
	if result.ExitCode == 0 {
		t.Fatal("expected nonzero ExitCode after timeout")
	}
}

// TestRunnerDescendantKeepsPipesOpen проверяет, что потомок не удерживает Run после deadline.
func TestRunnerDescendantKeepsPipesOpen(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	// Cleanup завершает потомка по сохранённому PID и после успеха, и после любой проверки.
	t.Cleanup(func() {
		pidData, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		var pid int
		if _, err := fmt.Sscanf(string(pidData), "%d", &pid); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	runner := &Runner{Timeout: 200 * time.Millisecond, OutputLimit: DefaultOutputLimit}
	started := time.Now()
	result := runner.runAbsolute(
		context.Background(), os.Args[0],
		"-test.run=^TestRunnerHelperProcess$", "--", "descendant", pidFile,
	)
	elapsed := time.Since(started)

	// Граница оставляет большой запас над timeout и WaitDelay, но меньше жизни потомка.
	if elapsed >= 2*time.Second {
		t.Fatalf("Run ждал закрытия pipe потомком: elapsed=%v, result=%+v", elapsed, result)
	}
	if !result.TimedOut {
		t.Fatalf("ожидался TimedOut=true, result=%+v", result)
	}
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("ожидался context.DeadlineExceeded, получено %v", result.Err)
	}
}

// TestRunnerParentCancellation проверяет отмену parent context без маскировки под timeout.
func TestRunnerParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := runHelper(t, NewRunner(), ctx, "sleep")

	if result.TimedOut {
		t.Fatal("parent cancellation must not set TimedOut")
	}
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", result.Err)
	}
}

// TestRunnerOutputLimit проверяет независимое ограничение stdout и stderr без short write.
func TestRunnerOutputLimit(t *testing.T) {
	runner := NewRunner()
	runner.OutputLimit = 8
	result := runHelper(t, runner, context.Background(), "large")

	if result.ExitCode != 0 || result.Err != nil {
		t.Fatalf("helper did not finish: code=%d err=%v", result.ExitCode, result.Err)
	}
	if result.Stdout != strings.Repeat("A", 8) {
		t.Fatalf("Stdout: got %q", result.Stdout)
	}
	if result.Stderr != strings.Repeat("B", 8) {
		t.Fatalf("Stderr: got %q", result.Stderr)
	}
	if !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("expected both truncation flags: %+v", result)
	}
}

// TestRunnerStartFailure проверяет безопасный результат при невозможности запустить absolute path.
func TestRunnerStartFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	result := NewRunner().runAbsolute(context.Background(), path)

	if result.ExitCode != -1 {
		t.Fatalf("ExitCode: got %d, want -1", result.ExitCode)
	}
	if result.Err == nil {
		t.Fatal("expected start error")
	}
	if result.Path != path || result.TimedOut {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// TestRunnerSettingsDefaults проверяет точные defaults для nil, zero и negative Runner.
func TestRunnerSettingsDefaults(t *testing.T) {
	tests := []struct {
		name   string
		runner *Runner
	}{
		// Nil receiver не должен ни паниковать, ни отключать ограничения.
		{name: "nil", runner: nil},
		// Zero-value Runner получает оба значения по умолчанию.
		{name: "zero", runner: &Runner{}},
		// Отрицательные значения заменяются безопасными defaults.
		{name: "negative", runner: &Runner{Timeout: -time.Second, OutputLimit: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timeout, limit := tt.runner.settings()
			if timeout != 10*time.Second {
				t.Fatalf("Timeout: got %v, want %v", timeout, 10*time.Second)
			}
			if limit != 64*1024 {
				t.Fatalf("OutputLimit: got %d, want %d", limit, 64*1024)
			}
		})
	}
}

// TestRunnerZeroValue проверяет применение defaults нулевым Runner без ожидания реального timeout.
func TestRunnerZeroValue(t *testing.T) {
	var runner Runner
	result := runHelper(t, &runner, context.Background(), "success")

	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("zero-value Runner failed: code=%d err=%v", result.ExitCode, result.Err)
	}
	if result.StdoutTruncated || result.StderrTruncated || result.TimedOut {
		t.Fatalf("unexpected flags: %+v", result)
	}
}

// TestRunnerNilReceiver проверяет завершение helper через nil Runner без реальных 10 секунд ожидания.
func TestRunnerNilReceiver(t *testing.T) {
	var runner *Runner
	result := runHelper(t, runner, context.Background(), "success")
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("nil receiver run failed: code=%d err=%v", result.ExitCode, result.Err)
	}
	if result.Stdout != "outLC_ALL=C\n" || result.Stderr != "err" {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

// TestRunnerNilContext проверяет замену nil context на context.Background.
func TestRunnerNilContext(t *testing.T) {
	result := runHelper(t, NewRunner(), nil, "success")
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("nil context run failed: code=%d err=%v", result.ExitCode, result.Err)
	}
}

// TestRunnerNewRunnerDefaults проверяет значения таймаута и лимита, заданные конструктором.
func TestRunnerNewRunnerDefaults(t *testing.T) {
	runner := NewRunner()
	if runner.Timeout != DefaultTimeout {
		t.Fatalf("Timeout: got %v, want %v", runner.Timeout, DefaultTimeout)
	}
	if runner.OutputLimit != DefaultOutputLimit {
		t.Fatalf("OutputLimit: got %d, want %d", runner.OutputLimit, DefaultOutputLimit)
	}
}

// TestRunnerRunReturnsResolutionError проверяет exported Run без обращения к PATH.
func TestRunnerRunReturnsResolutionError(t *testing.T) {
	result := NewRunner().Run(context.Background(), "definitely-not-present-fstab-edit")
	if result.ExitCode != -1 || !errors.Is(result.Err, ErrExecutableNotFound) {
		t.Fatalf("unexpected result: %+v", result)
	}
}
