// Пакет config: тесты загрузчика конфигурации.
// Все файловые пути — только в t.TempDir(); реальные системные пути
// (/etc/fstab-edit, home пользователя) не читаются и не пишутся.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	s, warnings, err := Load(LoadOptions{Environ: nil, EUID: 1000, LookupUser: testLookupUser(t.TempDir())})
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
// config при Load(EUID=1000) обязан принадлежать uid 1000 (при root-прогоне
// файлы в tempdir принадлежат uid 0) — одновременно обе части в одном
// окружении воспроизвести невозможно.
func TestLoadPriorityCLIEnvUserSystemDefaults(t *testing.T) {
	t.Run("env-over-user", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("user config при Load(EUID=1000) должен принадлежать uid 1000; под root файлы tempdir принадлежат uid 0")
		}
		stubSystemConfig(t)
		home := t.TempDir()
		writeUserConfig(t, home, "fstab_path: /from-user\nbackup:\n  keep: 3\n")

		s, _, err := Load(LoadOptions{
			Environ:    []string{"FSTAB_EDIT_FILE=/from-env"},
			EUID:       1000,
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
			EUID:       1000,
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
		t.Skip("при root-прогоне tempdir-файлы принадлежат uid 0 и не проходят ownership при Load(EUID=1000)")
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
		EUID:         1000,
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
		EUID:       1000,
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
	if s != Default() {
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
		EUID:         1000,
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSystemConfig(t)
			opts := LoadOptions{Environ: tc.environ, EUID: 1000, LookupUser: testLookupUser(t.TempDir())}
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
		EUID:         1000,
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
		_, _, err := Load(LoadOptions{Environ: nil, EUID: 1000, LookupUser: testLookupUser(home)})
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
		_, _, err := Load(LoadOptions{Environ: nil, EUID: 1000, LookupUser: testLookupUser(t.TempDir())})
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
		_, _, err := Load(LoadOptions{Environ: nil, EUID: 1000, LookupUser: testLookupUser(t.TempDir())})
		if err == nil {
			t.Fatal("ожидалась ошибка: system config не принадлежит root")
		}
	})
}

// TestLookupHomeByEffectiveUID проверяет, что user config ищется в home,
// возвращённом LookupUser по effective UID, а не в непроверенном HOME.
func TestLookupHomeByEffectiveUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("при root-прогоне tempdir-файлы принадлежат uid 0 и не проходят ownership при Load(EUID=1000)")
	}
	stubSystemConfig(t)
	envHome := t.TempDir()
	writeUserConfig(t, envHome, "fstab_path: /from-home-env\n")
	passwdHome := t.TempDir()
	writeUserConfig(t, passwdHome, "fstab_path: /from-passwd-home\n")

	s, _, err := Load(LoadOptions{
		Environ:    []string{"HOME=" + envHome},
		EUID:       1000,
		LookupUser: testLookupUser(passwdHome),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.FstabPath != "/from-passwd-home" {
		t.Fatalf("FstabPath: got %q, want user config из home LookupUser", s.FstabPath)
	}
}
