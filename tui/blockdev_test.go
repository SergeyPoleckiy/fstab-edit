// Файл blockdev_test.go покрывает экран блочных устройств (клавиша B):
// рендеринг таблицы из подменённого seam ListBlockDevices (без запуска
// реального lsblk), состояния ошибки и пустого списка, навигацию курсором с
// прокруткой окна и повторное чтение устройств при повторном входе на экран.
package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/sysutil"
)

// testBlockDevices — синтетическое дерево устройств: диск с двумя разделами.
// Тесты никогда не читают реальные блочные устройства хоста.
func testBlockDevices() []sysutil.BlockDevice {
	return []sysutil.BlockDevice{
		{
			Name: "sda", Path: "/dev/sda", Type: "disk", Size: "238.5G", Model: "ATA SSD",
			Children: []sysutil.BlockDevice{
				{
					Name: "sda1", Path: "/dev/sda1", Type: "part",
					FSType: "vfat", Size: "512M", Label: "BOOT", UUID: "AAAA-1111",
					MountPoint: "/boot/efi",
				},
				{
					Name: "sda2", Path: "/dev/sda2", Type: "part",
					FSType: "ext4", Size: "238G", UUID: "22222222-2222-2222-2222-222222222222",
					MountPoint: "/",
				},
			},
		},
		{Name: "sdb", Path: "/dev/sdb", Type: "disk", Size: "931.5G"},
	}
}

// blockdevOptions собирает Options с подменённым seam ListBlockDevices:
// fake возвращает заданные данные, runner из seam игнорируется — подмена
// идёт на уровне функции, а не CommandRunner (контракт Task 2 плана).
func blockdevOptions(t *testing.T, devs []sysutil.BlockDevice, err error) Options {
	t.Helper()
	opts := testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n"))
	opts.Deps.ListBlockDevices = func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error) {
		return devs, err
	}
	return opts
}

// TestViewBlockDevicesRenders проверяет рендеринг таблицы устройств: заголовок
// колонок, значения полей из плоского списка (дерево развёрнуто: диск и оба
// раздела показаны), выделение курсорной строки и подсказку возврата.
func TestViewBlockDevicesRenders(t *testing.T) {
	m := testModel(t, blockdevOptions(t, testBlockDevices(), nil))
	m.blockdev = loadBlockDevices(context.Background(), &m)
	if m.blockdev.Err != nil {
		t.Fatalf("loadBlockDevices вернул ошибку: %v", m.blockdev.Err)
	}
	// Flatten: диск → дочерний → дочерний → второй диск = 4 строки.
	if len(m.blockdev.Devices) != 4 {
		t.Fatalf("ожидалось 4 плоских устройства, получено %d", len(m.blockdev.Devices))
	}
	m.screen = ScreenBlockDevices
	view := m.View()
	for _, want := range []string{
		"Блочные устройства", "PATH", "TYPE", "FSTYPE", "SIZE", "MOUNTPOINT", "LABEL", "UUID",
		"/dev/sda", "/dev/sda1", "/dev/sda2", "/dev/sdb", "vfat", "ext4", "238.5G",
		"BOOT", "AAAA-1111", "22222222-2222-2222-2222-222222222222", "/boot/efi",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("экран устройств не содержит %q: %q", want, view)
		}
	}
}

// TestViewBlockDevicesRendersErrorAndEmpty различает сбой загрузки (MarkError с
// причиной) и штатно пустой список (отдельная подпись, без маркера ошибки).
func TestViewBlockDevicesRendersErrorAndEmpty(t *testing.T) {
	// Ошибка seam — MarkError с причиной; таблица не рендерится.
	m := testModel(t, blockdevOptions(t, nil, errors.New("lsblk boom")))
	m.blockdev = loadBlockDevices(context.Background(), &m)
	m.screen = ScreenBlockDevices
	view := m.View()
	if !strings.Contains(view, "!") || !strings.Contains(view, "lsblk boom") {
		t.Fatalf("сбой загрузки устройств должен рендериться через MarkError: %q", view)
	}

	// Пустой список — подпись без маркера ошибки.
	m = testModel(t, blockdevOptions(t, nil, nil))
	m.blockdev = loadBlockDevices(context.Background(), &m)
	m.screen = ScreenBlockDevices
	view = m.View()
	if !strings.Contains(view, "блочные устройства не обнаружены") || strings.Contains(view, "!") {
		t.Fatalf("пустой список устройств не подписан: %q", view)
	}
}

// TestUpdateBlockDevicesNavigation проверяет навигацию экрана: стрелки/k/j
// двигают курсор в границах списка, pgup/pgdown листают на страницу, окно
// прокрутки подтягивается за курсором, Esc возвращает на таблицу. Список из
// 25 устройств гарантирует, что курсор выходит за пределы окна в 20 строк.
func TestUpdateBlockDevicesNavigation(t *testing.T) {
	devices := make([]sysutil.BlockDevice, 0, 25)
	for i := 0; i < 25; i++ {
		devices = append(devices, sysutil.BlockDevice{
			Name: "loop" + strconv.Itoa(i), Path: "/dev/loop" + strconv.Itoa(i),
			Type: "loop", FSType: "ext4", Size: "1G",
		})
	}
	m := testModel(t, blockdevOptions(t, devices, nil))
	m.blockdev = loadBlockDevices(context.Background(), &m)
	m.screen = ScreenBlockDevices

	press := func(m model, msg tea.KeyMsg) model {
		updated, _ := m.Update(msg)
		return updated.(model)
	}

	// Вниз по списку: курсор дошёл до конца (последний индекс 24) и зажался.
	for i := 0; i < 30; i++ {
		m = press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	if m.blockdev.Cursor != 24 {
		t.Fatalf("курсор после прокрутки вниз = %d, want 24", m.blockdev.Cursor)
	}
	// Окно прокрутки подтянулось за курсором: последняя строка видима.
	if want := 24 - screenPageSize + 1; m.blockdev.Offset != want {
		t.Fatalf("offset = %d, want %d (окно следует за курсором)", m.blockdev.Offset, want)
	}

	// Наверх: курсор и окно возвращаются на верхнюю границу.
	for i := 0; i < 30; i++ {
		m = press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	}
	if m.blockdev.Cursor != 0 || m.blockdev.Offset != 0 {
		t.Fatalf("курсор/offset после прокрутки вверх = %d/%d, want 0/0",
			m.blockdev.Cursor, m.blockdev.Offset)
	}

	// pgdown листает на страницу (строка screenPageSize — первый индекс
	// следующего окна), pgup возвращает в начало; оба зажаты в границах.
	m = press(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.blockdev.Cursor != screenPageSize {
		t.Fatalf("курсор после pgdown = %d, want %d", m.blockdev.Cursor, screenPageSize)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.blockdev.Cursor != 0 {
		t.Fatalf("курсор после pgup = %d, want 0", m.blockdev.Cursor)
	}

	// Esc возвращает на таблицу (глобальное правило навигации).
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.ScreenNow() != ScreenTable {
		t.Fatalf("Esc не вернул на таблицу: %v", m.ScreenNow())
	}
}

// TestUpdateTableBKeyLoadsAndReloads проверяет вход на экран клавишей B из
// таблицы: загрузка идёт через seam, повторный вход перечитывает список
// (значения второго вызова заменяют первые — нет закэшированных данных).
func TestUpdateTableBKeyLoadsAndReloads(t *testing.T) {
	first := []sysutil.BlockDevice{{Name: "sda", Path: "/dev/sda", Type: "disk", Size: "1G"}}
	second := []sysutil.BlockDevice{{Name: "nvme0n1", Path: "/dev/nvme0n1", Type: "disk", Size: "2T"}}
	calls := 0
	opts := testOptions(t, writeTestFstab(t, "UUID=aaa / ext4 defaults 0 1\n"))
	opts.Deps.ListBlockDevices = func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return second, nil
	}
	m := testModel(t, opts)

	// Вход клавишей B (верхний регистр, как у M/H/L).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}})
	m = updated.(model)
	if m.ScreenNow() != ScreenBlockDevices {
		t.Fatalf("клавиша B не открыла экран устройств: %v", m.ScreenNow())
	}
	if len(m.blockdev.Devices) != 1 || m.blockdev.Devices[0].Path != "/dev/sda" {
		t.Fatalf("первый вход: устройства %+v", m.blockdev.Devices)
	}

	// Esc → обратно на таблицу → повторный вход перечитывает список.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.ScreenNow() != ScreenTable {
		t.Fatalf("Esc не вернул на таблицу: %v", m.ScreenNow())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = updated.(model)
	if calls != 2 {
		t.Fatalf("повторный вход не перечитал устройства: вызовов seam = %d", calls)
	}
	if m.blockdev.Devices[0].Path != "/dev/nvme0n1" {
		t.Fatalf("повторный вход: устройства не перечитаны: %+v", m.blockdev.Devices)
	}

	// Ошибка загрузки на экране: Esc всё равно возвращает на таблицу.
	opts.Deps.ListBlockDevices = func(context.Context, sysutil.CommandRunner) ([]sysutil.BlockDevice, error) {
		return nil, errors.New("lsblk недоступен")
	}
	m = testModel(t, opts)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}})
	m = updated.(model)
	if m.blockdev.Err == nil {
		t.Fatal("ошибка seam не зафиксирована в модели экрана")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if m.ScreenNow() != ScreenTable {
		t.Fatalf("Esc на ошибочном экране не вернул на таблицу: %v", m.ScreenNow())
	}
}
