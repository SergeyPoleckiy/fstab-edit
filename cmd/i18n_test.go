// Файл i18n_test.go проверяет локализацию CLI: help-тексты, usage флагов,
// диагностические сообщения и подтверждения на языке, выбранном --lang или
// резолвом окружения. Среда всегда инъектируется пустой, поэтому поведение
// «по умолчанию» детерминировано независимо от LANG машины прогона. Все
// сценарии используют временный fstab в t.TempDir() и fake runner: реальный
// /etc/fstab и findmnt не затрагиваются.
package cmd_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"fstab-edit/cmd"
)

// newAppI18n собирает App с инъектируемым окружением: Getenv/Environ
// подменяют os.Getenv/os.Environ в резолве языка, поэтому тест контролирует
// резолв полностью (пустое окружение = default "ru" без влияния LANG машины).
func newAppI18n(buffers *appBuffers, stdin io.Reader, tty bool, runner *fakeRunner,
	getenv func(string) string, environ func() []string,
) *cmd.App {
	return cmd.NewApp(cmd.AppOptions{
		Stdout:  &buffers.stdout,
		Stderr:  &buffers.stderr,
		Stdin:   stdin,
		TTY:     func() bool { return tty },
		Runner:  runner,
		Getenv:  getenv,
		Environ: environ,
	})
}

// emptyEnv — инъектируемое «пустое» окружение: ни переменных, ни локалей.
func emptyEnv() (func(string) string, func() []string) {
	return func(string) string { return "" }, func() []string { return nil }
}

// TestHelpLocalized проверяет, что --lang en переводит Short корневой команды:
// английский текст появляется, русский исчезает.
func TestHelpLocalized(t *testing.T) {
	buffers := &appBuffers{}
	getenv, environ := emptyEnv()
	app := newAppI18n(buffers, strings.NewReader(""), false, &fakeRunner{}, getenv, environ)

	if code := app.Execute([]string{"--lang", "en", "--help"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	output := buffers.stdout.String()
	if !strings.Contains(output, "safe fstab editing") {
		t.Fatalf("english help.root.short missing: %q", output)
	}
	if strings.Contains(output, "безопасное редактирование fstab") {
		t.Fatalf("russian help.root.short present with --lang en: %q", output)
	}
}

// TestHelpDefaultRussian проверяет, что без --lang и с пустым окружением
// справка остаётся русской (default язык Resolve).
func TestHelpDefaultRussian(t *testing.T) {
	buffers := &appBuffers{}
	getenv, environ := emptyEnv()
	app := newAppI18n(buffers, strings.NewReader(""), false, &fakeRunner{}, getenv, environ)

	if code := app.Execute([]string{"--help"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !strings.Contains(buffers.stdout.String(), "безопасное редактирование fstab") {
		t.Fatalf("russian help.root.short missing: %q", buffers.stdout.String())
	}
}

// TestErrorMessageLocalized проверяет, что диагностика чтения несуществующего
// target печатается на языке --lang: английский вариант, без русского текста.
func TestErrorMessageLocalized(t *testing.T) {
	missing := t.TempDir() + "/no-such-fstab"
	buffers := &appBuffers{}
	getenv, environ := emptyEnv()
	app := newAppI18n(buffers, strings.NewReader(""), false, &fakeRunner{}, getenv, environ)

	code := app.Execute([]string{"list", "--lang", "en", "--file", missing})
	if code != 6 {
		t.Fatalf("code = %d, want 6; stderr: %s", code, buffers.stderr.String())
	}
	stderr := buffers.stderr.String()
	if !strings.Contains(stderr, "read target") {
		t.Fatalf("english error.io.read_target missing: %q", stderr)
	}
	if strings.Contains(stderr, "прочитать target") {
		t.Fatalf("russian error.io.read_target present with --lang en: %q", stderr)
	}
}

// TestConfirmLocalizedYes проверяет локализованные формы подтверждения:
// «да» принимается в русском default, «y» — при --lang en, пустой ответ
// отменяет операцию (default No) в обоих случаях.
func TestConfirmLocalizedYes(t *testing.T) {
	buildArgs := func(target string, extra ...string) []string {
		return append([]string{
			"add", "--file", target,
			"--device", "/dev/sdd1", "--mount", "/mnt/new", "--type", "ext4",
			"--options", "defaults",
		}, extra...)
	}

	// Русский default: «да» применяет мутацию.
	russian := writeTempTarget(t, e2eFixture)
	buffers := &appBuffers{}
	getenv, environ := emptyEnv()
	app := newAppI18n(buffers, strings.NewReader("да\n"), true, &fakeRunner{}, getenv, environ)
	if code := app.Execute(buildArgs(russian)); code != 0 {
		t.Fatalf("ru accept code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Contains(readTargetBytes(t, russian), []byte("/mnt/new")) {
		t.Fatal("ru «да» did not apply mutation")
	}

	// Английский --lang: «y» применяет мутацию.
	english := writeTempTarget(t, e2eFixture)
	buffers = &appBuffers{}
	getenv, environ = emptyEnv()
	app = newAppI18n(buffers, strings.NewReader("y\n"), true, &fakeRunner{}, getenv, environ)
	if code := app.Execute(buildArgs(english, "--lang", "en")); code != 0 {
		t.Fatalf("en accept code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Contains(readTargetBytes(t, english), []byte("/mnt/new")) {
		t.Fatal("en «y» did not apply mutation")
	}

	// Пустой ответ — отказ: target не изменён, код успеха сохраняется.
	declined := writeTempTarget(t, e2eFixture)
	buffers = &appBuffers{}
	getenv, environ = emptyEnv()
	app = newAppI18n(buffers, strings.NewReader("\n"), true, &fakeRunner{}, getenv, environ)
	if code := app.Execute(buildArgs(declined)); code != 0 {
		t.Fatalf("decline code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, declined), []byte(e2eFixture)) {
		t.Fatal("empty answer changed target")
	}
}

// TestFlagUsageLocalized проверяет, что usage флага --dry-run в help переведён
// по ключу help.flag.dry_run.
func TestFlagUsageLocalized(t *testing.T) {
	buffers := &appBuffers{}
	getenv, environ := emptyEnv()
	app := newAppI18n(buffers, strings.NewReader(""), false, &fakeRunner{}, getenv, environ)

	if code := app.Execute([]string{"--lang", "en", "--help"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	output := buffers.stdout.String()
	if !strings.Contains(output, "show diff without writing") {
		t.Fatalf("english help.flag.dry_run missing: %q", output)
	}
	if strings.Contains(output, "показать diff без записи") {
		t.Fatalf("russian help.flag.dry_run present with --lang en: %q", output)
	}
}

// TestLangEnvResolution проверяет, что без --lang язык резолвится из
// инъектируемого окружения (приоритет выше локали и default).
func TestLangEnvResolution(t *testing.T) {
	buffers := &appBuffers{}
	getenv := func(name string) string { return "" }
	environ := func() []string { return []string{"FSTAB_EDIT_LANG=en"} }
	app := newAppI18n(buffers, strings.NewReader(""), false, &fakeRunner{}, getenv, environ)

	if code := app.Execute([]string{"--help"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !strings.Contains(buffers.stdout.String(), "safe fstab editing") {
		t.Fatalf("FSTAB_EDIT_LANG=en not applied: %q", buffers.stdout.String())
	}
}

// TestLangEqualsForm проверяет pre-scan формы --lang=en (последнее вхождение
// выигрывает) вместе с позиционным синтаксисом.
func TestLangEqualsForm(t *testing.T) {
	buffers := &appBuffers{}
	getenv, environ := emptyEnv()
	app := newAppI18n(buffers, strings.NewReader(""), false, &fakeRunner{}, getenv, environ)

	if code := app.Execute([]string{"--lang=ru", "--lang=en", "--help"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !strings.Contains(buffers.stdout.String(), "safe fstab editing") {
		t.Fatalf("last --lang= not applied: %q", buffers.stdout.String())
	}
}
