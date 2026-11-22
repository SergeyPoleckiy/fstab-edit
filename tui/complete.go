// Файл complete.go содержит чистую логику автодополнения формы записи
// (ТЗ §5.3): подсказки устройства из списка lsblk (с приоритетом UUID= /
// LABEL= / путь), фильтрацию вариантов по префиксу и контекстно-зависимый
// набор опций монтирования (базовый перечень плюс добавки по типу ФС и
// исключения бессмысленных опций, например discard для сетевых ФС).
// Функции не зависят от bubbletea и unit-тестируются напрямую.
package tui

import (
	"strings"

	"fstab-edit/internal/sysutil"
)

// maxDeviceHintInfoLen ограничивает длину правой колонки подсказки устройства:
// панель подсказок компактна и не должна раздвигать форму на узких терминалах.
const maxDeviceHintInfoLen = 50

// deviceHint — одна подсказка панели автодополнения: что подставить в поле
// (Value) и что показать в правой колонке (Info). Для типа ФС и опций Info
// пустой: значение само говорящее. Тип назван deviceHint по контракту плана
// этапа 3, но используется для подсказок всех полей формы.
type deviceHint struct {
	Value string // подставляемое значение: UUID=<uuid> | LABEL=<label> | /dev/... | тип ФС | опция
	Info  string // правая колонка: TYPE FSTYPE SIZE [MOUNTPOINT] [LABEL=...] (только для устройств)
}

// deviceHints собирает подсказки устройства для поля device по плоскому списку
// lsblk (дерево разворачивает sysutil.Flatten до вызова). Порядок подсказок —
// порядок устройств в списке, чтобы панель совпадала с экраном B.
func deviceHints(devices []sysutil.BlockDevice) []deviceHint {
	hints := make([]deviceHint, 0, len(devices))
	for _, device := range devices {
		hints = append(hints, deviceHint{
			Value: deviceHintValue(device),
			Info:  deviceHintInfo(device),
		})
	}
	return hints
}

// deviceHintValue выбирает подставляемое значение по приоритету ТЗ п.10:
// UUID=<uuid> — устойчиво к переименованию устройств, далее LABEL=<label>,
// и только при отсутствии обоих — путь /dev/.... Устройство без ФС и без
// UUID/LABEL (например, чистый диск) подставляется путём.
func deviceHintValue(device sysutil.BlockDevice) string {
	switch {
	case device.UUID != "":
		return "UUID=" + device.UUID
	case device.Label != "":
		return "LABEL=" + device.Label
	default:
		return device.Path
	}
}

// deviceHintInfo собирает правую колонку подсказки: TYPE, FSType и Size —
// всегда (пустые части пропускаются, без двойных пробелов), затем точка
// монтирования и метка — пока строка укладывается в maxDeviceHintInfoLen.
// Ограничение мягкое: лишнее урезается, а не режется посередине слова.
func deviceHintInfo(device sysutil.BlockDevice) string {
	parts := make([]string, 0, 5)
	for _, part := range []string{device.Type, device.FSType, device.Size} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	info := strings.Join(parts, " ")
	if device.MountPoint != "" {
		// Точка монтирования приписывается первой: она информативнее метки.
		if candidate := info + " " + device.MountPoint; len(candidate) <= maxDeviceHintInfoLen {
			info = candidate
		}
	}
	if device.Label != "" {
		// Метка — последний суффикс: она дублирует подставляемое значение
		// LABEL=<label> и важна только как пояснение.
		if candidate := info + " LABEL=" + device.Label; len(candidate) <= maxDeviceHintInfoLen {
			info = candidate
		}
	}
	return info
}

// filterHints возвращает кандидатов, начинающихся с prefix, регистронезависимо.
// Пустой prefix пропускает всех кандидатов; порядок списка сохраняется,
// потому что он несёт смысл (базовые опции — до per-FS добавок). Возвращается
// копия: вызывающий код может безопасно хранить результат между пересчётами.
func filterHints(prefix string, candidates []string) []string {
	if prefix == "" {
		return append([]string(nil), candidates...)
	}
	lower := strings.ToLower(prefix)
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.HasPrefix(strings.ToLower(candidate), lower) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

// baseMountOptions — базовый набор популярных опций монтирования (ТЗ §6.3).
// Порядок важен: при пустом префиксе панель показывает первые десять именно
// этих опций, а не случайный срез алфавита.
var baseMountOptions = []string{
	"defaults", "noatime", "nofail", "ro", "rw", "user", "auto", "noauto",
	"exec", "noexec", "sync", "async", "relatime", "nodev", "nosuid",
	"discard", "nosymfollow", "loop", "offset", "size", "mode", "uid", "gid",
}

// fsSpecificMountOptions — per-FS добавки к базовому набору (ТЗ §6.3): опции,
// которые имеют смысл только для конкретных типов ФС. Ключи совпадают с
// перечнем KnownFSTypes; fuse.-префикс снимается перед поиском (fuse.sshfs
// обслуживается как sshfs).
var fsSpecificMountOptions = map[string][]string{
	"ext2":    {"commit", "data", "journal", "errors"},
	"ext3":    {"commit", "data", "journal", "errors"},
	"ext4":    {"commit", "data", "journal", "errors"},
	"xfs":     {"allocsize", "inode64"},
	"btrfs":   {"compress", "subvol", "subvolid", "autodefrag"},
	"nfs":     {"rsize", "wsize", "timeo", "retrans", "noresvport", "soft", "hard"},
	"nfs4":    {"rsize", "wsize", "timeo", "retrans", "noresvport", "soft", "hard"},
	"cifs":    {"credentials", "vers", "sec", "dir_mode", "file_mode", "iocharset"},
	"smbfs":   {"credentials", "vers", "sec", "dir_mode", "file_mode", "iocharset"},
	"tmpfs":   {"size", "mode", "nr_inodes"},
	"vfat":    {"utf8", "umask", "dmask", "fmask", "flush"},
	"exfat":   {"utf8", "umask", "dmask", "fmask", "flush"},
	"msdos":   {"utf8", "umask", "dmask", "fmask", "flush"},
	"ntfs":    {"windows_names", "big_writes", "locale"},
	"ntfs-3g": {"windows_names", "big_writes", "locale"},
	// ntfs3 — драйвер ядра (не FUSE ntfs-3g): опции nls/sparse/prealloc,
	// discard там осмыслен и предлагается (ntfs3 НЕ входит в noDiscardFSTypes).
	"ntfs3": {"nls", "sparse", "prealloc", "discard"},
}

// noDiscardFSTypes перечисляет типы ФС, для которых опция discard не
// предлагается (ТЗ §6.3): на сетевых и псевдо-ФС discard бессмысленен или не
// поддерживается, поэтому подсказка не должна подсовывать лишнее.
var noDiscardFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smbfs": true, "sshfs": true,
	"overlay": true, "bind": true, "tmpfs": true, "proc": true, "sysfs": true,
	"devpts": true, "swap": true, "auto": true,
}

// optionHints возвращает варианты подстановки для поля options с учётом типа
// ФС и уже введённого значения. Префикс фильтрации — последний токен current
// после запятой: "defaults,noa" фильтрует по "noa". Пробелы после запятой
// обрезаются ("defaults, noa" — тот же префикс "noa"): при подстановке
// applyHint сохраняет только текст до запятой включительно, так что лишний
// пробел не копируется в результат. Возвращаются полные токены (не префикс+
// хвост): подстановка заменяет последний токен целиком. Набор — базовый плюс
// per-FS добавки минус контекстно исключённые опции.
func optionHints(fsType, current string) []string {
	// Префикс — последний токен после запятой; до запятой значение не трогаем.
	prefix := current
	if idx := strings.LastIndex(current, ","); idx >= 0 {
		prefix = strings.TrimLeft(current[idx+1:], " ")
	}
	// Нормализация типа ФС: поле формы может содержать мусировку регистра,
	// краевые пробелы или fuse.-префикс ("EXT4", "ext4 ", "fuse.sshfs") —
	// lookup идёт по чистому нижнему регистру без префикса.
	normalized := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fsType)), "fuse.")

	candidates := make([]string, 0, len(baseMountOptions)+8)
	seen := make(map[string]bool, len(baseMountOptions)+8)
	appendUnique := func(options ...string) {
		for _, option := range options {
			if !seen[option] {
				seen[option] = true
				candidates = append(candidates, option)
			}
		}
	}
	appendUnique(baseMountOptions...)
	appendUnique(fsSpecificMountOptions[normalized]...)

	if noDiscardFSTypes[normalized] {
		// discard исключён из предложений: фильтруем его из собранного набора,
		// а не из результата — так исключение срабатывает и при пустом префиксе.
		without := candidates[:0]
		for _, candidate := range candidates {
			if candidate != "discard" {
				without = append(without, candidate)
			}
		}
		candidates = without
	}
	return filterHints(prefix, candidates)
}
