// Package config загружает и резолвит настройки fstab-edit по приоритетам
// ТЗ §13.1: CLI > FSTAB_EDIT_* > User YAML > System YAML > Defaults.
// Пакет не читает реальные системные пути в тестах: пути и окружение
// передаются через LoadOptions, проверки собственности/прав — через
// переданный EUID. Конфигурация небезопасна — Load возвращает явную ошибку.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"fstab-edit/internal/backup"
)

// systemConfigPath — путь system config. package-level var только для
// тестовой подмены на tempdir; production код не меняет его.
var systemConfigPath = "/etc/fstab-edit/config.yaml"

// userConfigRelPath — путь user config относительно home effective UID.
const userConfigRelPath = ".config/fstab-edit/config.yaml"

// Settings — полностью резолвленные настройки одного запуска CLI.
// Lang и Output не читаются из YAML (ТЗ §13.3), только из env/CLI.
type Settings struct {
	FstabPath    string // default "/etc/fstab"
	BackupDir    string // пусто = каталог target
	BackupPrefix string
	BackupKeep   int
	LogFile      string // default "/var/log/fstab-edit.log"
	LogLevel     string // debug|info|warn|error
	LogMaxSizeMB int
	LogKeep      int
	TUITheme     string // сохраняется, на CLI не влияет
	// TUIColors — переопределения ролей палитры из tui.colors (ТЗ v2 этап 4,
	// пользовательские темы); nil = ключ colors не задан ни на одном уровне
	// и встроенная палитра темы применяется без изменений. Исходные строки
	// пользователя: конвертация имён ANSI в номера — при применении в tui.
	TUIColors map[string]string
	Lang      string // только env/CLI: ru|en
	Output    string // только env/CLI: table|json|csv|tsv
}

// fileConfig — зеркало YAML-схемы ТЗ §13.3; все поля указатели/omitempty-
// стиль, чтобы отсутствующий ключ не затирал значение низшего уровня.
type fileConfig struct {
	FstabPath *string `yaml:"fstab_path"`
	Backup    *struct {
		Dir    *string `yaml:"dir"`
		Prefix *string `yaml:"prefix"`
		Keep   *int    `yaml:"keep"`
	} `yaml:"backup"`
	Log *struct {
		File      *string `yaml:"file"`
		Level     *string `yaml:"level"`
		MaxSizeMB *int    `yaml:"max_size_mb"`
		Keep      *int    `yaml:"keep"`
	} `yaml:"log"`
	TUI *struct {
		Theme  *string            `yaml:"theme"`
		Colors *map[string]string `yaml:"colors"`
	} `yaml:"tui"`
}

// LoadError классифицирует сбои загрузки конфигурации для маппинга на exit
// codes: KindSecurity → код 3, KindInvalid → код 2.
type LoadError struct {
	Kind string // "security" | "invalid" | "io"
	Path string
	Err  error
}

// Error возвращает человекочитаемое описание сбоя с путём конфигурации.
func (e *LoadError) Error() string { return fmt.Sprintf("config %s: %v", e.Path, e.Err) }

// Unwrap возвращает исходную ошибку для errors.Is/errors.As.
func (e *LoadError) Unwrap() error { return e.Err }

// LoadOptions — injectable-границы. Production: ExplicitPath = флаг --config,
// Environ = os.Environ(), EUID = os.Geteuid(), LookupUser = LookupUserByUID.
type LoadOptions struct {
	ExplicitPath string
	Environ      []string
	EUID         int
	LookupUser   func(uid int) (username, home string, err error)
}

// Default возвращает настройки по умолчанию (нижний уровень приоритета).
func Default() Settings {
	return Settings{
		FstabPath:    "/etc/fstab",
		BackupDir:    "",
		BackupPrefix: backup.DefaultBackupPrefix,
		BackupKeep:   backup.DefaultKeepBackups,
		LogFile:      "/var/log/fstab-edit.log",
		LogLevel:     "info",
		LogMaxSizeMB: 10,
		LogKeep:      5,
		TUITheme:     "dark",
		Lang:         "ru",
		Output:       "",
	}
}

// Load резолвит Settings. Порядок: defaults → system YAML (euid != 0) →
// user YAML (euid != 0) → env → explicit path вместо system/user, но ниже env.
// warnings — непустые предупреждения (неизвестные FSTAB_EDIT_*), err — первый
// фатальный сбой.
func Load(opts LoadOptions) (Settings, []string, error) {
	s := Default()
	warnings := make([]string, 0)

	explicit := opts.ExplicitPath != ""
	if explicit {
		if err := loadFileInto(&s, opts.ExplicitPath, opts.EUID, false); err != nil {
			return s, warnings, err
		}
	} else if opts.EUID != 0 {
		// Root-процесс не загружает user config автоматически (ТЗ §13.2).
		if err := loadFileInto(&s, systemConfigPath, opts.EUID, true); err != nil {
			return s, warnings, err
		}
		home := ""
		if opts.LookupUser != nil {
			_, h, err := opts.LookupUser(opts.EUID)
			if err != nil {
				return s, warnings, &LoadError{Kind: "io", Path: userConfigRelPath,
					Err: fmt.Errorf("определить home по effective UID %d: %w", opts.EUID, err)}
			}
			home = h
		}
		if home != "" {
			if err := loadFileInto(&s, filepath.Join(home, userConfigRelPath), opts.EUID, false); err != nil {
				return s, warnings, err
			}
		}
	}

	if err := applyEnv(&s, opts.Environ, &warnings); err != nil {
		return s, warnings, err
	}
	if err := validateValues(&s); err != nil {
		return s, warnings, err
	}
	return s, warnings, nil
}

// loadFileInto читает один YAML-файл при его существовании, проверяет
// безопасность пути и вливает заданные ключи в Settings. system=true
// дополнительно требует root-ownership (проверяется для любого euid).
func loadFileInto(s *Settings, path string, euid int, system bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // отсутствующий config — не ошибка
		}
		return &LoadError{Kind: "io", Path: path, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &LoadError{Kind: "security", Path: path,
			Err: errors.New("symlink-конфигурация отклонена")}
	}
	if err := checkOwnership(path, info, euid, system); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return &LoadError{Kind: "io", Path: path, Err: err}
	}
	var fc fileConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // неизвестный ключ — явная ошибка
	if err := dec.Decode(&fc); err != nil {
		return &LoadError{Kind: "invalid", Path: path, Err: err}
	}
	mergeFileConfig(s, &fc)
	return nil
}

// checkOwnership проверяет владельца и права config-файла (ТЗ §13.2).
// Для euid==0: файл должен принадлежать root и не быть group/others-writable.
// Для euid!=0 (user/explicit config): файл должен принадлежать пользователю
// и не быть group/others-writable. System config всегда: root-owned.
func checkOwnership(path string, info os.FileInfo, euid int, system bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return &LoadError{Kind: "io", Path: path, Err: errors.New("не удалось получить stat")}
	}
	mode := info.Mode().Perm()
	if mode&0o022 != 0 { // group или others writable
		return &LoadError{Kind: "security", Path: path,
			Err: fmt.Errorf("config доступен на запись group/others (mode %o)", mode)}
	}
	wantUID := uint32(euid)
	if system {
		wantUID = 0
	}
	if stat.Uid != wantUID {
		return &LoadError{Kind: "security", Path: path,
			Err: fmt.Errorf("config принадлежит uid %d, ожидался uid %d", stat.Uid, wantUID)}
	}
	return nil
}

// mergeFileConfig вливает только заданные (non-nil) ключи fileConfig в
// Settings — отсутствующий ключ не затирает значение низшего уровня.
func mergeFileConfig(s *Settings, fc *fileConfig) {
	if fc.FstabPath != nil {
		s.FstabPath = *fc.FstabPath
	}
	if fc.Backup != nil {
		if fc.Backup.Dir != nil {
			s.BackupDir = *fc.Backup.Dir
		}
		if fc.Backup.Prefix != nil {
			s.BackupPrefix = *fc.Backup.Prefix
		}
		if fc.Backup.Keep != nil {
			s.BackupKeep = *fc.Backup.Keep
		}
	}
	if fc.Log != nil {
		if fc.Log.File != nil {
			s.LogFile = *fc.Log.File
		}
		if fc.Log.Level != nil {
			s.LogLevel = *fc.Log.Level
		}
		if fc.Log.MaxSizeMB != nil {
			s.LogMaxSizeMB = *fc.Log.MaxSizeMB
		}
		if fc.Log.Keep != nil {
			s.LogKeep = *fc.Log.Keep
		}
	}
	if fc.TUI != nil {
		if fc.TUI.Theme != nil {
			s.TUITheme = *fc.TUI.Theme
		}
		if fc.TUI.Colors != nil {
			// Слияние по элементам: роли, заданные на этом уровне,
			// переопределяют значения нижележащего уровня, не заданные здесь
			// роли сохраняются (тот же принцип «отсутствующий ключ не затирает
			// значение», что и для скаляров). Мапа материализуется лениво:
			// пока colors не задан ни на одном уровне, s.TUIColors остаётся
			// nil и LoadTheme применяет встроенную палитру без изменений.
			if s.TUIColors == nil {
				s.TUIColors = make(map[string]string, len(*fc.TUI.Colors))
			}
			for role, color := range *fc.TUI.Colors {
				s.TUIColors[role] = color
			}
		}
	}
}

// knownEnv — разрешённые FSTAB_EDIT_* и их маппинг на Settings.
var knownEnv = map[string]func(*Settings, string){
	"FSTAB_EDIT_FILE":       func(s *Settings, v string) { s.FstabPath = v },
	"FSTAB_EDIT_BACKUP_DIR": func(s *Settings, v string) { s.BackupDir = v },
	"FSTAB_EDIT_LOG_LEVEL":  func(s *Settings, v string) { s.LogLevel = v },
	"FSTAB_EDIT_LANG":       func(s *Settings, v string) { s.Lang = v },
	"FSTAB_EDIT_OUTPUT":     func(s *Settings, v string) { s.Output = v },
}

// applyEnv применяет известные FSTAB_EDIT_* поверх Settings; неизвестные
// добавляют warning в список и не меняют поведение (ТЗ §13.4).
func applyEnv(s *Settings, environ []string, warnings *[]string) error {
	for _, pair := range environ {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || !strings.HasPrefix(name, "FSTAB_EDIT_") {
			continue
		}
		apply, known := knownEnv[name]
		if !known {
			*warnings = append(*warnings,
				fmt.Sprintf("неизвестная переменная окружения %s проигнорирована", name))
			continue
		}
		apply(s, value)
	}
	return nil
}

// validateValues отклоняет некорректные резолвленные значения явной ошибкой.
func validateValues(s *Settings) error {
	switch s.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("некорректный log.level %q: допустимы debug, info, warn, error", s.LogLevel)
	}
	switch s.Lang {
	case "ru", "en":
	default:
		return fmt.Errorf("некорректный lang %q: допустимы ru, en", s.Lang)
	}
	if s.Output != "" {
		switch s.Output {
		case "table", "json", "csv", "tsv":
		default:
			return fmt.Errorf("некорректный output %q: допустимы table, json, csv, tsv", s.Output)
		}
	}
	// ТЗ §5.5/§9.6: оформление TUI выбирается ключом tui.theme; допустимые
	// имена совпадают с множеством тем пакета tui (LoadTheme). Пустое значение
	// невозможно: Default() задаёт "dark", а merge вливает только непустые ключи.
	switch s.TUITheme {
	case "dark", "light", "high-contrast", "monochrome":
	default:
		return fmt.Errorf("некорректный tui.theme %q: допустимы dark, light, high-contrast, monochrome", s.TUITheme)
	}
	// ТЗ v2 этап 4 (пользовательские темы): tui.colors — переопределения ролей
	// палитры поверх встроенной темы. Ключ опционален; неизвестная роль или
	// цвет невалидного формата отклоняются явной ошибкой TUIColorError —
	// cmd-слой маппит её на локализованное сообщение и exit code 2.
	if err := validateTUIColors(s.TUIColors); err != nil {
		return err
	}
	if s.LogMaxSizeMB <= 0 || s.LogKeep <= 0 || s.BackupKeep <= 0 {
		return errors.New("keep/max_size_mb должны быть положительными")
	}
	return nil
}

// LookupUserByUID определяет имя и home пользователя по UID через системную
// базу пользователей (effective UID, не непроверенный HOME — ТЗ §13.2).
func LookupUserByUID(uid int) (string, string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", "", err
	}
	return u.Username, u.HomeDir, nil
}
