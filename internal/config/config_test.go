// Пакет config: тесты загрузчика конфигурации.
// Все файловые пути — только в t.TempDir(); реальные системные пути
// (/etc/fstab-edit, home пользователя) не читаются и не пишутся.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// testLookupUser подменяет LookupUser: uid 0 → root с home из tempdir,
// иначе — пользователь с home из tempdir.
func testLookupUser(home string) func(int) (string, string, error) {
	return func(uid int) (string, string, error) {
		if uid == 0 {
			return "root", home, nil
		}
		return "user", home, nil
	}
}

// testEUID — effective UID текущего процесса. Файлы t.TempDir() всегда
// принадлежат ему, поэтому загрузку в тестах вызываем с ним: хардкод
// конкретного uid (1000) ломал прогон на любой машине, где uid пользователя
// иной (GitHub Actions использует uid 1001 — отсюда и выбор os.Geteuid()).
var testEUID = os.Geteuid()

// writeConfig пишет YAML в tempdir и возвращает путь.
func writeConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeUserConfig создаёт user config в home из tempdir и возвращает путь.
func writeUserConfig(t *testing.T, home, content string) string {
	t.Helper()
	dir := filepath.Join(home, ".config", "fstab-edit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return writeConfig(t, dir, "config.yaml", content)
}

// parseFileConfig декодирует YAML-фрагмент в fileConfig; нужен unit-тестам
// слияния уровней (mergeFileConfig), которым файловая система не требуется.
func parseFileConfig(t *testing.T, content string) *fileConfig {
	t.Helper()
	var fc fileConfig
	if err := yaml.Unmarshal([]byte(content), &fc); err != nil {
		t.Fatalf("parseFileConfig %q: %v", content, err)
	}
	return &fc
}

// stubSystemConfig перенаправляет systemConfigPath в несуществующий файл
// tempdir, чтобы тесты никогда не читали реальный /etc/fstab-edit.
func stubSystemConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := systemConfigPath
	systemConfigPath = path
	t.Cleanup(func() { systemConfigPath = old })
	return path
}

// TestLoadDefaults проверяет, что при пустом окружении и отсутствующих
// config-файлах Load возвращает значения Default() без warning'ов.
func TestLoadDefaults(t *testing.T) {
	stubSystemConfig(t)
	s, warnings, err := Load(LoadOptions{Environ: nil, EUID: testEUID, LookupUser: testLookupUser(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if s.FstabPath != "/etc/fstab" || s.LogFile != "/var/log/fstab-edit.log" ||
		s.LogLevel != "info" || s.LogMaxSizeMB != 10 || s.LogKeep != 5 ||
		s.BackupKeep != 10 || s.Lang != "ru" || s.Output != "" {
		t.Fatalf("defaults: %+v", s)
	}
}

// TestLoadPriorityCLIEnvUserSystemDefaults проверяет лестницу приоритетов:
// env выше user/system, user выше system, system доезжает своими ключами.
// Системная и пользовательская части разнесены по subtest'ам: system config
// обязан принадлежать root (может создать только root-процесс), а user
// config при Load(testEUID) обязан принадлежать этому же uid (при root-прогоне
// файлы в tempdir принадлежат uid 0) — одновременно обе части в одном
// окружении воспроизвести невозможно.
func TestLoadPriorityCLIEnvUserSystemDefaults(t *testing.T) {
	t.Run("env-over-user", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("user config при Load(testEUID) должен принадлежать testEUID; под root файлы tempdir принадлежат uid 0")
		}
		stubSystemConfig(t)
		home := t.TempDir()
		writeUserConfig(t, home, "fstab_path: /from-user\nbackup:\n  keep: 3\n")

		s, _, err := Load(LoadOptions{
			Environ:    []string{"FSTAB_EDIT_FILE=/from-env"},
			EUID:       testEUID,
			LookupUser: testLookupUser(home),
		})
		if err != nil {
			t.Fatal(err)
		}
		if s.FstabPath != "/from-env" {
			t.Fatalf("FstabPath: got %q, want env value /from-env", s.FstabPath)
		}
		if s.BackupKeep != 3 {
			t.Fatalf("BackupKeep: got %d, want user value 3", s.BackupKeep)
		}
	})

	t.Run("system-and-env", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("system config должен принадлежать root; создать root-owned файл может только root-процесс")
		}
		systemPath := stubSystemConfig(t)
		writeConfig(t, filepath.Dir(systemPath), "config.yaml",
			"fstab_path: /from-system\nlog:\n  level: warn\n")

		s, _, err := Load(LoadOptions{
			Environ:    []string{"FSTAB_EDIT_FILE=/from-env"},
			EUID:       testEUID,
			LookupUser: testLookupUser(t.TempDir()),
		})
		if err != nil {
			t.Fatal(err)
		}
		if s.FstabPath != "/from-env" {
			t.Fatalf("FstabPath: got %q, want env value /from-env", s.FstabPath)
		}
		if s.LogLevel != "warn" {
			t.Fatalf("LogLevel: got %q, want system value warn", s.LogLevel)
		}
		if s.BackupKeep != 10 {
			t.Fatalf("BackupKeep: got %d, want default 10", s.BackupKeep)
		}
	})
}

// TestLoadExplicitPath проверяет, что --config загружает файл вне
// стандартных путей, system/user конфиги при этом не читаются, а непустой
// ключ explicit перекрывается env.
func TestLoadExplicitPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("при root-прогоне tempdir-файлы принадлежат uid 0 и не проходят ownership при Load(testEUID)")
	}
	systemPath := stubSystemConfig(t)
	// system config существует и валиден, но не должен быть прочитан.
	writeConfig(t, filepath.Dir(systemPath), "config.yaml", "fstab_path: /from-system\n")

	home := t.TempDir()
	writeUserConfig(t, home, "fstab_path: /from-user\n")

	explicit := writeConfig(t, t.TempDir(), "explicit.yaml",
		"fstab_path: /from-explicit\nlog:\n  level: debug\n")

	s, _, err := Load(LoadOptions{
		ExplicitPath: explicit,
		Environ:      []string{"FSTAB_EDIT_FILE=/from-env"},
		EUID:         testEUID,
		LookupUser:   testLookupUser(home),
	})
	if err != nil {
		t.Fatal(err)
	}
	// env перекрывает непустой ключ explicit.
	if s.FstabPath != "/from-env" {
		t.Fatalf("FstabPath: got %q, want env value /from-env", s.FstabPath)
	}
	// Ключ, заданный только в explicit, доезжает.
	if s.LogLevel != "debug" {
		t.Fatalf("LogLevel: got %q, want explicit value debug", s.LogLevel)
	}
	// system/user конфиги не читались.
	if s.BackupKeep != 10 {
		t.Fatalf("BackupKeep: got %d, want default 10 (user config не читался)", s.BackupKeep)
	}
}

// TestLoadUnknownEnvWarns проверяет, что неизвестная FSTAB_EDIT_* даёт ровно
// одну warning и не меняет Settings.
func TestLoadUnknownEnvWarns(t *testing.T) {
	stubSystemConfig(t)
	s, warnings, err := Load(LoadOptions{
		Environ:    []string{"FSTAB_EDIT_FOO=bar"},
		EUID:       testEUID,
		LookupUser: testLookupUser(t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings: got %d, want ровно 1: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "FSTAB_EDIT_FOO") {
		t.Fatalf("warning не упоминает переменную: %q", warnings[0])
	}
	if !reflect.DeepEqual(s, Default()) {
		t.Fatalf("Settings изменились: %+v", s)
	}
}

// TestLoadUnknownYAMLKeyErrors проверяет, что неизвестный ключ в explicit
// config отклоняется явной ошибкой (strict YAML keys).
func TestLoadUnknownYAMLKeyErrors(t *testing.T) {
	stubSystemConfig(t)
	explicit := writeConfig(t, t.TempDir(), "explicit.yaml", "nonsense: 1\n")
	_, _, err := Load(LoadOptions{
		ExplicitPath: explicit,
		Environ:      nil,
		EUID:         testEUID,
		LookupUser:   testLookupUser(t.TempDir()),
	})
	if err == nil {
		t.Fatal("ожидалась ошибка на неизвестный ключ nonsense")
	}
}

// TestLoadInvalidValuesError проверяет отклонение некорректных резолвленных
// значений: log.level, lang и output вне допустимых множеств.
func TestLoadInvalidValuesError(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		environ []string
	}{
		{name: "log.level из YAML", yaml: "log:\n  level: trace\n"},
		{name: "lang из env", yaml: "", environ: []string{"FSTAB_EDIT_LANG=de"}},
		{name: "output из env", yaml: "", environ: []string{"FSTAB_EDIT_OUTPUT=xml"}},
		{name: "tui.theme из YAML", yaml: "tui:\n  theme: neon\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSystemConfig(t)
			opts := LoadOptions{Environ: tc.environ, EUID: testEUID, LookupUser: testLookupUser(t.TempDir())}
			if tc.yaml != "" {
				opts.ExplicitPath = writeConfig(t, t.TempDir(), "explicit.yaml", tc.yaml)
			}
			_, _, err := Load(opts)
			if err == nil {
				t.Fatalf("ожидалась ошибка валидации (%s)", tc.name)
			}
		})
	}
}

// TestLoadValidTUIThemes проверяет, что все четыре допустимые темы TUI
// (ТЗ §9.6: dark, light, high-contrast, monochrome) принимаются валидацией
// ключа tui.theme и резолвятся в Settings.TUITheme.
func TestLoadValidTUIThemes(t *testing.T) {
	for _, theme := range []string{"dark", "light", "high-contrast", "monochrome"} {
		t.Run(theme, func(t *testing.T) {
			stubSystemConfig(t)
			opts := LoadOptions{
				ExplicitPath: writeConfig(t, t.TempDir(), "explicit.yaml", "tui:\n  theme: "+theme+"\n"),
				Environ:      nil,
				EUID:         testEUID,
				LookupUser:   testLookupUser(t.TempDir()),
			}
			s, _, err := Load(opts)
			if err != nil {
				t.Fatalf("тема %s должна приниматься: %v", theme, err)
			}
			if s.TUITheme != theme {
				t.Fatalf("TUITheme: got %q, want %q", s.TUITheme, theme)
			}
		})
	}
}

// TestLoadInvalidTUIThemeMessage проверяет, что недопустимое значение
// tui.theme отклоняется ошибкой валидации с понятным сообщением, включающим
// и отклонённое значение, и список допустимых имён.
func TestLoadInvalidTUIThemeMessage(t *testing.T) {
	stubSystemConfig(t)
	_, _, err := Load(LoadOptions{
		ExplicitPath: writeConfig(t, t.TempDir(), "explicit.yaml", "tui:\n  theme: neon\n"),
		Environ:      nil,
		EUID:         testEUID,
		LookupUser:   testLookupUser(t.TempDir()),
	})
	if err == nil {
		t.Fatal("ожидалась ошибка валидации tui.theme")
	}
	msg := err.Error()
	if !strings.Contains(msg, "neon") {
		t.Fatalf("ошибка должна содержать отклонённое значение: %q", msg)
	}
	for _, want := range []string{"dark", "light", "high-contrast", "monochrome"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("ошибка должна перечислять допустимую тему %s: %q", want, msg)
		}
	}
}

// TestLoadRejectsSymlink проверяет, что explicit config, являющийся symlink,
// отклоняется ошибкой security.
func TestLoadRejectsSymlink(t *testing.T) {
	stubSystemConfig(t)
	dir := t.TempDir()
	target := writeConfig(t, dir, "real.yaml", "fstab_path: /from-real\n")
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(LoadOptions{
		ExplicitPath: link,
		Environ:      nil,
		EUID:         testEUID,
		LookupUser:   testLookupUser(t.TempDir()),
	})
	if err == nil {
		t.Fatal("ожидалась ошибка на symlink config")
	}
	var loadErr *LoadError
	if !errors.As(err, &loadErr) || loadErr.Kind != "security" {
		t.Fatalf("ожидался LoadError Kind=security, got: %v", err)
	}
}

// TestLoadRootSkipsUserConfig проверяет, что при EUID=0 user config не
// загружается, даже если он задан и валиден.
func TestLoadRootSkipsUserConfig(t *testing.T) {
	stubSystemConfig(t)
	home := t.TempDir()
	writeUserConfig(t, home, "fstab_path: /from-user\nbackup:\n  keep: 3\n")

	s, _, err := Load(LoadOptions{
		Environ:    nil,
		EUID:       0,
		LookupUser: testLookupUser(home),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.FstabPath != "/etc/fstab" || s.BackupKeep != 10 {
		t.Fatalf("значения user config просочились при EUID=0: %+v", s)
	}
}

// TestLoadUserConfigOwnershipAndMode проверяет security-проверки user config:
// group-writable файл отклоняется, owner, не совпадающий с euid, отклоняется.
func TestLoadUserConfigOwnershipAndMode(t *testing.T) {
	t.Run("group-writable", func(t *testing.T) {
		stubSystemConfig(t)
		home := t.TempDir()
		path := writeUserConfig(t, home, "fstab_path: /from-user\n")
		// 0o660 = rw-rw----: настоящий group-writable (0o640 группе только
		// на чтение и не должен отклоняться).
		if err := os.Chmod(path, 0o660); err != nil {
			t.Fatal(err)
		}
		_, _, err := Load(LoadOptions{Environ: nil, EUID: testEUID, LookupUser: testLookupUser(home)})
		if err == nil {
			t.Fatal("ожидалась ошибка на group-writable user config")
		}
	})

	t.Run("owner-mismatch", func(t *testing.T) {
		stubSystemConfig(t)
		home := t.TempDir()
		// Файл создан текущим процессом (uid=os.Geteuid()); Load вызываем
		// с другим EUID — владелец не совпадает с effective UID.
		writeUserConfig(t, home, "fstab_path: /from-user\n")
		otherEUID := os.Geteuid() + 1
		_, _, err := Load(LoadOptions{Environ: nil, EUID: otherEUID, LookupUser: testLookupUser(home)})
		if err == nil {
			t.Fatalf("ожидалась ошибка на owner-mismatch (euid=%d)", otherEUID)
		}
	})
}

// TestLoadSystemConfigOwnership проверяет security-проверки system config:
// group-writable файл отклоняется, а владелец, отличный от root, отклоняется.
// Путь system config подменяется через package-level var на tempdir.
func TestLoadSystemConfigOwnership(t *testing.T) {
	t.Run("group-writable", func(t *testing.T) {
		systemPath := stubSystemConfig(t)
		writeConfig(t, filepath.Dir(systemPath), "config.yaml", "log:\n  level: warn\n")
		if err := os.Chmod(systemPath, 0o660); err != nil {
			t.Fatal(err)
		}
		_, _, err := Load(LoadOptions{Environ: nil, EUID: testEUID, LookupUser: testLookupUser(t.TempDir())})
		if err == nil {
			t.Fatal("ожидалась ошибка на group-writable system config")
		}
	})

	// owner-mismatch возможен только когда тест не запущен от root: тогда
	// любой созданный файл принадлежит не root и system-конфиг отклоняется.
	t.Run("owner-mismatch", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("тест требует непривилегированного пользователя")
		}
		systemPath := stubSystemConfig(t)
		writeConfig(t, filepath.Dir(systemPath), "config.yaml", "log:\n  level: warn\n")
		_, _, err := Load(LoadOptions{Environ: nil, EUID: testEUID, LookupUser: testLookupUser(t.TempDir())})
		if err == nil {
			t.Fatal("ожидалась ошибка: system config не принадлежит root")
		}
	})
}

// TestLookupHomeByEffectiveUID проверяет, что user config ищется в home,
// возвращённом LookupUser по effective UID, а не в непроверенном HOME.
func TestLookupHomeByEffectiveUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("при root-прогоне tempdir-файлы принадлежат uid 0 и не проходят ownership при Load(testEUID)")
	}
	stubSystemConfig(t)
	envHome := t.TempDir()
	writeUserConfig(t, envHome, "fstab_path: /from-home-env\n")
	passwdHome := t.TempDir()
	writeUserConfig(t, passwdHome, "fstab_path: /from-passwd-home\n")

	s, _, err := Load(LoadOptions{
		Environ:    []string{"HOME=" + envHome},
		EUID:       testEUID,
		LookupUser: testLookupUser(passwdHome),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.FstabPath != "/from-passwd-home" {
		t.Fatalf("FstabPath: got %q, want user config из home LookupUser", s.FstabPath)
	}
}

// TestLoadTUIColorsValid проверяет, что tui.colors со всеми тринадцатью
// ролями и всеми тремя форматами цвета (hex #rrggbb любого регистра, номер
// ANSI 0–255, имя ANSI-цвета включая bright-варианты) принимается валидацией
// и резолвится в Settings.TUIColors без изменений (имена не нормализуются:
// конвертация в номера — при применении в tui.LoadTheme).
func TestLoadTUIColorsValid(t *testing.T) {
	stubSystemConfig(t)
	yaml := `
tui:
  theme: dark
  colors:
    title: "#00ffff"
    header: "#FF00AA"
    selected_fg: "0"
    selected_bg: "255"
    muted: black
    info: bright_white
    dirty: red
    error: "196"
    warning: bright_red
    field_source: "#abcdef"
    field_mount: "28"
    field_fstype: bright_yellow
    field_options: "007"
`
	s, _, err := Load(LoadOptions{
		ExplicitPath: writeConfig(t, t.TempDir(), "explicit.yaml", yaml),
		Environ:      nil,
		EUID:         testEUID,
		LookupUser:   testLookupUser(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("валидная палитра должна приниматься: %v", err)
	}
	want := map[string]string{
		"title": "#00ffff", "header": "#FF00AA", "selected_fg": "0",
		"selected_bg": "255", "muted": "black", "info": "bright_white",
		"dirty": "red", "error": "196", "warning": "bright_red",
		"field_source": "#abcdef", "field_mount": "28",
		"field_fstype": "bright_yellow", "field_options": "007",
	}
	if !reflect.DeepEqual(s.TUIColors, want) {
		t.Fatalf("TUIColors = %+v, want %+v", s.TUIColors, want)
	}
}

// TestLoadTUIColorsEmptyMapValid проверяет, что пустой блок colors валиден:
// он материализуется в пустую мапу (не nil — ключ был задан явно), что
// эквивалентно отсутствию переопределений.
func TestLoadTUIColorsEmptyMapValid(t *testing.T) {
	stubSystemConfig(t)
	s, _, err := Load(LoadOptions{
		ExplicitPath: writeConfig(t, t.TempDir(), "explicit.yaml", "tui:\n  colors: {}\n"),
		Environ:      nil,
		EUID:         testEUID,
		LookupUser:   testLookupUser(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("пустой tui.colors должен приниматься: %v", err)
	}
	if s.TUIColors == nil || len(s.TUIColors) != 0 {
		t.Fatalf("TUIColors = %+v, want пустая не-nil мапа", s.TUIColors)
	}
}

// TestLoadTUIColorsInvalid проверяет табличные отказы валидации tui.colors:
// неизвестная роль (в любом регистре) и цвета невалидного формата — частичные
// hex (#rgb, 5/8 цифр, не-hex символы), числа вне 0–255, строки с не-цифрами,
// неизвестные имена, регистр имён, пробелы, пустые значения. Каждый кейс даёт
// *TUIColorError с причиной и отклонённой парой.
func TestLoadTUIColorsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		yaml   string
		reason TUIColorReason
		role   string
		value  string
	}{
		{"неизвестная роль", "tui:\n  colors:\n    nonsense: red\n", TUIColorUnknownRole, "nonsense", "red"},
		{"роль в верхнем регистре", "tui:\n  colors:\n    Title: red\n", TUIColorUnknownRole, "Title", "red"},
		{"роль severity", "tui:\n  colors:\n    sev_error: red\n", TUIColorUnknownRole, "sev_error", "red"},
		{"hex сокращённый #rgb", "tui:\n  colors:\n    title: \"#0f0\"\n", TUIColorInvalidColor, "title", "#0f0"},
		{"hex пять цифр", "tui:\n  colors:\n    title: \"#00ff0\"\n", TUIColorInvalidColor, "title", "#00ff0"},
		{"hex восемь цифр", "tui:\n  colors:\n    title: \"#00ff00f\"\n", TUIColorInvalidColor, "title", "#00ff00f"},
		{"hex не-hex символ", "tui:\n  colors:\n    title: \"#00ff0g\"\n", TUIColorInvalidColor, "title", "#00ff0g"},
		{"hex без решётки", "tui:\n  colors:\n    title: 00ff00\n", TUIColorInvalidColor, "title", "00ff00"},
		{"номер 256", "tui:\n  colors:\n    title: \"256\"\n", TUIColorInvalidColor, "title", "256"},
		{"отрицательный номер", "tui:\n  colors:\n    title: \"-1\"\n", TUIColorInvalidColor, "title", "-1"},
		{"номер со знаком", "tui:\n  colors:\n    title: \"+5\"\n", TUIColorInvalidColor, "title", "+5"},
		{"номер с буквой", "tui:\n  colors:\n    title: \"12a\"\n", TUIColorInvalidColor, "title", "12a"},
		{"пустое значение", "tui:\n  colors:\n    title: \"\"\n", TUIColorInvalidColor, "title", ""},
		{"неизвестное имя", "tui:\n  colors:\n    title: gray\n", TUIColorInvalidColor, "title", "gray"},
		{"имя в CamelCase", "tui:\n  colors:\n    title: Bright_Red\n", TUIColorInvalidColor, "title", "Bright_Red"},
		{"имя с пробелом", "tui:\n  colors:\n    title: \"red \"\n", TUIColorInvalidColor, "title", "red "},
		{"пробел перед номером", "tui:\n  colors:\n    title: \" 12\"\n", TUIColorInvalidColor, "title", " 12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSystemConfig(t)
			explicitPath := writeConfig(t, t.TempDir(), "explicit.yaml", tc.yaml)
			_, _, err := Load(LoadOptions{
				ExplicitPath: explicitPath,
				Environ:      nil,
				EUID:         testEUID,
				LookupUser:   testLookupUser(t.TempDir()),
			})
			if err == nil {
				t.Fatalf("ожидался отказ валидации tui.colors (%s)", tc.name)
			}
			var colorErr *TUIColorError
			if !errors.As(err, &colorErr) {
				t.Fatalf("ошибка должна быть *TUIColorError, got: %T %v", err, err)
			}
			if colorErr.Reason != tc.reason {
				t.Fatalf("Reason = %q, want %q", colorErr.Reason, tc.reason)
			}
			if colorErr.Role != tc.role || colorErr.Value != tc.value {
				t.Fatalf("отклонённая пара = (%q, %q), want (%q, %q)",
					colorErr.Role, colorErr.Value, tc.role, tc.value)
			}
			// Load обязан сообщить путь файла-источника: сообщение об ошибке
			// называет файл, без него пользователь автозагрузки не поймёт,
			// какая из конфигураций отклонена.
			if colorErr.Path != explicitPath {
				t.Fatalf("Path = %q, want %q (путь explicit-конфига)", colorErr.Path, explicitPath)
			}
		})
	}
}

// TestLoadTUIColorsLeadingZeros фиксирует границу «номер ANSI с ведущими
// нулями»: "00" и "007" — валидные номера (strconv.Atoi даёт 0 и 7 в
// диапазоне 0–255). Регрессия здесь дешевле дисциплины: явный кейс
// запрещает будущему рефактору валидации случайно отклонить ведущие нули.
func TestLoadTUIColorsLeadingZeros(t *testing.T) {
	stubSystemConfig(t)
	yaml := `
tui:
  colors:
    info: "00"
    warning: "007"
`
	s, _, err := Load(LoadOptions{
		ExplicitPath: writeConfig(t, t.TempDir(), "explicit.yaml", yaml),
		Environ:      nil,
		EUID:         testEUID,
		LookupUser:   testLookupUser(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("номера с ведущими нулями должны приниматься: %v", err)
	}
	if s.TUIColors["info"] != "00" || s.TUIColors["warning"] != "007" {
		t.Fatalf("TUIColors = %+v, want info=00 warning=007", s.TUIColors)
	}
}

// TestLoadInvalidTUIColorsMessage проверяет, что сообщения об отказе палитры
// содержат отклонённую роль/значение и (для неизвестной роли) полный список
// допустимых ролей — пользователь должен понять причину без чтения кода.
func TestLoadInvalidTUIColorsMessage(t *testing.T) {
	stubSystemConfig(t)
	_, _, err := Load(LoadOptions{
		ExplicitPath: writeConfig(t, t.TempDir(), "explicit.yaml", "tui:\n  colors:\n    nonsense: red\n"),
		Environ:      nil,
		EUID:         testEUID,
		LookupUser:   testLookupUser(t.TempDir()),
	})
	if err == nil {
		t.Fatal("ожидался отказ на неизвестную роль")
	}
	msg := err.Error()
	for _, want := range []string{"nonsense", "title", "header", "selected_fg", "field_options"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("сообщение должно содержать %q: %q", want, msg)
		}
	}

	stubSystemConfig(t)
	_, _, err = Load(LoadOptions{
		ExplicitPath: writeConfig(t, t.TempDir(), "explicit.yaml", "tui:\n  colors:\n    title: \"#0f0\"\n"),
		Environ:      nil,
		EUID:         testEUID,
		LookupUser:   testLookupUser(t.TempDir()),
	})
	if err == nil {
		t.Fatal("ожидался отказ на невалидный формат")
	}
	msg = err.Error()
	for _, want := range []string{"#0f0", "title", "#rrggbb"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("сообщение должно содержать %q: %q", want, msg)
		}
	}
}

// TestMergeFileConfigTUIColorsMergesLevels проверяет послойное слияние
// tui.colors: роль, заданная на старшем уровне, переопределяет значение
// нижележащего уровня, а не заданные на старшем уровне роли сохраняются
// (тот же принцип «отсутствующий ключ не затирает», что и для скаляров).
func TestMergeFileConfigTUIColorsMergesLevels(t *testing.T) {
	s := Default()
	system := parseFileConfig(t, "tui:\n  colors:\n    error: \"196\"\n    title: \"#111111\"\n")
	user := parseFileConfig(t, "tui:\n  colors:\n    title: \"#00ffff\"\n")
	mergeFileConfig(&s, system)
	mergeFileConfig(&s, user)
	if s.TUIColors["title"] != "#00ffff" {
		t.Fatalf("title = %q, want user-значение #00ffff", s.TUIColors["title"])
	}
	if s.TUIColors["error"] != "196" {
		t.Fatalf("error = %q, want system-значение 196 (user-уровень его не затирал)", s.TUIColors["error"])
	}
	// Без colors на всех уровнях мапа остаётся nil: LoadTheme применяет
	// встроенную палитру без изменений.
	plain := Default()
	mergeFileConfig(&plain, parseFileConfig(t, "tui:\n  theme: light\n"))
	if plain.TUIColors != nil {
		t.Fatalf("TUIColors = %+v, want nil без ключа colors", plain.TUIColors)
	}
}
