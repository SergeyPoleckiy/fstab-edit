// Файл app_test.go проверяет корневую команду App: --help/--version без
// обращения к target и runner, валидацию глобальных флагов до handler'а,
// вывод list во всех форматах и usage errors. Тесты используют только memory
// buffers, временные файлы и fake runner: реальный /etc/fstab и внешние
// утилиты не затрагиваются.
package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fstab-edit/cmd"
	"fstab-edit/internal/sysutil"
)

// recordedCall фиксирует один вызов внешней утилиты.
type recordedCall struct {
	name string
	args []string
}

// fakeRunner подменяет sysutil.CommandRunner: записывает argv каждого вызова
// и возвращает заранее заданные результаты (по умолчанию — успех без вывода).
type fakeRunner struct {
	calls   []recordedCall
	results []sysutil.RunResult
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) sysutil.RunResult {
	f.calls = append(f.calls, recordedCall{name: name, args: append([]string(nil), args...)})
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result
	}
	return sysutil.RunResult{ExitCode: 0}
}

// appBuffers собирает memory buffers для одного запуска App.
type appBuffers struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
}

// newApp создаёт App с заданным stdin/TTY и fake runner. Все writers —
// memory buffers, поэтому тесты не касаются реальных потоков процесса.
func newApp(buffers *appBuffers, stdin io.Reader, tty bool, runner *fakeRunner) *cmd.App {
	return cmd.NewApp(cmd.AppOptions{
		Stdout: &buffers.stdout,
		Stderr: &buffers.stderr,
		Stdin:  stdin,
		TTY:    func() bool { return tty },
		Runner: runner,
	})
}

// writeTempTarget записывает content во временный fstab-файл и возвращает путь.
func writeTempTarget(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// listFixture: заголовок, активная запись корневой ФС, активная запись с
// комментарием и закомментированная запись.
const listFixture = `# заголовок документа
/dev/sda1 / ext4 defaults 0 1
# данные диска
/dev/sdb1 /mnt/data ext4 defaults,noatime 0 2
# отключённая запись
# /dev/sdc1 /mnt/old ext4 defaults 0 2
`

// TestAppHelpDoesNotAccessTarget проверяет, что --help (корень и подкоманда)
// завершается кодом 0, печатает usage и не вызывает runner: даже при
// отсутствующем дефолтном /etc/fstab справка обязана работать.
func TestAppHelpDoesNotAccessTarget(t *testing.T) {
	runner := &fakeRunner{}
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, runner)

	if code := app.Execute([]string{"--help"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	output := buffers.stdout.String()
	if !strings.Contains(output, "Usage") || !strings.Contains(output, "fstab-edit") {
		t.Fatalf("help output missing usage: %q", output)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("help invoked runner: %v", runner.calls)
	}

	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, runner)
	if code := app.Execute([]string{"list", "--help"}); code != 0 {
		t.Fatalf("list --help code = %d, want 0", code)
	}
	if !strings.Contains(buffers.stdout.String(), "Usage") {
		t.Fatalf("list help output: %q", buffers.stdout.String())
	}
	if len(runner.calls) != 0 {
		t.Fatalf("list --help invoked runner: %v", runner.calls)
	}
}

// TestAppVersionDoesNotAccessTarget проверяет шаблон версии и отсутствие
// обращений к runner при --version.
func TestAppVersionDoesNotAccessTarget(t *testing.T) {
	runner := &fakeRunner{}
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, runner)

	if code := app.Execute([]string{"--version"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if got, want := buffers.stdout.String(), "fstab-edit version dev\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("--version invoked runner: %v", runner.calls)
	}
}

// TestAppGlobalFlagValidation проверяет, что некорректные комбинации
// глобальных флагов отклоняются до handler'а с кодом 2 и русским сообщением.
func TestAppGlobalFlagValidation(t *testing.T) {
	target := writeTempTarget(t, listFixture)
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"verbose quiet", []string{"--verbose", "--quiet", "list", "--file", target}, "verbose"},
		{"unknown output", []string{"-o", "xml", "list", "--file", target}, "формат"},
		{"unknown log level", []string{"--log-level", "trace", "list", "--file", target}, "log-level"},
		{"unknown lang", []string{"--lang", "de", "list", "--file", target}, "lang"},
		{"unknown flag", []string{"list", "--file", target, "--bogus"}, "флаг"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{}
			buffers := &appBuffers{}
			app := newApp(buffers, strings.NewReader(""), false, runner)
			if code := app.Execute(testCase.args); code != 2 {
				t.Fatalf("code = %d, want 2; stderr: %s", code, buffers.stderr.String())
			}
			if !strings.Contains(buffers.stderr.String(), testCase.message) {
				t.Fatalf("stderr %q does not mention %q", buffers.stderr.String(), testCase.message)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("runner invoked on usage error: %v", runner.calls)
			}
		})
	}
}

// TestListJSONSchema проверяет machine output list: schema_version, записи
// active/commented, raw/decoded поля и отсутствие comments/blanks как записей.
func TestListJSONSchema(t *testing.T) {
	target := writeTempTarget(t, listFixture)
	runner := &fakeRunner{}
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, runner)

	if code := app.Execute([]string{"list", "--file", target, "-o", "json"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	var document struct {
		SchemaVersion int `json:"schema_version"`
		Entries       []struct {
			Line       int    `json:"line"`
			State      string `json:"state"`
			MountPoint struct {
				Raw     string `json:"raw"`
				Decoded string `json:"decoded"`
				Present bool   `json:"present"`
			} `json:"mount_point"`
			Comment string `json:"comment"`
		} `json:"entries"`
	}
	decoder := json.NewDecoder(&buffers.stdout)
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode list JSON: %v; stdout: %q", err, buffers.stdout.String())
	}
	if document.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", document.SchemaVersion)
	}
	if len(document.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(document.Entries))
	}
	if document.Entries[0].Line != 2 || document.Entries[0].State != "active" {
		t.Fatalf("first entry: %+v", document.Entries[0])
	}
	if document.Entries[2].State != "commented" {
		t.Fatalf("third entry state: %+v", document.Entries[2])
	}
	if document.Entries[1].MountPoint.Decoded != "/mnt/data" || !document.Entries[1].MountPoint.Present {
		t.Fatalf("mount point view: %+v", document.Entries[1].MountPoint)
	}
	if document.Entries[1].Comment != "данные диска" {
		t.Fatalf("associated comment: %q", document.Entries[1].Comment)
	}
}

// TestListTableOutput проверяет человекочитаемую таблицу: заголовок колонок
// и содержимое записей на stdout.
func TestListTableOutput(t *testing.T) {
	target := writeTempTarget(t, listFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})

	if code := app.Execute([]string{"list", "--file", target}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	output := buffers.stdout.String()
	for _, fragment := range []string{"LINE", "STATE", "/mnt/data", "commented"} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("table output missing %q: %q", fragment, output)
		}
	}
}

// TestListQuietSuppressesStdout проверяет, что --quiet глушит обычный stdout
// успешной команды, не меняя exit code.
func TestListQuietSuppressesStdout(t *testing.T) {
	target := writeTempTarget(t, listFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})

	if code := app.Execute([]string{"list", "--file", target, "--quiet"}); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if buffers.stdout.Len() != 0 {
		t.Fatalf("quiet stdout not empty: %q", buffers.stdout.String())
	}
}
