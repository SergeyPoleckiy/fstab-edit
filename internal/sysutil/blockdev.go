// Файл blockdev.go получает перечень блочных устройств и свойства отдельного
// устройства через безопасный запуск lsblk и blkid. Парсеры зависят только от
// интерфейса CommandRunner, поэтому тесты подменяют процесс fixture-выводом
// и никогда не выполняют реальные lsblk/blkid. Сырой вывод не сохраняется
// в структурах: ошибки содержат только stderr без stdout.
package sysutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// lsblkOutputColumns задаёт единственный поддерживаемый набор колонок lsblk.
// Порядок колонок влияет на JSON-ключи, поэтому аргумент передаётся одной строкой.
// PARTUUID/PARTLABEL добавлены на этапе 5 (ТЗ v2 этап 3): это идентификаторы
// раздела GPT, которые lsblk показывает независимо от ФС, и они нужны форме
// для автоподстановки и для пометки повторяющихся идентификаторов.
const lsblkOutputColumns = "NAME,PATH,TYPE,FSTYPE,SIZE,MOUNTPOINT,LABEL,UUID,PARTUUID,PARTLABEL,MODEL"

var (
	// ErrNilRunner означает вызов парсера без CommandRunner: читать устройства
	// неоткуда, и это состояние отличается от ошибки самой утилиты.
	ErrNilRunner = errors.New("командный runner не задан")
	// ErrNilDevicePath означает вызов ProbeDevice с пустым путём устройства.
	ErrNilDevicePath = errors.New("путь устройства не задан")
)

// BlockDevice описывает одну строку lsblk: идентификацию, размер, ФС и метки.
// Пустые поля (неприменимо или не заполнено) представляются пустой строкой;
// null из JSON lsblk нормализуется в "". UUID/LABEL берутся из lsblk, без
// отдельного вызова blkid; blkid применяется через ProbeDevice, когда lsblk
// не показал нужных свойств устройства. PartUUID/PartLabel — идентификаторы
// раздела из таблицы разделов (GPT/dos), не зависят от ФС и доступны, даже
// когда UUID тома ещё не присвоен (ТЗ v2 этап 3).
type BlockDevice struct {
	Name       string        // sda1
	Path       string        // /dev/sda1
	Type       string        // disk, part, lvm, crypt, rom, loop
	FSType     string        // vfat, ext4, ...; "" если ФС не определена
	Size       string        // как выдал lsblk (17.4G) — без пересчёта
	MountPoint string        // текущая точка монтирования или ""
	Label      string        // метка тома или ""
	UUID       string        // UUID тома или ""
	PartUUID   string        // UUID раздела (PARTUUID) или ""
	PartLabel  string        // метка раздела (PARTLABEL) или ""
	Model      string        // модель диска или ""
	Children   []BlockDevice // вложенные: partitions of disk, lv of vg
}

// lsblkJSON описывает корневую обёртку вывода `lsblk --json`.
type lsblkJSON struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

// lsblkDevice — внутреннее представление одного узла lsblk: все строковые
// поля допускают null, поэтому разбираются через указатели и приводятся к "".
// Ключи partuuid/partlabel соответствуют колонкам PARTUUID/PARTLABEL.
type lsblkDevice struct {
	Name       string        `json:"name"`
	Path       *string       `json:"path"`
	Type       string        `json:"type"`
	FSType     *string       `json:"fstype"`
	Size       *string       `json:"size"`
	MountPoint *string       `json:"mountpoint"`
	Label      *string       `json:"label"`
	UUID       *string       `json:"uuid"`
	PartUUID   *string       `json:"partuuid"`
	PartLabel  *string       `json:"partlabel"`
	Model      *string       `json:"model"`
	Children   []lsblkDevice `json:"children"`
}

// ListBlockDevices возвращает дерево блочных устройств через
// `lsblk --json --output NAME,PATH,TYPE,FSTYPE,SIZE,MOUNTPOINT,LABEL,UUID,PARTUUID,PARTLABEL,MODEL`.
// Ненулевой exit code — ошибка с stderr; malformed JSON и обрезанный stdout —
// ошибка, чтобы частичный список не выдавался за полный.
func ListBlockDevices(ctx context.Context, runner CommandRunner) ([]BlockDevice, error) {
	if runner == nil {
		return nil, ErrNilRunner
	}
	result := runner.Run(ctx, "lsblk", "--json", "--output", lsblkOutputColumns)
	if result.Err != nil {
		return nil, fmt.Errorf("запуск lsblk: %w: %s", result.Err, summarizeStderr(result.Stderr))
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("lsblk завершился с кодом %d: %s",
			result.ExitCode, summarizeStderr(result.Stderr))
	}
	if result.StdoutTruncated {
		return nil, errors.New("вывод lsblk обрезан ограничением размера")
	}

	var wrapper lsblkJSON
	if err := json.Unmarshal([]byte(result.Stdout), &wrapper); err != nil {
		return nil, fmt.Errorf("разбор JSON lsblk: %w", err)
	}
	devices := make([]BlockDevice, 0, len(wrapper.BlockDevices))
	for _, raw := range wrapper.BlockDevices {
		devices = append(devices, raw.toBlockDevice())
	}
	return devices, nil
}

// toBlockDevice приводит внутреннее представление lsblk к публичному типу,
// нормализуя null в пустые строки и рекурсивно обходя children.
func (raw lsblkDevice) toBlockDevice() BlockDevice {
	device := BlockDevice{
		Name:       raw.Name,
		Path:       stringValue(raw.Path),
		Type:       raw.Type,
		FSType:     stringValue(raw.FSType),
		Size:       stringValue(raw.Size),
		MountPoint: stringValue(raw.MountPoint),
		Label:      stringValue(raw.Label),
		UUID:       stringValue(raw.UUID),
		PartUUID:   stringValue(raw.PartUUID),
		PartLabel:  stringValue(raw.PartLabel),
		Model:      stringValue(raw.Model),
	}
	if len(raw.Children) > 0 {
		device.Children = make([]BlockDevice, 0, len(raw.Children))
		for _, child := range raw.Children {
			device.Children = append(device.Children, child.toBlockDevice())
		}
	}
	return device
}

// ProbeDevice возвращает карту свойств устройства через `blkid -o export <path>`
// (DEVNAME, UUID, LABEL, TYPE, PARTUUID, ...). Устройство без свойств — пустая
// карта без ошибки: blkid завершается кодом 2, когда атрибуты не найдены.
// Прочие ненулевые коды и сбои запуска — ошибка с stderr.
func ProbeDevice(ctx context.Context, runner CommandRunner, path string) (map[string]string, error) {
	if path == "" {
		return nil, ErrNilDevicePath
	}
	if runner == nil {
		return nil, ErrNilRunner
	}
	result := runner.Run(ctx, "blkid", "-o", "export", path)
	if result.ExitCode == 2 {
		// Код 2 по документации blkid: устройство без атрибутов. Это штатный
		// результат (например, раздел без ФС), а не сбой утилиты.
		return map[string]string{}, nil
	}
	if result.Err != nil {
		return nil, fmt.Errorf("запуск blkid: %w: %s", result.Err, summarizeStderr(result.Stderr))
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("blkid завершился с кодом %d: %s",
			result.ExitCode, summarizeStderr(result.Stderr))
	}

	properties := make(map[string]string)
	for _, line := range strings.Split(result.Stdout, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" {
			// Пустые строки и служебные строки без '=' не являются свойствами.
			continue
		}
		properties[key] = value
	}
	return properties, nil
}

// Flatten возвращает плоский список устройств в порядке обхода дерева:
// родитель, затем его children рекурсивно. Порядок совпадает с выводом lsblk,
// поэтому экран и подсказки формы показывают устройства предсказуемо.
func Flatten(devices []BlockDevice) []BlockDevice {
	flattened := make([]BlockDevice, 0, len(devices))
	var appendDevice func(device BlockDevice)
	appendDevice = func(device BlockDevice) {
		flattened = append(flattened, device)
		for _, child := range device.Children {
			appendDevice(child)
		}
	}
	for _, device := range devices {
		appendDevice(device)
	}
	return flattened
}

// stringValue приводит nullable строку lsblk к обычной строке.
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// summarizeStderr сокращает stderr до одной строки без переводов строк,
// чтобы сообщение об ошибке оставалось читаемым и не копировало stdout.
func summarizeStderr(stderr string) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return "stderr пуст"
	}
	if strings.Contains(trimmed, "\n") {
		trimmed, _, _ = strings.Cut(trimmed, "\n")
		trimmed = strings.TrimSpace(trimmed) + " ..."
	}
	return trimmed
}
