// Файл entry.go проверяет поля одной активной записи fstab: общие ограничения
// и escapes, source, mount point, тип файловой системы и числовые dump/pass.
// Здесь также применяется специальное правило mount none для swap.
package validator

import (
	"slices"
	"strings"

	"fstab-edit/pkg/models"
)

// KnownFSTypes — полный перечень поддерживаемых типов файловых систем:
// все типы ТЗ §6.2 плюс типы, которые валидатор знал до введения общего
// списка (ntfs3, devtmpfs, cgroup, cgroup2, squashfs, fuse). Список
// отсортирован и является единым источником для валидации (validateFSType)
// и автодополнения в TUI, чтобы форма не дублировала перечень.
var KnownFSTypes = []string{
	"auto",
	"bind",
	"btrfs",
	"cgroup",
	"cgroup2",
	"cifs",
	"devpts",
	"devtmpfs",
	"exfat",
	"ext2",
	"ext3",
	"ext4",
	"f2fs",
	"fuse",
	"iso9660",
	"msdos",
	"nfs",
	"nfs4",
	"ntfs",
	"ntfs-3g",
	"ntfs3",
	"overlay",
	"proc",
	"reiserfs",
	"smbfs",
	"squashfs",
	"sshfs",
	"swap",
	"sysfs",
	"tmpfs",
	"udf",
	"vfat",
	"xfs",
}

// validateEntry применяет правила в стабильном порядке полей fstab. Специальная
// проверка выполняется после общей, но пропускается для пустого значения, чтобы
// одна причина не порождала каскад вторичных диагностик.
func validateEntry(collector *collector, entry *models.Entry) {
	if entry == nil {
		return
	}

	validateField(collector, entry.Line, "source", entry.Source, true)
	validateSource(collector, entry)

	validateField(collector, entry.Line, "mount_point", entry.MountPoint, true)
	validateMount(collector, entry)

	validateField(collector, entry.Line, "fs_type", entry.FSType, true)
	validateFSType(collector, entry)

	validateField(collector, entry.Line, "options", entry.Options, true)
	validateOptions(collector, entry)
	validateSpecial(collector, entry)

	if entry.Dump != nil {
		validateField(collector, entry.Line, "dump", *entry.Dump, false)
		validateNumber(collector, entry.Line, "dump", *entry.Dump)
	}
	if entry.Pass != nil {
		validateField(collector, entry.Line, "pass", *entry.Pass, false)
		validateNumber(collector, entry.Line, "pass", *entry.Pass)
	}
}

// validateField выполняет общие проверки исходного и decoded представлений.
// Для обязательного пустого поля достаточно field.empty: controls и escapes
// в таком Raw отсутствуют, а последующие semantic rules имеют empty guard.
func validateField(collector *collector, line int, name string, field models.Field, required bool) {
	if required && field.Value == "" {
		collector.add(models.Diagnostic{
			Code:     "field.empty",
			Severity: models.SeverityError,
			Line:     line,
			Field:    name,
			Message:  "Обязательное поле не может быть пустым",
		})
		return
	}
	validateControls(collector, line, name, field)
	validateEscapes(collector, line, name, field.Raw)
}

// validateControls отклоняет литеральные control bytes и decoded NUL. Если Raw
// уже содержит control, decoded проверка пропускается, чтобы один байт не дал
// две диагностики; escaped tab является единственным разрешённым control.
func validateControls(collector *collector, line int, name string, field models.Field) {
	rawHasControl := false
	for index := 0; index < len(field.Raw); index++ {
		if !isControl(field.Raw[index]) {
			continue
		}
		rawHasControl = true
		collector.add(models.Diagnostic{
			Code:     "field.control",
			Severity: models.SeverityError,
			Line:     line,
			Field:    name,
			Message:  "Поле содержит недопустимый управляющий байт",
		})
	}
	if rawHasControl {
		return
	}

	for index := 0; index < len(field.Value); index++ {
		value := field.Value[index]
		if !isControl(value) || value == '\t' {
			continue
		}
		severity := models.SeverityWarning
		code := "field.control_escaped"
		message := "Поле содержит экранированный управляющий байт"
		if value == 0 {
			severity = models.SeverityError
			code = "field.control"
			message = "Поле содержит экранированный NUL"
		}
		collector.add(models.Diagnostic{
			Code:     code,
			Severity: severity,
			Line:     line,
			Field:    name,
			Message:  message,
		})
	}
}

// isControl выделяет ровно C0 bytes и DEL, не классифицируя UTF-8 continuation
// bytes как управляющие символы.
func isControl(value byte) bool {
	return value < 0x20 || value == 0x7f
}

// validateEscapes сканирует только Raw слева направо. Три octal digits после
// backslash образуют допустимый escape; иначе выдаётся warning и продолжается
// посимвольный обход, включая оставшиеся байты неполной последовательности.
func validateEscapes(collector *collector, line int, name, raw string) {
	for index := 0; index < len(raw); {
		if raw[index] != '\\' {
			index++
			continue
		}
		if index+3 < len(raw) && isOctal(raw[index+1]) && isOctal(raw[index+2]) && isOctal(raw[index+3]) {
			index += 4
			continue
		}
		collector.add(models.Diagnostic{
			Code:     "escape.unknown",
			Severity: models.SeverityWarning,
			Line:     line,
			Field:    name,
			Message:  "Поле содержит неизвестную или неполную escape-последовательность",
		})
		index++
	}
}

// isOctal проверяет один byte без преобразования строки и переполнения.
func isOctal(value byte) bool {
	return value >= '0' && value <= '7'
}

// validateMount проверяет абсолютный путь и специальную семантику swap. Пустое
// поле уже диагностировано как field.empty, поэтому здесь оно пропускается.
func validateMount(collector *collector, entry *models.Entry) {
	mount := entry.MountPoint.Value
	if mount == "" {
		return
	}
	if entry.FSType.Value == "swap" {
		if mount != "none" {
			collector.add(models.Diagnostic{
				Code:     "swap.mount",
				Severity: models.SeverityError,
				Line:     entry.Line,
				Field:    "mount_point",
				Value:    mount,
				Message:  "Для swap точка монтирования должна быть none",
			})
		}
		return
	}
	if mount == "none" {
		collector.add(models.Diagnostic{
			Code:     "mount.none",
			Severity: models.SeverityError,
			Line:     entry.Line,
			Field:    "mount_point",
			Value:    mount,
			Message:  "Специальная точка none допустима только для swap",
		})
		return
	}
	if !strings.HasPrefix(mount, "/") {
		collector.add(models.Diagnostic{
			Code:     "mount.relative",
			Severity: models.SeverityError,
			Line:     entry.Line,
			Field:    "mount_point",
			Value:    mount,
			Message:  "Точка монтирования должна быть абсолютной",
		})
	}
}

// validateNumber проверяет dump/pass как строку decimal digits без Atoi: любая
// длина остаётся безопасной. После lexical check нетипичное значение получает
// warning, а malformed value завершает проверку одним number.invalid.
func validateNumber(collector *collector, line int, name string, field models.Field) {
	value := field.Value
	if value == "" {
		collector.add(models.Diagnostic{
			Code:     "number.invalid",
			Severity: models.SeverityError,
			Line:     line,
			Field:    name,
			Message:  "Числовое поле не может быть пустым",
		})
		return
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			collector.add(models.Diagnostic{
				Code:     "number.invalid",
				Severity: models.SeverityError,
				Line:     line,
				Field:    name,
				Value:    value,
				Message:  "Числовое поле должно содержать только десятичные цифры",
			})
			return
		}
	}

	usual := value == "0" || value == "1" || name == "pass" && value == "2"
	if !usual {
		collector.add(models.Diagnostic{
			Code:     "number.unusual",
			Severity: models.SeverityWarning,
			Line:     line,
			Field:    name,
			Value:    value,
			Message:  "Значение допустимо синтаксически, но нетипично для этого поля",
		})
	}
}

// validateSource сначала применяет network/pseudo правила и только затем generic
// label/absolute path. Благодаря этому /dev/... не обходит обязательный синтаксис
// network FS. Диагностика намеренно не копирует source, где могут быть secrets.
func validateSource(collector *collector, entry *models.Entry) {
	source := entry.Source.Value
	if source == "" {
		return
	}

	fsType := entry.FSType.Value
	switch fsType {
	case "cifs", "smbfs":
		if !validCIFSSource(source) {
			addInvalidSource(collector, entry.Line)
		}
		return
	case "nfs", "nfs4":
		if !validHostPathSource(source, false) {
			addInvalidSource(collector, entry.Line)
		}
		return
	case "sshfs":
		if !validHostPathSource(source, true) {
			addInvalidSource(collector, entry.Line)
		}
		return
	}

	if isPseudoFS(fsType) || hasLabelPrefix(source) || strings.HasPrefix(source, "/") {
		return
	}
	addInvalidSource(collector, entry.Line)
}

// validCIFSSource требует //host/share и отдельно проверяет nonempty host/share.
// Дополнительные '/' остаются частью share и не требуют нормализации пути.
func validCIFSSource(source string) bool {
	if !strings.HasPrefix(source, "//") {
		return false
	}
	host, share, found := strings.Cut(strings.TrimPrefix(source, "//"), "/")
	return found && host != "" && share != ""
}

// validHostPathSource проверяет host:/path для NFS и SSHFS. Для SSHFS host part
// может содержать user@, но при наличии '@' user и host обязаны быть непустыми.
func validHostPathSource(source string, allowUser bool) bool {
	host, path, found := strings.Cut(source, ":")
	if !found || host == "" || !strings.HasPrefix(path, "/") {
		return false
	}
	if allowUser {
		if user, remoteHost, hasUser := strings.Cut(host, "@"); hasUser {
			return user != "" && remoteHost != ""
		}
	}
	return true
}

// hasLabelPrefix принимает только четыре стандартных prefix с nonempty suffix.
func hasLabelPrefix(source string) bool {
	for _, prefix := range []string{"UUID=", "LABEL=", "PARTUUID=", "PARTLABEL="} {
		if strings.HasPrefix(source, prefix) && len(source) > len(prefix) {
			return true
		}
	}
	return false
}

// isPseudoFS перечисляет типы, для которых любой nonempty source осмыслен,
// и распространяет это правило на произвольные fuse.* подтипы.
func isPseudoFS(fsType string) bool {
	if strings.HasPrefix(fsType, "fuse.") {
		return true
	}
	switch fsType {
	case "proc", "sysfs", "tmpfs", "devtmpfs", "devpts", "cgroup", "cgroup2", "overlay":
		return true
	default:
		return false
	}
}

// addInvalidSource создаёт единую redacted диагностику без Value и Params.
func addInvalidSource(collector *collector, line int) {
	collector.add(models.Diagnostic{
		Code:     "source.invalid",
		Severity: models.SeverityError,
		Line:     line,
		Field:    "source",
		Message:  "Источник не соответствует типу файловой системы",
	})
}

// validateFSType сообщает deprecated smbfs и info о неизвестном nonempty типе.
// Проверка идёт по экспортируемому KnownFSTypes; smbfs обрабатывается раньше
// списка (deprecated-warning вместо «известен»), а любой fuse.* считается
// известным без внешнего lookup. Неизвестность типа не возвращает управление
// в validateEntry и не отключает остальные проверки.
func validateFSType(collector *collector, entry *models.Entry) {
	fsType := entry.FSType.Value
	if fsType == "" {
		return
	}
	if fsType == "smbfs" {
		collector.add(models.Diagnostic{
			Code:     "fs.deprecated",
			Severity: models.SeverityWarning,
			Line:     entry.Line,
			Field:    "fs_type",
			Value:    fsType,
			Message:  "Тип smbfs устарел; рекомендуется cifs",
		})
		return
	}
	if strings.HasPrefix(fsType, "fuse.") {
		return
	}
	if slices.Contains(KnownFSTypes, fsType) {
		return
	}
	collector.add(models.Diagnostic{
		Code:     "fs.unknown",
		Severity: models.SeverityInfo,
		Line:     entry.Line,
		Field:    "fs_type",
		Value:    fsType,
		Message:  "Тип файловой системы отсутствует во встроенном списке",
	})
}
