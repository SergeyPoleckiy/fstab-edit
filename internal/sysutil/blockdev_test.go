package sysutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeBlockdevRunner фиксирует имя утилиты и argv и возвращает заранее заданный
// результат, не запуская lsblk, blkid или какой-либо другой внешний процесс.
type fakeBlockdevRunner struct {
	result RunResult
	calls  int
	name   string
	args   []string
}

func (r *fakeBlockdevRunner) Run(_ context.Context, name string, args ...string) RunResult {
	r.calls++
	r.name = name
	r.args = append([]string(nil), args...)
	return r.result
}

// blockdevFixture читает только синтетический наблюдённый вывод из testdata.
func blockdevFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "blockdev", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// wantLsblkArgv — единственный поддерживаемый набор аргументов lsblk.
var wantLsblkArgv = []string{"--json", "--output", "NAME,PATH,TYPE,FSTYPE,SIZE,MOUNTPOINT,LABEL,UUID,MODEL"}

// TestListBlockDevicesTree проверяет точный argv, разбор дерева (disk с
// partitions, LVM-children) и нормализацию null-полей lsblk в пустые строки.
func TestListBlockDevicesTree(t *testing.T) {
	runner := &fakeBlockdevRunner{result: RunResult{
		Path: "/usr/bin/lsblk", ExitCode: 0,
		Stdout: blockdevFixture(t, "lsblk-tree.json"),
	}}

	devices, err := ListBlockDevices(context.Background(), runner)
	if err != nil {
		t.Fatalf("ListBlockDevices: %v", err)
	}
	if runner.name != "lsblk" {
		t.Fatalf("утилита: got %q, want %q", runner.name, "lsblk")
	}
	if !reflect.DeepEqual(runner.args, wantLsblkArgv) {
		t.Fatalf("argv: got %v, want %v", runner.args, wantLsblkArgv)
	}
	if len(devices) != 3 {
		t.Fatalf("корневых устройств: got %d, want 3", len(devices))
	}

	// Диск sda: null fstype/mountpoint/label/uuid превращаются в "".
	sda := devices[0]
	if sda.Name != "sda" || sda.Path != "/dev/sda" || sda.Type != "disk" {
		t.Fatalf("диск sda разобран неверно: %+v", sda)
	}
	if sda.FSType != "" || sda.MountPoint != "" || sda.Label != "" || sda.UUID != "" {
		t.Fatalf("null-поля должны стать пустыми строками: %+v", sda)
	}
	if sda.Model != "Samsung_SSD_870_EVO" || sda.Size != "931.5G" {
		t.Fatalf("model/size разобраны неверно: %+v", sda)
	}
	if len(sda.Children) != 2 {
		t.Fatalf("children диска: got %d, want 2", len(sda.Children))
	}

	// Раздел sda2 содержит LVM-children.
	sda2 := sda.Children[1]
	if sda2.FSType != "LVM2_member" || sda2.UUID != "lvm-pv-uuid-0000" {
		t.Fatalf("раздел sda2 разобран неверно: %+v", sda2)
	}
	if len(sda2.Children) != 2 || sda2.Children[0].Path != "/dev/mapper/cachyos-root" {
		t.Fatalf("LVM-children разобраны неверно: %+v", sda2.Children)
	}
	root := sda2.Children[0]
	if root.FSType != "btrfs" || root.MountPoint != "/" || root.UUID != "root-uuid-2222" {
		t.Fatalf("LV root разобран неверно: %+v", root)
	}

	// rom и loop устройства верхнего уровня: label у sr0, null label у loop0.
	if devices[1].Type != "rom" || devices[1].Label != "INSTALL_DISC" {
		t.Fatalf("sr0 разобран неверно: %+v", devices[1])
	}
	if devices[2].Type != "loop" || devices[2].Label != "" || devices[2].MountPoint != "/snap/core" {
		t.Fatalf("loop0 разобран неверно: %+v", devices[2])
	}
}

// TestListBlockDevicesErrors покрывает malformed JSON, ненулевой exit code,
// обрезанный stdout, nil runner и сбой запуска как ошибки.
func TestListBlockDevicesErrors(t *testing.T) {
	cases := []struct {
		name   string
		result RunResult
	}{
		// Невалидный JSON обязан дать ошибку, а не пустой список.
		{name: "malformed json", result: RunResult{ExitCode: 0, Stdout: `{"blockdevices": [`}},
		// JSON не той формы (массив вместо обёртки) — тоже ошибка разбора.
		{name: "wrong json shape", result: RunResult{ExitCode: 0, Stdout: `[{"name":"sda"}]`}},
		// Ненулевой exit code с stderr даёт ошибку с stderr в сообщении.
		{name: "nonzero exit", result: RunResult{ExitCode: 32, Stdout: "", Stderr: "lsblk: invalid argument"}},
		// Обрезанный stdout мог бы разобраться как частичный список — запрещаем.
		{name: "truncated stdout", result: RunResult{ExitCode: 0, StdoutTruncated: true, Stdout: `{"blockdevices":[]}`}},
		// Сбой самого запуска (например, утилита не найдена) — ошибка.
		{name: "run failure", result: RunResult{ExitCode: -1, Err: errors.New("исполняемый файл не найден")}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeBlockdevRunner{result: test.result}
			devices, err := ListBlockDevices(context.Background(), runner)
			if err == nil {
				t.Fatalf("ожидалась ошибка, получены устройства: %+v", devices)
			}
			if devices != nil {
				t.Fatalf("при ошибке устройства должны быть nil: %+v", devices)
			}
		})
	}

	t.Run("nil runner", func(t *testing.T) {
		devices, err := ListBlockDevices(context.Background(), nil)
		if !errors.Is(err, ErrNilRunner) {
			t.Fatalf("ожидался ErrNilRunner, получено: %v", err)
		}
		if devices != nil {
			t.Fatalf("при nil runner устройства должны быть nil: %+v", devices)
		}
	})
}

// TestListBlockDevicesEmpty проверяет пустой успешный список без ошибки.
func TestListBlockDevicesEmpty(t *testing.T) {
	runner := &fakeBlockdevRunner{result: RunResult{ExitCode: 0, Stdout: `{"blockdevices":[]}`}}
	devices, err := ListBlockDevices(context.Background(), runner)
	if err != nil {
		t.Fatalf("ListBlockDevices: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("ожидался пустой список: %+v", devices)
	}
}

// TestProbeDeviceExport проверяет точный argv и разбор KEY=VALUE-строк blkid
// с UUID/LABEL/TYPE/PARTUUID из fixture.
func TestProbeDeviceExport(t *testing.T) {
	runner := &fakeBlockdevRunner{result: RunResult{
		Path: "/usr/bin/blkid", ExitCode: 0,
		Stdout: blockdevFixture(t, "blkid-export-sda1.stdout"),
	}}

	properties, err := ProbeDevice(context.Background(), runner, "/dev/sda1")
	if err != nil {
		t.Fatalf("ProbeDevice: %v", err)
	}
	if runner.name != "blkid" {
		t.Fatalf("утилита: got %q, want %q", runner.name, "blkid")
	}
	wantArgs := []string{"-o", "export", "/dev/sda1"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("argv: got %v, want %v", runner.args, wantArgs)
	}

	want := map[string]string{
		"DEVNAME":  "/dev/sda1",
		"UUID":     "3f5c9e2a-7b1c-4a2e-9d44-1a2b3c4d5e6f",
		"LABEL":    "SYSTEM",
		"TYPE":     "ext4",
		"PARTUUID": "11112222-3333-4444-5555-666677778888",
	}
	if !reflect.DeepEqual(properties, want) {
		t.Fatalf("свойства: got %v, want %v", properties, want)
	}
}

// TestProbeDeviceExitTwo покрывает штатный случай blkid: exit code 2 означает
// «атрибуты не найдены» и даёт пустую карту без ошибки.
func TestProbeDeviceExitTwo(t *testing.T) {
	runner := &fakeBlockdevRunner{result: RunResult{
		ExitCode: 2,
		Err:      errors.New("exit status 2"),
		Stderr:   "",
	}}
	properties, err := ProbeDevice(context.Background(), runner, "/dev/sda9")
	if err != nil {
		t.Fatalf("exit code 2 не должен быть ошибкой: %v", err)
	}
	if properties == nil || len(properties) != 0 {
		t.Fatalf("ожидалась пустая non-nil карта: %#v", properties)
	}
}

// TestProbeDeviceErrors покрывает пустой путь, nil runner, прочие ненулевые
// коды и сбой запуска как ошибки.
func TestProbeDeviceErrors(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		runner := &fakeBlockdevRunner{result: RunResult{ExitCode: 0}}
		_, err := ProbeDevice(context.Background(), runner, "")
		if !errors.Is(err, ErrNilDevicePath) {
			t.Fatalf("ожидался ErrNilDevicePath, получено: %v", err)
		}
	})

	t.Run("nil runner", func(t *testing.T) {
		_, err := ProbeDevice(context.Background(), nil, "/dev/sda1")
		if !errors.Is(err, ErrNilRunner) {
			t.Fatalf("ожидался ErrNilRunner, получено: %v", err)
		}
	})

	t.Run("nonzero exit", func(t *testing.T) {
		runner := &fakeBlockdevRunner{result: RunResult{
			ExitCode: 4, Err: errors.New("exit status 4"), Stderr: "blkid: probe failed",
		}}
		_, err := ProbeDevice(context.Background(), runner, "/dev/sda1")
		if err == nil || !strings.Contains(err.Error(), "probe failed") {
			t.Fatalf("ошибка должна включать stderr: %v", err)
		}
	})

	t.Run("run failure", func(t *testing.T) {
		runner := &fakeBlockdevRunner{result: RunResult{ExitCode: -1, Err: errors.New("исполняемый файл не найден")}}
		_, err := ProbeDevice(context.Background(), runner, "/dev/sda1")
		if err == nil {
			t.Fatal("ожидалась ошибка запуска")
		}
	})
}

// TestFlattenOrder проверяет порядок обхода: родитель, затем children рекурсивно.
func TestFlattenOrder(t *testing.T) {
	tree := []BlockDevice{
		{Name: "sda", Path: "/dev/sda", Children: []BlockDevice{
			{Name: "sda1", Path: "/dev/sda1"},
			{Name: "sda2", Path: "/dev/sda2", Children: []BlockDevice{
				{Name: "vg-root", Path: "/dev/mapper/vg-root"},
				{Name: "vg-home", Path: "/dev/mapper/vg-home"},
			}},
		}},
		{Name: "sr0", Path: "/dev/sr0"},
	}

	flattened := Flatten(tree)
	wantPaths := []string{
		"/dev/sda", "/dev/sda1", "/dev/sda2",
		"/dev/mapper/vg-root", "/dev/mapper/vg-home", "/dev/sr0",
	}
	gotPaths := make([]string, 0, len(flattened))
	for _, device := range flattened {
		gotPaths = append(gotPaths, device.Path)
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("Flatten: got %v, want %v", gotPaths, wantPaths)
	}
}

// TestFlattenEmptyAndNil проверяет граничные случаи Flatten.
func TestFlattenEmptyAndNil(t *testing.T) {
	if got := Flatten(nil); len(got) != 0 {
		t.Fatalf("Flatten(nil) должен быть пустым: %+v", got)
	}
	if got := Flatten([]BlockDevice{}); len(got) != 0 {
		t.Fatalf("Flatten([]) должен быть пустым: %+v", got)
	}
	// Устройство без children даёт ровно одну запись.
	got := Flatten([]BlockDevice{{Name: "sda", Path: "/dev/sda"}})
	if len(got) != 1 || got[0].Path != "/dev/sda" {
		t.Fatalf("Flatten одиночного устройства: %+v", got)
	}
}
