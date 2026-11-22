// Файл config_log_test.go проверяет сквозное встраивание config и logger в
// pipeline CLI: приоритеты конфигурации (CLI > env > YAML), security-checks
// конфигурации, гарантию доступности журнала до mutation, redaction секретов
// в событиях и отсутствие файла журнала для dry-run/read-only команд. Все
// сценарии используют tempdir target/backup/config/log, memory buffers,
// инъектируемое окружение и fake runner: реальный /etc/fstab, /var/log и
// findmnt не затрагиваются.
package cmd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fstab-edit/cmd"
)

// writeTestConfig пишет YAML-конфигурацию с mode 0600 (group/others-writable
// mode отклоняется security-check) и возвращает путь.
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// testLogConfig пишет минимальный config с log.file в отдельном tempdir:
// mutating-команды открывают журнал до mutation, поэтому тесты обязаны
// направлять его во временный каталог, а не в системный /var/log.
func testLogConfig(t *testing.T) string {
	t.Helper()
	return writeTestConfig(t, "log:\n  file: "+filepath.Join(t.TempDir(), "fstab-edit.log")+"\n")
}

// newConfigApp собирает App с заданным config по умолчанию, инъектируемым
// окружением и fake runner. stdin/TTY задаются вызывающим тестом: сценарии
// подтверждения используют scripted stdin при TTY=true.
func newConfigApp(buffers *appBuffers, configPath string, environ []string,
	stdin io.Reader, tty bool,
) *cmd.App {
	return cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		Stdin:             stdin,
		TTY:               func() bool { return tty },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return environ },
		DefaultConfigPath: configPath,
	})
}

// readLogEvents читает JSON Lines журнал и декодирует каждую строку в
// map-событие; журнал обязан существовать.
func readLogEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

// addArgs возвращает аргументы валидной мутации add для target.
func addArgs(target string, extra ...string) []string {
	return append([]string{
		"add", "--file", target,
		"--device", "/dev/sdd1", "--mount", "/mnt/new", "--type", "ext4",
		"--options", "defaults",
	}, extra...)
}

// TestConfigFileAffectsBackupDir проверяет, что backup.dir из YAML-конфигурации
// определяет каталог резервной копии мутации: add --yes создаёт backup именно
// в tempdir/sub, а не рядом с target.
func TestConfigFileAffectsBackupDir(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	base := t.TempDir()
	backupDir := filepath.Join(base, "sub")
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(base, "log.jsonl")
	configPath := writeTestConfig(t, "backup:\n  dir: "+backupDir+"\nlog:\n  file: "+logFile+"\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath, nil, strings.NewReader(""), false)
	if code := app.Execute(addArgs(target, "--yes")); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if backups := listBackupData(t, backupDir); len(backups) != 1 {
		t.Fatalf("backups in config dir = %v, want exactly 1", backups)
	}
	if backups := listBackupData(t, filepath.Dir(target)); len(backups) != 0 {
		t.Fatalf("backups next to target = %v, want 0", backups)
	}
}

// TestEnvOverridesConfig проверяет, что FSTAB_EDIT_BACKUP_DIR из окружения
// перекрывает backup.dir YAML-конфигурации.
func TestEnvOverridesConfig(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	base := t.TempDir()
	yamlDir := filepath.Join(base, "yaml")
	envDir := filepath.Join(base, "env")
	for _, dir := range []string{yamlDir, envDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	logFile := filepath.Join(base, "log.jsonl")
	configPath := writeTestConfig(t, "backup:\n  dir: "+yamlDir+"\nlog:\n  file: "+logFile+"\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath,
		[]string{"FSTAB_EDIT_BACKUP_DIR=" + envDir}, strings.NewReader(""), false)
	if code := app.Execute(addArgs(target, "--yes")); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if backups := listBackupData(t, envDir); len(backups) != 1 {
		t.Fatalf("backups in env dir = %v, want exactly 1", backups)
	}
	if backups := listBackupData(t, yamlDir); len(backups) != 0 {
		t.Fatalf("backups in yaml dir = %v, want 0", backups)
	}
}

// TestCLIPriority проверяет, что --backup-dir перекрывает и env, и YAML.
func TestCLIPriority(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	base := t.TempDir()
	yamlDir := filepath.Join(base, "yaml")
	envDir := filepath.Join(base, "env")
	cliDir := filepath.Join(base, "cli")
	for _, dir := range []string{yamlDir, envDir, cliDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	logFile := filepath.Join(base, "log.jsonl")
	configPath := writeTestConfig(t, "backup:\n  dir: "+yamlDir+"\nlog:\n  file: "+logFile+"\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath,
		[]string{"FSTAB_EDIT_BACKUP_DIR=" + envDir}, strings.NewReader(""), false)
	if code := app.Execute(addArgs(target, "--backup-dir", cliDir, "--yes")); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if backups := listBackupData(t, cliDir); len(backups) != 1 {
		t.Fatalf("backups in cli dir = %v, want exactly 1", backups)
	}
	for _, dir := range []string{yamlDir, envDir} {
		if backups := listBackupData(t, dir); len(backups) != 0 {
			t.Fatalf("backups in %s = %v, want 0", dir, backups)
		}
	}
}

// TestUnknownEnvWarns проверяет, что неизвестная FSTAB_EDIT_* переменная
// печатает warning в stderr, но не меняет exit code.
func TestUnknownEnvWarns(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	configPath := testLogConfig(t)

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath,
		[]string{"FSTAB_EDIT_NOPE=1"}, strings.NewReader(""), false)
	if code := app.Execute([]string{"list", "--file", target}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	stderr := buffers.stderr.String()
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "FSTAB_EDIT_NOPE") {
		t.Fatalf("stderr missing env warning: %q", stderr)
	}
}

// TestUnsafeConfigRejected проверяет, что config с group-writable mode
// отклоняется security-check кода 3 и target остаётся байтово неизменным.
func TestUnsafeConfigRejected(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	configPath := writeTestConfig(t, "log:\n  file: "+filepath.Join(t.TempDir(), "log.jsonl")+"\n")
	if err := os.Chmod(configPath, 0o664); err != nil {
		t.Fatal(err)
	}

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath, nil, strings.NewReader(""), false)
	if code := app.Execute(addArgs(target, "--yes")); code != 3 {
		t.Fatalf("code = %d, want 3; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("unsafe config changed target")
	}
}

// TestInvalidConfigRejected проверяет, что YAML с неизвестным ключом
// отклоняется кодом 2 и target не изменён.
func TestInvalidConfigRejected(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	configPath := writeTestConfig(t, "nope: 1\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath, nil, strings.NewReader(""), false)
	if code := app.Execute(addArgs(target, "--yes")); code != 2 {
		t.Fatalf("code = %d, want 2; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("invalid config changed target")
	}
}

// TestMutationWritesLog проверяет, что успешная мутация пишет ровно одно
// JSON-событие: operation=add, result=success, непустой backup_id, target,
// duration_ms >= 0 и контекст uid процесса. Инъектируемый Now продвигается
// на 1 мс за вызов: длительность детерминированно положительна и поле
// duration_ms присутствует в JSON (omitempty скрывал бы ноль).
func TestMutationWritesLog(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	logFile := filepath.Join(t.TempDir(), "log.jsonl")
	configPath := writeTestConfig(t, "log:\n  file: "+logFile+"\n")

	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	calls := 0
	buffers := &appBuffers{}
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		Stdin:             strings.NewReader(""),
		TTY:               func() bool { return false },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: configPath,
		Now: func() time.Time {
			calls++
			return base.Add(time.Duration(calls) * time.Millisecond)
		},
	})
	if code := app.Execute(addArgs(target, "--yes")); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	events := readLogEvents(t, logFile)
	if len(events) != 1 {
		t.Fatalf("log events = %d, want 1: %v", len(events), events)
	}
	event := events[0]
	if event["operation"] != "add" || event["result"] != "success" {
		t.Fatalf("event operation/result = %v/%v, want add/success", event["operation"], event["result"])
	}
	if event["backup_id"] == "" {
		t.Fatalf("event backup_id empty: %v", event)
	}
	if event["target"] != target {
		t.Fatalf("event target = %v, want %q", event["target"], target)
	}
	duration, ok := event["duration_ms"].(float64)
	if !ok || duration < 0 {
		t.Fatalf("duration_ms = %v, want >= 0", event["duration_ms"])
	}
	for _, field := range []string{"real_uid", "effective_uid", "timestamp"} {
		if _, ok := event[field]; !ok {
			t.Fatalf("event missing %s: %v", field, event)
		}
	}
}

// TestLogUnavailableBlocksMutation проверяет гарантию доступности журнала:
// log.file в несоздаваемом месте (удалённый каталог) блокирует мутацию кодом
// 3/6, target байтово не изменён и backup не создан.
func TestLogUnavailableBlocksMutation(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	base := t.TempDir()
	logFile := filepath.Join(base, "gone", "log.jsonl") // каталог "gone" не создан
	configPath := writeTestConfig(t, "log:\n  file: "+logFile+"\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath, nil, strings.NewReader(""), false)
	code := app.Execute(addArgs(target, "--yes"))
	if code != 3 && code != 6 {
		t.Fatalf("code = %d, want 3 or 6; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("unavailable log allowed mutation")
	}
	if backups := listBackupData(t, filepath.Dir(target)); len(backups) != 0 {
		t.Fatalf("backup created despite unavailable log: %v", backups)
	}
}

// TestDryRunNoLog проверяет, что --dry-run не создаёт файл журнала вообще.
func TestDryRunNoLog(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	logFile := filepath.Join(t.TempDir(), "log.jsonl")
	configPath := writeTestConfig(t, "log:\n  file: "+logFile+"\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath, nil, strings.NewReader(""), false)
	if code := app.Execute(addArgs(target, "--dry-run")); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatalf("dry-run created log file: %v", err)
	}
}

// TestReadOnlyNoLog проверяет, что read-only команда (list) не открывает
// журнал: файла нет, несмотря на log.file в конфигурации.
func TestReadOnlyNoLog(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	logFile := filepath.Join(t.TempDir(), "log.jsonl")
	configPath := writeTestConfig(t, "log:\n  file: "+logFile+"\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath, nil, strings.NewReader(""), false)
	if code := app.Execute([]string{"list", "--file", target}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatalf("list created log file: %v", err)
	}
}

// TestLogRedactsSecrets проверяет, что значения секретов из diff маскируются
// в old_value/new_value события: исходное значение в журнале отсутствует.
func TestLogRedactsSecrets(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	logFile := filepath.Join(t.TempDir(), "log.jsonl")
	configPath := writeTestConfig(t, "log:\n  file: "+logFile+"\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath, nil, strings.NewReader(""), false)
	// password=abc — неизвестная опция (warning), но не блокирует мутацию.
	code := app.Execute([]string{
		"edit", "--file", target, "--mount-point", "/mnt/data",
		"--options", "defaults,password=abc", "--yes",
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if bytes.Contains(data, []byte("abc")) {
		t.Fatalf("log leaks secret value: %q", data)
	}
	events := readLogEvents(t, logFile)
	if len(events) != 1 {
		t.Fatalf("log events = %d, want 1", len(events))
	}
	if newValue, _ := events[0]["new_value"].(string); !strings.Contains(newValue, "password=***") {
		t.Fatalf("new_value not redacted: %q", newValue)
	}
}

// TestCancelledMutationLogged проверяет, что отмена пользователем на prompt
// фиксируется событием result=cancelled и target не изменён.
func TestCancelledMutationLogged(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	logFile := filepath.Join(t.TempDir(), "log.jsonl")
	configPath := writeTestConfig(t, "log:\n  file: "+logFile+"\n")

	buffers := &appBuffers{}
	app := newConfigApp(buffers, configPath, nil, strings.NewReader("n\n"), true)
	if code := app.Execute(addArgs(target)); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("cancelled mutation changed target")
	}
	events := readLogEvents(t, logFile)
	if len(events) != 1 {
		t.Fatalf("log events = %d, want 1: %v", len(events), events)
	}
	if events[0]["result"] != "cancelled" || events[0]["operation"] != "add" {
		t.Fatalf("cancelled event = %v, want add/cancelled", events[0])
	}
}

// TestRollbackLogged проверяет, что ручной rollback фиксирует событие
// operation=rollback с backup_id восстановленной копии и result=success.
func TestRollbackLogged(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	directory := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "log.jsonl")
	configPath := writeTestConfig(t, "log:\n  file: "+logFile+"\n")

	// Ручная копия, из которой выполняется restore. Свой config и журнал:
	// журнал rollback-запуска обязан содержать ровно одно событие rollback.
	buffers := &appBuffers{}
	app := newConfigApp(buffers, testLogConfig(t), nil, strings.NewReader(""), false)
	if code := app.Execute([]string{"backup", "--file", target, "--backup-dir", directory, "-o", "json"}); code != 0 {
		t.Fatalf("backup code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	var backupDoc struct {
		BackupID string `json:"backup_id"`
	}
	if err := json.NewDecoder(&buffers.stdout).Decode(&backupDoc); err != nil {
		t.Fatalf("decode backup JSON: %v", err)
	}
	if backupDoc.BackupID == "" {
		t.Fatal("backup_id empty")
	}

	modified := []byte(e2eFixture + "/dev/sdd1 /mnt/extra ext4 defaults 0 0\n")
	if err := os.WriteFile(target, modified, 0o600); err != nil {
		t.Fatal(err)
	}

	// Отдельный App: журнал rollback-запуска должен содержать ровно одно событие.
	buffers = &appBuffers{}
	app = newConfigApp(buffers, configPath, nil, strings.NewReader("y\n"), true)
	if code := app.Execute([]string{
		"rollback", "--file", target, "--backup-dir", directory,
		"--restore", backupDoc.BackupID, "-o", "json",
	}); code != 0 {
		t.Fatalf("rollback code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	events := readLogEvents(t, logFile)
	if len(events) != 1 {
		t.Fatalf("log events = %d, want 1: %v", len(events), events)
	}
	event := events[0]
	if event["operation"] != "rollback" || event["result"] != "success" {
		t.Fatalf("rollback event = %v, want rollback/success", event)
	}
	if event["backup_id"] != backupDoc.BackupID {
		t.Fatalf("backup_id = %v, want %q", event["backup_id"], backupDoc.BackupID)
	}
}

// TestMutationDurationMeasured проверяет, что duration_ms измеряется через
// инъектируемый Now AppOptions: продвигающиеся часы дают детерминированную
// положительную длительность, а не ноль.
func TestMutationDurationMeasured(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	logFile := filepath.Join(t.TempDir(), "log.jsonl")
	configPath := writeTestConfig(t, "log:\n  file: "+logFile+"\n")

	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	calls := 0
	buffers := &appBuffers{}
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:            &buffers.stdout,
		Stderr:            &buffers.stderr,
		Stdin:             strings.NewReader(""),
		TTY:               func() bool { return false },
		Runner:            &fakeRunner{},
		Getenv:            func(string) string { return "" },
		Environ:           func() []string { return nil },
		DefaultConfigPath: configPath,
		Now: func() time.Time {
			calls++
			return base.Add(time.Duration(calls) * time.Millisecond)
		},
	})
	if code := app.Execute(addArgs(target, "--yes")); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	events := readLogEvents(t, logFile)
	if len(events) != 1 {
		t.Fatalf("log events = %d, want 1", len(events))
	}
	if duration, _ := events[0]["duration_ms"].(float64); duration != 1 {
		t.Fatalf("duration_ms = %v, want 1 (инъектируемый Now)", events[0]["duration_ms"])
	}
}

// TestInvalidTUIColorPaletteRejected проверяет, что неизвестная роль или
// невалидный формат цвета в tui.colors отклоняются кодом 2 с локализованным
// сообщением, называющим отклонённую пару и файл-источник, и target остаётся
// неизменным. Отдельным кейсом фиксируется английский текст при --lang=en:
// EN-ветка детальных ключей защищена не только парностью TestCatalogParity,
// но и сквозным выводом.
func TestInvalidTUIColorPaletteRejected(t *testing.T) {
	cases := []struct {
		name   string
		yaml   string
		detail string
		lang   string
	}{
		{"неизвестная роль", "tui:\n  colors:\n    nonsense: red\n", "неизвестная роль цвета tui.colors \"nonsense\"", ""},
		{"невалидный цвет", "tui:\n  colors:\n    title: \"#0f0\"\n", "некорректный цвет \"#0f0\" для роли tui.colors \"title\"", ""},
		{"неизвестная роль, английский", "tui:\n  colors:\n    nonsense: red\n", "unknown tui.colors role \"nonsense\"", "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := writeTempTarget(t, e2eFixture)
			configPath := writeTestConfig(t, tc.yaml)

			buffers := &appBuffers{}
			app := newConfigApp(buffers, configPath, nil, strings.NewReader(""), false)
			args := addArgs(target, "--yes")
			if tc.lang != "" {
				args = append(args, "--lang", tc.lang)
			}
			if code := app.Execute(args); code != 2 {
				t.Fatalf("code = %d, want 2; stderr: %s", code, buffers.stderr.String())
			}
			if !strings.Contains(buffers.stderr.String(), tc.detail) {
				t.Fatalf("stderr должен содержать %q: %q", tc.detail, buffers.stderr.String())
			}
			// Сообщение обязано называть файл-источник отказа: путь явного
			// --config проброшен Load в TUIColorError.Path, пустое место
			// означало бы регрессию «в конфигурации : …».
			if !strings.Contains(buffers.stderr.String(), configPath) {
				t.Fatalf("stderr должен называть конфигурацию %q: %q", configPath, buffers.stderr.String())
			}
			if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
				t.Fatal("invalid palette changed target")
			}
		})
	}
}
