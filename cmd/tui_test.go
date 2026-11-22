// Файл tui_test.go проверяет интеграцию CLI с TUI: команду `tui`, запуск без
// аргументов, требование TTY и проброс настроек (язык, тема, target) в
// tui.Options. Реальный bubbletea-запуск здесь не выполняется: AppOptions
// получает заглушку TuiLauncher, которая захватывает Options; ветвление
// «bare invocation → tui» проверяется отдельно от интерактивного рендера.
// Все сценарии используют tempdir target/config, memory buffers, инъектируемое
// пустое окружение (default язык детерминированно "ru") и fake runner.
package cmd_test

import (
	"reflect"
	"strings"
	"testing"

	"fstab-edit/cmd"
	"fstab-edit/tui"
)

// tuiLauncherCapture — заглушка AppOptions.TuiLauncher: фиксирует факт вызова
// и захватывает проброшенные tui.Options, чтобы тесты проверяли сборку
// опций без реального запуска bubbletea-программы.
type tuiLauncherCapture struct {
	called bool
	opts   tui.Options
}

func (c *tuiLauncherCapture) launch(opts tui.Options) error {
	c.called = true
	c.opts = opts
	return nil
}

// TestTuiCommandWithoutTTYRejected фиксирует, что `tui` без TTY отклоняется
// usage error (exit 2) с локализованным сообщением tui.no_tty и не вызывает
// launcher.
func TestTuiCommandWithoutTTYRejected(t *testing.T) {
	launcher := &tuiLauncherCapture{}
	buffers := &appBuffers{}
	target := writeTempTarget(t, "UUID=aaa / ext4 defaults 0 1\n")
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		TTY:               func() bool { return false },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: testLogConfig(t),
		TuiLauncher:       launcher.launch,
	})

	code := app.Execute([]string{"tui", "--file", target})
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr: %s", code, buffers.stderr.String())
	}
	if !strings.Contains(buffers.stderr.String(), "TUI требует интерактивный терминал (TTY)") {
		t.Fatalf("tui.no_tty missing: %q", buffers.stderr.String())
	}
	if launcher.called {
		t.Fatal("launcher invoked without TTY")
	}
}

// TestBareInvocationOpensTUI фиксирует ветвление запуска без аргументов:
// пустой argv подменяется на ["tui"] до парсинга, поэтому non-TTY запуск
// без аргументов даёт тот же usage error, а при TTY вызывается launcher;
// явные аргументы (--help) подмене не подвержены.
func TestBareInvocationOpensTUI(t *testing.T) {
	// Non-TTY без аргументов: подмена на tui → тот же отказ, exit 2.
	launcher := &tuiLauncherCapture{}
	buffers := &appBuffers{}
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		TTY:               func() bool { return false },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: testLogConfig(t),
		TuiLauncher:       launcher.launch,
	})
	if code := app.Execute(nil); code != 2 {
		t.Fatalf("bare code = %d, want 2; stderr: %s", code, buffers.stderr.String())
	}
	if !strings.Contains(buffers.stderr.String(), "TUI требует интерактивный терминал (TTY)") {
		t.Fatalf("tui.no_tty missing on bare run: %q", buffers.stderr.String())
	}
	if launcher.called {
		t.Fatal("launcher invoked without TTY on bare run")
	}

	// TTY=true: launcher получает управление, target — дефолтный из config.
	launcher = &tuiLauncherCapture{}
	buffers = &appBuffers{}
	app = cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		TTY:               func() bool { return true },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: testLogConfig(t),
		TuiLauncher:       launcher.launch,
	})
	if code := app.Execute(nil); code != 0 {
		t.Fatalf("bare TTY code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !launcher.called {
		t.Fatal("launcher not invoked on bare TTY run")
	}

	// --help: подмена не затрагивает непустой argv, справка печатается (exit 0).
	launcher.called = false
	buffers = &appBuffers{}
	app = cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		TTY:               func() bool { return false },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: testLogConfig(t),
		TuiLauncher:       launcher.launch,
	})
	if code := app.Execute([]string{"--help"}); code != 0 {
		t.Fatalf("--help code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !strings.Contains(buffers.stdout.String(), "безопасное редактирование fstab") {
		t.Fatalf("help output missing root short: %q", buffers.stdout.String())
	}
	if launcher.called {
		t.Fatal("launcher invoked for --help")
	}
}

// TestTuiRespectsLangAndThemeFromConfig проверяет проброс настроек в
// tui.Options: --lang en выбирает язык сообщений TUI, а tui.theme из YAML
// конфигурации задаёт тему; target приходит из --file.
func TestTuiRespectsLangAndThemeFromConfig(t *testing.T) {
	launcher := &tuiLauncherCapture{}
	buffers := &appBuffers{}
	target := writeTempTarget(t, "UUID=aaa / ext4 defaults 0 1\n")
	configPath := writeTestConfig(t, "tui:\n  theme: monochrome\n")
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		TTY:               func() bool { return true },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: configPath,
		TuiLauncher:       launcher.launch,
	})

	code := app.Execute([]string{"--lang", "en", "tui", "--file", target})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !launcher.called {
		t.Fatal("launcher not invoked")
	}
	opts := launcher.opts
	if opts.Lang != "en" {
		t.Fatalf("Lang = %q, want en", opts.Lang)
	}
	if opts.Theme != "monochrome" {
		t.Fatalf("Theme = %q, want monochrome", opts.Theme)
	}
	if opts.Target != target {
		t.Fatalf("Target = %q, want %q", opts.Target, target)
	}
}

// TestTuiPassesThemeOverridesFromConfig проверяет проброс пользовательской
// палитры: tui.colors из YAML-конфигурации доезжает до tui.Options.ThemeOverrides
// (этап 5, Task 3: пользовательские темы), без ключа colors проброс nil.
func TestTuiPassesThemeOverridesFromConfig(t *testing.T) {
	launcher := &tuiLauncherCapture{}
	buffers := &appBuffers{}
	target := writeTempTarget(t, "UUID=aaa / ext4 defaults 0 1\n")
	configPath := writeTestConfig(t, "tui:\n  theme: dark\n  colors:\n    title: \"#00ffff\"\n    selected_fg: \"0\"\n")
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		TTY:               func() bool { return true },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: configPath,
		TuiLauncher:       launcher.launch,
	})
	if code := app.Execute([]string{"tui", "--file", target}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	want := map[string]string{"title": "#00ffff", "selected_fg": "0"}
	if !reflect.DeepEqual(launcher.opts.ThemeOverrides, want) {
		t.Fatalf("ThemeOverrides = %+v, want %+v", launcher.opts.ThemeOverrides, want)
	}

	// Без tui.colors в конфигурации проброс nil: палитра темы без изменений.
	launcher = &tuiLauncherCapture{}
	buffers = &appBuffers{}
	app = cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		TTY:               func() bool { return true },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: testLogConfig(t),
		TuiLauncher:       launcher.launch,
	})
	if code := app.Execute([]string{"tui", "--file", target}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if launcher.opts.ThemeOverrides != nil {
		t.Fatalf("ThemeOverrides = %+v, want nil без tui.colors", launcher.opts.ThemeOverrides)
	}
}
