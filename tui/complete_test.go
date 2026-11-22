// Файл complete_test.go покрывает чистую логику автодополнения формы:
// фильтрацию вариантов по префиксу, контекстно-зависимый набор опций
// монтирования и сборку подсказок устройства из списка lsblk. Все функции
// тестируются без bubbletea: подаются готовые структуры sysutil.BlockDevice.
package tui

import (
	"slices"
	"strings"
	"testing"

	"fstab-edit/internal/sysutil"
)

// TestFilterHintsPrefix проверяет регистронезависимый prefix-match:
// префикс "ex" отбирает ext2/ext3/ext4/exfat и не пропускает btrfs,
// порядок кандидатов в результате сохраняется.
func TestFilterHintsPrefix(t *testing.T) {
	candidates := []string{"ext2", "ext3", "ext4", "exfat", "btrfs", "xfs"}
	got := filterHints("ex", candidates)
	want := []string{"ext2", "ext3", "ext4", "exfat"}
	if !slices.Equal(got, want) {
		t.Fatalf("filterHints(\"ex\"): %v, ожидалось %v", got, want)
	}
}

// TestFilterHintsCaseInsensitive проверяет, что префикс сравнивается без учёта
// регистра: "NOA" отбирает noatime/noauto.
func TestFilterHintsCaseInsensitive(t *testing.T) {
	candidates := []string{"defaults", "noatime", "noauto", "rw"}
	got := filterHints("NOA", candidates)
	want := []string{"noatime", "noauto"}
	if !slices.Equal(got, want) {
		t.Fatalf("filterHints(\"NOA\"): %v, ожидалось %v", got, want)
	}
}

// TestFilterHintsEmptyPrefix проверяет, что пустой префикс пропускает всех
// кандидатов в исходном порядке.
func TestFilterHintsEmptyPrefix(t *testing.T) {
	candidates := []string{"btrfs", "ext4", "xfs"}
	got := filterHints("", candidates)
	if !slices.Equal(got, candidates) {
		t.Fatalf("filterHints(\"\"): %v, ожидалось %v", got, candidates)
	}
}

// TestOptionHintsLastToken проверяет токенизацию: префиксом фильтрации служит
// последний токен после запятой, а подсказки — полные токены, а не префикс+хвост
// ("defaults,noa" фильтрует по "noa" и возвращает noatime/noauto целиком).
func TestOptionHintsLastToken(t *testing.T) {
	got := optionHints("ext4", "defaults,noa")
	if !slices.Equal(got, []string{"noatime", "noauto"}) {
		t.Fatalf("optionHints(ext4, \"defaults,noa\"): %v, ожидалось [noatime noauto]", got)
	}
}

// TestOptionHintsDiscardExcludedForNFS проверяет контекстное исключение ТЗ §6.3:
// для nfs опция discard не предлагается даже при пустом префиксе, а для ext4 —
// предлагается.
func TestOptionHintsDiscardExcludedForNFS(t *testing.T) {
	nfs := optionHints("nfs", "")
	if slices.Contains(nfs, "discard") {
		t.Fatalf("discard предложен для nfs: %v", nfs)
	}
	ext4 := optionHints("ext4", "di")
	if !slices.Equal(ext4, []string{"discard"}) {
		t.Fatalf("optionHints(ext4, \"di\"): %v, ожидалось [discard]", ext4)
	}
}

// TestOptionHintsFSSpecific проверяет per-FS добавки: btrfs получает compress,
// ext4 — commit, nfs — rsize (и базовый набор при этом сохраняется).
func TestOptionHintsFSSpecific(t *testing.T) {
	btrfs := optionHints("btrfs", "comp")
	if !slices.Equal(btrfs, []string{"compress"}) {
		t.Fatalf("optionHints(btrfs, \"comp\"): %v, ожидалось [compress]", btrfs)
	}
	ext4 := optionHints("ext4", "comm")
	if !slices.Equal(ext4, []string{"commit"}) {
		t.Fatalf("optionHints(ext4, \"comm\"): %v, ожидалось [commit]", ext4)
	}
	nfs := optionHints("nfs", "rs")
	if !slices.Equal(nfs, []string{"rsize"}) {
		t.Fatalf("optionHints(nfs, \"rs\"): %v, ожидалось [rsize]", nfs)
	}
	// Базовый набор никуда не исчезает: для неизвестного типа ФС остаются
	// общие опции и никаких per-FS добавок.
	base := optionHints("", "nof")
	if !slices.Equal(base, []string{"nofail"}) {
		t.Fatalf("optionHints(\"\", \"nof\"): %v, ожидалось [nofail]", base)
	}
}

// TestDeviceHintsPriorityUUID проверяет приоритет подставляемого значения
// (ТЗ v2 этап 3): UUID выигрывает у PARTUUID, LABEL, PARTLABEL и пути.
func TestDeviceHintsPriorityUUID(t *testing.T) {
	devices := []sysutil.BlockDevice{
		{Path: "/dev/sdb1", Type: "part", FSType: "vfat", Size: "16G",
			Label: "USBKEY", UUID: "BBBB-2222", MountPoint: "/mnt/usb"},
	}
	hints := deviceHints("ru", devices)
	if len(hints) != 1 {
		t.Fatalf("подсказок: %d, ожидалась 1", len(hints))
	}
	if hints[0].Value != "UUID=BBBB-2222" {
		t.Fatalf("Value: %q, ожидалось %q", hints[0].Value, "UUID=BBBB-2222")
	}
}

// TestDeviceHintsPriorityLabelAndPath проверяет fallback-цепочку: без UUID
// и PARTUUID подставляется LABEL=<label>, без всех идентификаторов —
// путь устройства.
func TestDeviceHintsPriorityLabelAndPath(t *testing.T) {
	devices := []sysutil.BlockDevice{
		{Path: "/dev/sdc1", Type: "part", FSType: "ext4", Size: "256G", Label: "DATA"},
		{Path: "/dev/sda", Type: "disk", Size: "476.9G"},
	}
	hints := deviceHints("ru", devices)
	if len(hints) != 2 {
		t.Fatalf("подсказок: %d, ожидалось 2", len(hints))
	}
	if hints[0].Value != "LABEL=DATA" {
		t.Fatalf("Value без UUID/PARTUUID: %q, ожидалось %q", hints[0].Value, "LABEL=DATA")
	}
	if hints[1].Value != "/dev/sda" {
		t.Fatalf("Value без идентификаторов: %q, ожидался путь /dev/sda", hints[1].Value)
	}
}

// TestDeviceHintsInfoFormat проверяет правую колонку: TYPE, FSTYPE и SIZE
// присутствуют всегда, точка монтирования и метка дописываются компактно
// (в пределах лимита длины).
func TestDeviceHintsInfoFormat(t *testing.T) {
	devices := []sysutil.BlockDevice{
		{Path: "/dev/sdb1", Type: "part", FSType: "vfat", Size: "16G",
			Label: "USBKEY", UUID: "BBBB-2222", MountPoint: "/mnt/usb"},
		{Path: "/dev/sda", Type: "disk", Size: "476.9G"},
	}
	hints := deviceHints("ru", devices)
	info := hints[0].Info
	for _, part := range []string{"part", "vfat", "16G", "/mnt/usb"} {
		if !strings.Contains(info, part) {
			t.Fatalf("Info %q не содержит %q", info, part)
		}
	}
	if len(info) > maxDeviceHintInfoLen {
		t.Fatalf("Info длиннее лимита: %q (%d > %d)", info, len(info), maxDeviceHintInfoLen)
	}
	// Устройство без ФС: Info — TYPE и SIZE, без пустых частей.
	bare := hints[1].Info
	if bare != "disk 476.9G" {
		t.Fatalf("Info без ФС: %q, ожидалось %q", bare, "disk 476.9G")
	}
}

// TestDeviceHintsIdentifierPriority покрывает полный приоритет подстановки
// ТЗ v2 этап 3 таблично: UUID → PARTUUID → LABEL → PARTLABEL → путь. Каждый
// случай задаёт ровно один заполненный идентификатор из оставшихся, чтобы
// цепочка fallback проверялась по одному звену за раз.
func TestDeviceHintsIdentifierPriority(t *testing.T) {
	cases := []struct {
		name   string
		device sysutil.BlockDevice
		want   string
	}{
		{
			name: "все идентификаторы → UUID",
			device: sysutil.BlockDevice{Path: "/dev/sda1", UUID: "u-1",
				PartUUID: "pu-1", Label: "l-1", PartLabel: "pl-1"},
			want: "UUID=u-1",
		},
		{
			name: "без UUID → PARTUUID",
			device: sysutil.BlockDevice{Path: "/dev/sda1",
				PartUUID: "pu-2", Label: "l-2", PartLabel: "pl-2"},
			want: "PARTUUID=pu-2",
		},
		{
			name: "без UUID и PARTUUID → LABEL",
			device: sysutil.BlockDevice{Path: "/dev/sda1",
				Label: "l-3", PartLabel: "pl-3"},
			want: "LABEL=l-3",
		},
		{
			name:   "только PARTLABEL → PARTLABEL",
			device: sysutil.BlockDevice{Path: "/dev/sda1", PartLabel: "pl-4"},
			want:   "PARTLABEL=pl-4",
		},
		{
			name:   "без идентификаторов → путь",
			device: sysutil.BlockDevice{Path: "/dev/sda1"},
			want:   "/dev/sda1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hints := deviceHints("ru", []sysutil.BlockDevice{tc.device})
			if len(hints) != 1 {
				t.Fatalf("подсказок: %d, ожидалась 1", len(hints))
			}
			if hints[0].Value != tc.want {
				t.Fatalf("Value: %q, ожидалось %q", hints[0].Value, tc.want)
			}
		})
	}
}

// TestDeviceHintsDuplicateMarker проверяет пометку повторяющихся
// идентификаторов (ТЗ v2 этап 3): если выбранное для устройства значение
// встречается более чем у одного устройства, Info получает локализованный
// суффикс-маркер; уникальные идентификаторы и fallback-пути остаются
// без пометки. Проверяются дубликаты на двух уровнях приоритета — UUID и
// PARTUUID — и оба языка каталога.
func TestDeviceHintsDuplicateMarker(t *testing.T) {
	devices := []sysutil.BlockDevice{
		// Два раздела с одинаковым UUID (типичный след клонирования диска):
		// обе подсказки обязаны быть помечены.
		{Path: "/dev/sda1", Type: "part", FSType: "ext4", Size: "100G", UUID: "DUP-UUID"},
		{Path: "/dev/sdb1", Type: "part", FSType: "xfs", Size: "200G", UUID: "DUP-UUID"},
		// Уникальный UUID — без пометки.
		{Path: "/dev/sdc1", Type: "part", FSType: "btrfs", Size: "300G", UUID: "UNIQ-UUID"},
		// Два раздела с одинаковым PARTUUID (UUID тома отсутствует, выбран
		// следующий по приоритету идентификатор): тоже помечаются.
		{Path: "/dev/sdd1", Type: "part", FSType: "ext4", Size: "400G", PartUUID: "DUP-PUUID"},
		{Path: "/dev/sde1", Type: "part", FSType: "ext4", Size: "500G", PartUUID: "DUP-PUUID"},
		// Без единого идентификатора: подставляется путь, пометка невозможна.
		{Path: "/dev/sdf", Type: "disk", Size: "600G"},
	}

	t.Run("русский маркер", func(t *testing.T) {
		hints := deviceHints("ru", devices)
		if len(hints) != len(devices) {
			t.Fatalf("подсказок: %d, ожидалось %d", len(hints), len(devices))
		}
		marker := "(не уникальный идентификатор)"
		for _, i := range []int{0, 1, 3, 4} {
			if !strings.HasSuffix(hints[i].Info, " "+marker) {
				t.Fatalf("Info[%d] %q должна оканчиваться маркером %q", i, hints[i].Info, marker)
			}
		}
		for _, i := range []int{2, 5} {
			if strings.Contains(hints[i].Info, marker) {
				t.Fatalf("Info[%d] %q не должна содержать маркер дубликата", i, hints[i].Info)
			}
		}
	})

	t.Run("английский маркер", func(t *testing.T) {
		hints := deviceHints("en", devices[:2])
		marker := "(non-unique identifier)"
		for i, hint := range hints {
			if !strings.HasSuffix(hint.Info, " "+marker) {
				t.Fatalf("Info[%d] %q должна оканчиваться маркером %q", i, hint.Info, marker)
			}
		}
	})
}

// TestOptionHintsEdgeTokens проверяет граничные токены: trailing comma
// ("defaults," — пустой последний токен даёт весь набор), leading comma
// (",noatime" — префикс "noatime") и пробел после запятой ("defaults, noa" —
// префикс "noa", панель не ломается).
func TestOptionHintsEdgeTokens(t *testing.T) {
	trailing := optionHints("ext4", "defaults,")
	if !slices.Contains(trailing, "rw") || !slices.Contains(trailing, "commit") {
		t.Fatalf("trailing comma: %v, ожидался весь набор с per-FS добавками", trailing)
	}
	leading := optionHints("ext4", ",noatime")
	if !slices.Equal(leading, []string{"noatime"}) {
		t.Fatalf("leading comma: %v, ожидалось [noatime]", leading)
	}
	spaced := optionHints("ext4", "defaults, noa")
	if !slices.Equal(spaced, []string{"noatime", "noauto"}) {
		t.Fatalf("пробел после запятой: %v, ожидалось [noatime noauto]", spaced)
	}
}

// TestOptionHintsNormalizedFSType проверяет нормализацию типа ФС: регистр
// ("EXT4") и краевые пробелы ("ext4 ") не мешают per-FS добавкам и
// контекстным исключениям.
func TestOptionHintsNormalizedFSType(t *testing.T) {
	upper := optionHints("EXT4", "comm")
	if !slices.Equal(upper, []string{"commit"}) {
		t.Fatalf("EXT4: %v, ожидалось [commit]", upper)
	}
	spaced := optionHints("ext4 ", "di")
	if !slices.Equal(spaced, []string{"discard"}) {
		t.Fatalf("«ext4 »: %v, ожидалось [discard]", spaced)
	}
	// Для NFS нормализация тоже работает: discard исключён даже с мусором.
	nfs := optionHints(" NFS4 ", "di")
	if slices.Contains(nfs, "discard") {
		t.Fatalf("discard предложен для « NFS4 »: %v", nfs)
	}
}

// TestOptionHintsNtfs3Specific проверяет per-FS добавки драйвера ядра ntfs3:
// nls/sparse/prealloc предлагаются, discard осмыслен и НЕ исключён, а опции
// FUSE ntfs-3g (windows_names, big_writes, locale) для ntfs3 не предлагаются.
func TestOptionHintsNtfs3Specific(t *testing.T) {
	if hints := optionHints("ntfs3", "nl"); !slices.Equal(hints, []string{"nls"}) {
		t.Fatalf("ntfs3 «nl»: %v, ожидалось [nls]", hints)
	}
	if hints := optionHints("ntfs3", "di"); !slices.Equal(hints, []string{"discard"}) {
		t.Fatalf("ntfs3 «di»: %v, ожидалось [discard]", hints)
	}
	all := optionHints("ntfs3", "")
	for _, fuseOnly := range []string{"windows_names", "big_writes", "locale"} {
		if slices.Contains(all, fuseOnly) {
			t.Fatalf("опция FUSE ntfs-3g %q предложена для ntfs3: %v", fuseOnly, all)
		}
	}
}
