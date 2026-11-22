// Файл blockdev.go реализует экран блочных устройств (клавиша B, ТЗ §5.4):
// плоская таблица вывода lsblk (PATH, TYPE, FSTYPE, SIZE, MOUNTPOINT, LABEL,
// UUID) с курсорной навигацией и прокруткой. Данные получаются через seam
// Deps.ListBlockDevices с тем же sysutil.CommandRunner, что и прочие проверки;
// TUI только отображает результат и не дублирует доменную логику sysutil.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"fstab-edit/internal/sysutil"
)

// blockdevModel — данные экрана блочных устройств: плоский список (порядок
// lsblk, дерево развёрнуто через sysutil.Flatten), позиция курсора, сдвиг окна
// прокрутки и ошибка загрузки. Пустой список — штатное состояние (не сбой):
// Err остаётся nil, список помечается отдельной подписью.
type blockdevModel struct {
	Devices []sysutil.BlockDevice
	Cursor  int
	Offset  int
	Err     error
}

// loadBlockDevices загружает устройства через seam: дерево lsblk сворачивается
// в плоский список для табличного показа. Ошибка seam не паникует и не
// маскируется: она фиксируется в Err и показывается на экране. Runner берётся
// из Options — тот же, что используют findmnt-проверки (монтирования, verify).
func loadBlockDevices(ctx context.Context, m *model) blockdevModel {
	out := blockdevModel{}
	tree, err := m.deps().ListBlockDevices(ctx, m.opts.Runner)
	if err != nil {
		out.Err = err
		return out
	}
	out.Devices = sysutil.Flatten(tree)
	return out
}

// blockdevContent собирает строки таблицы устройств. Колонки усечены до
// фиксированной ширины (как в viewTable): длинные значения не ломают соседние
// колонки. UUID — последняя колонка, усечение мягче (36 символов), чтобы
// стандартный UUID показывался целиком.
func (m model) blockdevContent() []string {
	lines := make([]string, 0, len(m.blockdev.Devices))
	for _, dev := range m.blockdev.Devices {
		lines = append(lines, fmt.Sprintf("%-20.20s %-6.6s %-8.8s %-8.8s %-16.16s %-12.12s %-36.36s",
			dev.Path, dev.Type, dev.FSType, dev.Size, dev.MountPoint, dev.Label, dev.UUID))
	}
	return lines
}

// updateBlockDevices обрабатывает клавиши экрана устройств. Навигация зажата
// в границах списка; сдвиг окна прокрутки подтягивается за курсором, чтобы
// выбранная строка оставалась видимой (окно условно в screenPageSize строк,
// как у прочих read-only экранов). Esc возвращает на таблицу (глобальное
// правило). Ошибка загрузки не блокирует выход: экран остаётся только
// просмотровым, клавиши навигации игнорируются.
func (m model) updateBlockDevices(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.blockdev.Err != nil {
		if msg.String() == "esc" {
			m.screen = ScreenTable
		}
		return m, nil
	}
	total := len(m.blockdev.Devices)
	switch msg.String() {
	case "esc":
		m.screen = ScreenTable
		return m, nil
	case "up", "k":
		if m.blockdev.Cursor > 0 {
			m.blockdev.Cursor--
		}
	case "down", "j":
		if m.blockdev.Cursor < total-1 {
			m.blockdev.Cursor++
		}
	case "pgup":
		m.blockdev.Cursor = max(0, m.blockdev.Cursor-screenPageSize)
	case "pgdown":
		m.blockdev.Cursor = min(total-1, m.blockdev.Cursor+screenPageSize)
	default:
		return m, nil
	}
	// Окно прокрутки следует за курсором: ушёл выше окна — окно подтянулось
	// вверх; ушёл ниже — вниз. Это сдвиг окна, а не пересборка содержимого.
	if m.blockdev.Cursor < m.blockdev.Offset {
		m.blockdev.Offset = m.blockdev.Cursor
	}
	if m.blockdev.Cursor >= m.blockdev.Offset+screenPageSize {
		m.blockdev.Offset = m.blockdev.Cursor - screenPageSize + 1
	}
	return m, nil
}

// viewBlockDevices рендерит экран блочных устройств: заголовок, далее по
// состоянию — ошибка загрузки (MarkError с причиной), подпись пустого списка
// или таблица с заголовком колонок и окном прокрутки; курсорная строка
// подсвечена через theme.Selected.
func (m model) viewBlockDevices() string {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render(Tr(m.opts.Lang, "blockdev_title")) + "\n\n")
	switch {
	case m.blockdev.Err != nil:
		b.WriteString(m.theme.MarkError(Trf(m.opts.Lang, "blockdev_error", m.blockdev.Err.Error())) + "\n")
	case len(m.blockdev.Devices) == 0:
		b.WriteString(m.theme.Muted.Render(Tr(m.opts.Lang, "blockdev_empty")) + "\n")
	default:
		b.WriteString(m.theme.Header.Render(Tr(m.opts.Lang, "blockdev_header")) + "\n")
		content := m.blockdevContent()
		for i := m.blockdev.Offset; i < len(content); i++ {
			row := content[i]
			if i == m.blockdev.Cursor {
				row = m.theme.Selected.Render(row)
			}
			b.WriteString(row + "\n")
		}
	}
	b.WriteString("\n" + m.theme.Muted.Render(Tr(m.opts.Lang, "keys_back")) + "\n")
	return b.String()
}
