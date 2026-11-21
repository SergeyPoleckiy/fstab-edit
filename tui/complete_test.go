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
// (ТЗ п.10): UUID выигрывает у LABEL и пути.
func TestDeviceHintsPriorityUUID(t *testing.T) {
	devices := []sysutil.BlockDevice{
		{Path: "/dev/sdb1", Type: "part", FSType: "vfat", Size: "16G",
			Label: "USBKEY", UUID: "BBBB-2222", MountPoint: "/mnt/usb"},
	}
	hints := deviceHints(devices)
	if len(hints) != 1 {
		t.Fatalf("подсказок: %d, ожидалась 1", len(hints))
	}
	if hints[0].Value != "UUID=BBBB-2222" {
		t.Fatalf("Value: %q, ожидалось %q", hints[0].Value, "UUID=BBBB-2222")
	}
}

// TestDeviceHintsPriorityLabelAndPath проверяет fallback-цепочку: без UUID
// подставляется LABEL=<label>, без UUID и LABEL — путь устройства.
func TestDeviceHintsPriorityLabelAndPath(t *testing.T) {
	devices := []sysutil.BlockDevice{
		{Path: "/dev/sdc1", Type: "part", FSType: "ext4", Size: "256G", Label: "DATA"},
		{Path: "/dev/sda", Type: "disk", Size: "476.9G"},
	}
	hints := deviceHints(devices)
	if len(hints) != 2 {
		t.Fatalf("подсказок: %d, ожидалось 2", len(hints))
	}
	if hints[0].Value != "LABEL=DATA" {
		t.Fatalf("Value без UUID: %q, ожидалось %q", hints[0].Value, "LABEL=DATA")
	}
	if hints[1].Value != "/dev/sda" {
		t.Fatalf("Value без UUID/LABEL: %q, ожидался путь /dev/sda", hints[1].Value)
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
	hints := deviceHints(devices)
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
