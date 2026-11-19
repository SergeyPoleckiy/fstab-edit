// Package validator_test проверяет внутреннюю валидацию через публичный API.
// Тесты используют только документы в памяти и не обращаются к /etc/fstab.
package validator_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"fstab-edit/internal/parser"
	"fstab-edit/internal/validator"
	"fstab-edit/pkg/models"
)

// codes сохраняет severity вместе со стабильным кодом, чтобы тест различал
// блокирующую ошибку, предупреждение и информационное сообщение.
func codes(report models.Report) []string {
	result := make([]string, len(report.Diagnostics))
	for index, diagnostic := range report.Diagnostics {
		result[index] = string(diagnostic.Severity) + ":" + diagnostic.Code
	}
	return result
}

// requireCodes сравнивает и состав, и порядок диагностик для правил Task 2.
func requireCodes(t *testing.T, input string, want []string) {
	t.Helper()
	report := validator.Validate(parser.Parse([]byte(input)))
	if got := codes(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics: got %v, want %v; report=%#v", got, want, report)
	}
}

// requireNoErrors отдельно проверяет отсутствие блокирующих диагностик,
// не закрепляя необязательные info/warning правила следующих задач.
func requireNoErrors(t *testing.T, input string) models.Report {
	t.Helper()
	report := validator.Validate(parser.Parse([]byte(input)))
	if report.HasErrors() {
		t.Fatalf("неожиданные error для %q: %#v", input, report.Diagnostics)
	}
	return report
}

// withCode выбирает диагностики одного правила, не связывая тест с будущими
// необязательными диагностиками других пакетов проверок.
func withCode(report models.Report, code string) []models.Diagnostic {
	result := make([]models.Diagnostic, 0)
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == code {
			result = append(result, diagnostic)
		}
	}
	return result
}

// TestNilDocument гарантирует безопасную обработку отсутствующего документа.
func TestNilDocument(t *testing.T) {
	t.Parallel()

	report := validator.Validate(nil)
	if len(report.Diagnostics) != 1 {
		t.Fatalf("ожидалась одна диагностика: %#v", report.Diagnostics)
	}
	diagnostic := report.Diagnostics[0]
	if diagnostic.Code != "document.nil" || diagnostic.Severity != models.SeverityError {
		t.Errorf("неверные code/severity: %#v", diagnostic)
	}
	if diagnostic.Line != 0 {
		t.Errorf("nil document не относится к физической строке: line=%d", diagnostic.Line)
	}
	if diagnostic.Message == "" {
		t.Error("сообщение document.nil не должно быть пустым")
	}
}

// TestEmptyAndInactiveDocuments проверяет, что служебные и отключённые строки
// не порождают диагностик, а пустой список остаётся non-nil для JSON-контракта.
func TestEmptyAndInactiveDocuments(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
	}{
		// Пустой вход не содержит активных записей и не должен считаться ошибкой.
		{name: "пустой документ", input: ""},
		// Пробелы, комментарии и пустые строки не влияют на активную конфигурацию.
		{name: "comments и blanks", input: "\n \t\n# обычный комментарий\n   # ещё комментарий\n"},
		// Даже корректная закомментированная запись не должна проходить field checks.
		{name: "commented entry", input: "# /dev/sda1 /mnt/data ext4 defaults 0 1\n"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := validator.Validate(parser.Parse([]byte(test.input)))
			if report.Diagnostics == nil {
				t.Fatal("Diagnostics должен быть non-nil даже при отсутствии проблем")
			}
			if len(report.Diagnostics) != 0 {
				t.Fatalf("неактивные узлы дали диагностики: %#v", report.Diagnostics)
			}
		})
	}
}

// TestInvalidActiveNode проверяет привязку parse.invalid к физической строке,
// включая предшествующие комментарии и blank lines.
func TestInvalidActiveNode(t *testing.T) {
	t.Parallel()

	input := "# комментарий\n\nbroken\n"
	report := validator.Validate(parser.Parse([]byte(input)))
	if got, want := codes(report), []string{"error:parse.invalid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics: got %v, want %v", got, want)
	}
	if got := report.Diagnostics[0].Line; got != 3 {
		t.Fatalf("физическая строка: got %d, want 3", got)
	}
}

// TestValidRootEntry не требует отсутствия будущего mount.system, но запрещает
// любые error для базовой корректной записи с абсолютным source и числами 0/1.
func TestValidRootEntry(t *testing.T) {
	t.Parallel()

	requireNoErrors(t, "/dev/sda1 / ext4 defaults 0 1")
}

// TestRequiredEmptyFields использует ручную модель, недостижимую через parser,
// чтобы каждое обязательное поле дало ровно одну ошибку без каскадных правил.
func TestRequiredEmptyFields(t *testing.T) {
	t.Parallel()

	document := &models.Document{Nodes: []models.Node{{
		Kind:  models.NodeEntry,
		Entry: &models.Entry{Line: 7},
	}}}
	report := validator.Validate(document)
	if got, want := codes(report), []string{
		"error:field.empty",
		"error:field.empty",
		"error:field.empty",
		"error:field.empty",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics: got %v, want %v; report=%#v", got, want, report)
	}
	wantFields := []string{"source", "mount_point", "fs_type", "options"}
	for index, field := range wantFields {
		if report.Diagnostics[index].Field != field || report.Diagnostics[index].Line != 7 {
			t.Errorf("диагностика %d имеет неверные field/line: %#v", index, report.Diagnostics[index])
		}
	}
}

// TestControls различает запрещённые raw/decoded NUL и control bytes,
// разрешённый escaped tab и предупреждение о прочих decoded controls.
func TestControls(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  []string
	}{
		// Литеральный NUL в raw source должен блокировать сохранение.
		{name: "raw NUL", input: "LABEL=A\x00B / ext4 defaults 0 0", want: []string{"error:field.control"}},
		// Другой литеральный C0 control также недопустим в исходном поле.
		{name: "raw C0 control", input: "LABEL=A\x01B / ext4 defaults 0 0", want: []string{"error:field.control"}},
		// DEL относится к control bytes наряду с диапазоном ниже 0x20.
		{name: "raw DEL", input: "LABEL=A\x7fB / ext4 defaults 0 0", want: []string{"error:field.control"}},
		// Octal NUL обнаруживается уже в decoded Value и остаётся error.
		{name: "decoded NUL", input: `LABEL=A\000B / ext4 defaults 0 0`, want: []string{"error:field.control"}},
		// Escaped tab разрешён ТЗ и не должен порождать control diagnostic.
		{name: "escaped tab", input: `LABEL=A\011B / ext4 defaults 0 0`, want: []string{}},
		// Иной escaped control не NUL сообщается как warning, а не error.
		{name: "escaped C0 control", input: `LABEL=A\001B / ext4 defaults 0 0`, want: []string{"warning:field.control_escaped"}},
		// Escaped DEL проходит тот же путь decoded control warning.
		{name: "escaped DEL", input: `LABEL=A\177B / ext4 defaults 0 0`, want: []string{"warning:field.control_escaped"}},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			requireCodes(t, test.input, test.want)
			report := validator.Validate(parser.Parse([]byte(test.input)))
			for _, diagnostic := range report.Diagnostics {
				if strings.HasPrefix(diagnostic.Code, "field.control") && diagnostic.Value != "" {
					t.Errorf("control diagnostic раскрывает значение: %#v", diagnostic)
				}
			}
		})
	}
}

// TestEscapes проверяет левостороннее сканирование Raw: неизвестный и неполный
// backslash заметны, а любые три octal digits образуют допустимый escape.
func TestEscapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  []string
	}{
		// Неизвестная буква после backslash должна дать warning без потери raw.
		{name: "unknown escape", input: `LABEL=A\qB / ext4 defaults 0 0`, want: []string{"warning:escape.unknown"}},
		// Две octal digits не образуют трёхзначный escape и тоже диагностируются.
		{name: "incomplete octal", input: `LABEL=A\04 / ext4 defaults 0 0`, want: []string{"warning:escape.unknown"}},
		// Пространство внутри значения кодируется штатным escape \040.
		{name: "space", input: `LABEL=A\040B / ext4 defaults 0 0`, want: []string{}},
		// Табуляция кодируется \011 и одновременно разрешена control-проверкой.
		{name: "tab", input: `LABEL=A\011B / ext4 defaults 0 0`, want: []string{}},
		// Backslash кодируется \134 и не должен повторно трактоваться как escape.
		{name: "backslash", input: `LABEL=A\134B / ext4 defaults 0 0`, want: []string{}},
		// Произвольный трёхзначный octal \041 также допустим по алгоритму brief.
		{name: "generic octal", input: `LABEL=A\041B / ext4 defaults 0 0`, want: []string{}},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			requireCodes(t, test.input, test.want)
		})
	}
}

// TestMountRules проверяет взаимоисключающие ветки mount validation и отсутствие
// каскадных mount.none/mount.relative для swap со специальной точкой none.
func TestMountRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  []string
	}{
		// Относительная точка монтирования небезопасна и должна быть отклонена.
		{name: "relative mount", input: "/dev/sda1 mnt/data ext4 defaults 0 0", want: []string{"error:mount.relative"}},
		// Специальная точка none не имеет смысла для обычной файловой системы.
		{name: "none for non-swap", input: "/dev/sda1 none ext4 defaults 0 0", want: []string{"error:mount.none"}},
		// Swap не монтируется в обычный абсолютный путь.
		{name: "absolute swap mount", input: "/dev/sda1 /swap swap defaults 0 0", want: []string{"error:swap.mount"}},
		// Единственная допустимая точка swap не должна давать mount errors.
		{name: "none for swap", input: "/dev/sda1 none swap defaults 0 0", want: []string{}},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			requireCodes(t, test.input, test.want)
		})
	}
}

// TestInvalidNumbers прогоняет одинаковые malformed values через dump и pass,
// чтобы общий helper не потерял стабильное имя конкретного optional-поля.
func TestInvalidNumbers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		field string
		value string
	}{
		// Отрицательный dump содержит non-digit и должен быть number.invalid.
		{name: "dump negative", field: "dump", value: "-1"},
		// Буквенный dump нельзя частично интерпретировать как число.
		{name: "dump letters", field: "dump", value: "x"},
		// Дробный dump содержит точку и не является decimal digits.
		{name: "dump decimal", field: "dump", value: "1.0"},
		// Отрицательный pass подчиняется той же lexical-проверке.
		{name: "pass negative", field: "pass", value: "-1"},
		// Буквенный pass должен быть error с Field=pass.
		{name: "pass letters", field: "pass", value: "x"},
		// Дробный pass не округляется и не принимается strconv-подобным prefix.
		{name: "pass decimal", field: "pass", value: "1.0"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := "/dev/sda1 /mnt/data ext4 defaults " + test.value + " 0"
			if test.field == "pass" {
				input = "/dev/sda1 /mnt/data ext4 defaults 0 " + test.value
			}
			requireCodes(t, input, []string{"error:number.invalid"})
			report := validator.Validate(parser.Parse([]byte(input)))
			if got := report.Diagnostics[0].Field; got != test.field {
				t.Fatalf("field: got %q, want %q", got, test.field)
			}
		})
	}
}

// TestUnusualNumbers отличает синтаксически допустимые, но нетипичные значения
// от malformed input: предупреждение не блокирует сохранение.
func TestUnusualNumbers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		field string
	}{
		// Dump 2 состоит только из digits, но выходит за типичные 0/1.
		{name: "dump 2", input: "/dev/sda1 /mnt/data ext4 defaults 2 0", field: "dump"},
		// Pass 3 состоит только из digits, но выходит за типичные 0/1/2.
		{name: "pass 3", input: "/dev/sda1 /mnt/data ext4 defaults 0 3", field: "pass"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			requireCodes(t, test.input, []string{"warning:number.unusual"})
			report := validator.Validate(parser.Parse([]byte(test.input)))
			if report.HasErrors() || report.Diagnostics[0].Field != test.field {
				t.Fatalf("неожиданная диагностика: %#v", report.Diagnostics[0])
			}
		})
	}
}

// TestVeryLongNumbers гарантирует отсутствие strconv overflow и panic: длинная
// digit string остаётся валидной лексически и получает только number.unusual.
func TestVeryLongNumbers(t *testing.T) {
	t.Parallel()

	longNumber := strings.Repeat("9", 10000)
	cases := []struct {
		name  string
		input string
		field string
	}{
		// Длинный dump не должен превращать overflow в number.invalid.
		{name: "long dump", input: "/dev/sda1 /mnt/data ext4 defaults " + longNumber + " 0", field: "dump"},
		// Длинный pass проверяется тем же посимвольным алгоритмом без conversion.
		{name: "long pass", input: "/dev/sda1 /mnt/data ext4 defaults 0 " + longNumber, field: "pass"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			requireCodes(t, test.input, []string{"warning:number.unusual"})
			report := validator.Validate(parser.Parse([]byte(test.input)))
			if report.Diagnostics[0].Field != test.field {
				t.Fatalf("field: got %q, want %q", report.Diagnostics[0].Field, test.field)
			}
		})
	}
}

// TestReadOnlyAndDeterministic проверяет сохранность round-trip bytes и базового
// SHA-256, а также DeepEqual двух независимых результатов Validate.
func TestReadOnlyAndDeterministic(t *testing.T) {
	t.Parallel()

	input := []byte("# comment\nLABEL=A\\qB relative mystery defaults 2 3\nbroken\n")
	document := parser.Parse(input)
	serializedBefore := document.Serialize()
	hashBefore := document.BaseSHA256
	first := validator.Validate(document)
	second := validator.Validate(document)

	if got := document.Serialize(); !bytes.Equal(got, serializedBefore) || !bytes.Equal(got, input) {
		t.Fatalf("Validate изменил Serialize: got %q, want %q", got, input)
	}
	if document.BaseSHA256 != hashBefore {
		t.Fatalf("Validate изменил BaseSHA256: got %x, want %x", document.BaseSHA256, hashBefore)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("повторный Validate не детерминирован:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

// TestAcceptedGenericSources проверяет все четыре label prefix с suffix и
// абсолютные пути device/file для обычной FS без source.invalid.
func TestAcceptedGenericSources(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
	}{
		// UUID с непустым suffix является стандартным способом задать device.
		{name: "UUID", source: "UUID=abc"},
		// LABEL с непустым suffix допустим без абсолютного пути.
		{name: "LABEL", source: "LABEL=data"},
		// PARTUUID идентифицирует partition и принимается generic source rule.
		{name: "PARTUUID", source: "PARTUUID=abc-01"},
		// PARTLABEL с непустым suffix также относится к поддерживаемым prefix.
		{name: "PARTLABEL", source: `PARTLABEL=Linux\040data`},
		// Абсолютный device path является базовым source для локальной FS.
		{name: "device path", source: "/dev/sda1"},
		// Абсолютный file path допустим для file-backed и bind-подобных записей.
		{name: "file path", source: "/var/lib/disk.img"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := requireNoErrors(t, test.source+" /mnt/data ext4 defaults 0 0")
			if got := withCode(report, "source.invalid"); len(got) != 0 {
				t.Fatalf("корректный source отклонён: %#v", got)
			}
		})
	}
}

// TestRejectedGenericSources покрывает пустые label suffix и неподдерживаемые
// относительные либо неструктурированные source для обычной FS.
func TestRejectedGenericSources(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
	}{
		// Prefix UUID= без suffix не идентифицирует устройство.
		{name: "empty UUID", source: "UUID="},
		// Prefix LABEL= без suffix не должен проходить только по совпадению prefix.
		{name: "empty LABEL", source: "LABEL="},
		// PARTUUID= требует непустой идентификатор после '='.
		{name: "empty PARTUUID", source: "PARTUUID="},
		// PARTLABEL= без имени partition должен быть source.invalid.
		{name: "empty PARTLABEL", source: "PARTLABEL="},
		// Относительный device path не является ни label, ни absolute path.
		{name: "relative path", source: "dev/sda1"},
		// Произвольное слово не заменяет source для неизвестной локальной записи.
		{name: "bare name", source: "disk"},
		// Отсутствие '=' не должно распознаваться как поддерживаемый UUID prefix.
		{name: "prefix without equals", source: "UUIDabc"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			requireCodes(t, test.source+" /mnt/data ext4 defaults 0 0", []string{"error:source.invalid"})
		})
	}
}

// TestAcceptedNetworkSources проверяет специальный синтаксис каждой network FS;
// валидный CIFS пример намеренно не содержит реальных credentials.
func TestAcceptedNetworkSources(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
		fsType string
	}{
		// CIFS требует UNC-подобный //host/share с обеими nonempty частями.
		{name: "CIFS", source: "//server/share", fsType: "cifs"},
		// Устаревший smbfs использует тот же сетевой source-синтаксис.
		{name: "smbfs", source: "//server/share", fsType: "smbfs"},
		// NFS принимает nonempty host и absolute exported path.
		{name: "NFS", source: "server:/export", fsType: "nfs"},
		// NFSv4 разделяет правила source с NFS.
		{name: "NFSv4", source: "server:/export/data", fsType: "nfs4"},
		// NFS root export '/' является непустым абсолютным path.
		{name: "NFS root", source: "server:/", fsType: "nfs"},
		// SSHFS допускает host:/path без необязательного user@.
		{name: "SSHFS host", source: "server:/home/user", fsType: "sshfs"},
		// SSHFS допускает непустой user перед host.
		{name: "SSHFS user and host", source: "user@server:/home/user", fsType: "sshfs"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := requireNoErrors(t, test.source+" /mnt/data "+test.fsType+" defaults 0 0")
			if got := withCode(report, "source.invalid"); len(got) != 0 {
				t.Fatalf("корректный network source отклонён: %#v", got)
			}
		})
	}
}

// TestRejectedNetworkSources гарантирует, что network FS не обходят свой
// синтаксис через generic absolute path или пустые host/share/path компоненты.
func TestRejectedNetworkSources(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		source string
		fsType string
	}{
		// CIFS без ведущего // не соответствует //host/share.
		{name: "CIFS missing slashes", source: "server/share", fsType: "cifs"},
		// Пустой host в ///share недопустим даже при наличии share.
		{name: "CIFS empty host", source: "///share", fsType: "cifs"},
		// //server/ не содержит nonempty share.
		{name: "CIFS empty share", source: "//server/", fsType: "cifs"},
		// //server не содержит separator и share.
		{name: "CIFS missing share", source: "//server", fsType: "cifs"},
		// Generic /dev path не должен заменять CIFS network source.
		{name: "CIFS absolute path bypass", source: "/dev/sda1", fsType: "cifs"},
		// smbfs разделяет CIFS validation и отклоняет отсутствующий // prefix.
		{name: "smbfs malformed", source: "server/share", fsType: "smbfs"},
		// NFS без ':' не содержит обязательную host:/path структуру.
		{name: "NFS missing colon", source: "server/export", fsType: "nfs"},
		// NFS с пустым host не должен принимать только absolute path справа.
		{name: "NFS empty host", source: ":/export", fsType: "nfs"},
		// NFS с пустым path после ':' неполон.
		{name: "NFS empty path", source: "server:", fsType: "nfs"},
		// NFS export должен начинаться с '/', а не быть relative path.
		{name: "NFS relative path", source: "server:export", fsType: "nfs"},
		// NFS не может подменить network source локальным /dev path.
		{name: "NFS absolute path bypass", source: "/dev/sda1", fsType: "nfs"},
		// NFSv4 применяет те же требования и отклоняет строку без ':'.
		{name: "NFSv4 missing colon", source: "server/export", fsType: "nfs4"},
		// SSHFS требует nonempty host перед ':'.
		{name: "SSHFS empty host", source: ":/home/user", fsType: "sshfs"},
		// SSHFS требует nonempty absolute path после ':'.
		{name: "SSHFS empty path", source: "server:", fsType: "sshfs"},
		// Relative SSHFS path не соответствует host:/path.
		{name: "SSHFS relative path", source: "server:home/user", fsType: "sshfs"},
		// Если user@ указан, user перед '@' не должен быть пустым.
		{name: "SSHFS empty user", source: "@server:/home/user", fsType: "sshfs"},
		// Generic absolute path не обходит специальную SSHFS-проверку.
		{name: "SSHFS absolute path bypass", source: "/dev/sda1", fsType: "sshfs"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			want := []string{"error:source.invalid"}
			if test.fsType == "smbfs" {
				want = append(want, "warning:fs.deprecated")
			}
			requireCodes(t, test.source+" /mnt/data "+test.fsType+" defaults 0 0", want)
		})
	}
}

// TestPseudoSources проверяет, что pseudo FS и любые fuse.* принимают любой
// nonempty source, не применяя к ним generic absolute path restriction.
func TestPseudoSources(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fsType string
	}{
		// proc является pseudo FS и принимает условный source proc.
		{name: "proc", fsType: "proc"},
		// sysfs не требует device path.
		{name: "sysfs", fsType: "sysfs"},
		// tmpfs допускает произвольный nonempty source.
		{name: "tmpfs", fsType: "tmpfs"},
		// devtmpfs относится к встроенному pseudo list.
		{name: "devtmpfs", fsType: "devtmpfs"},
		// devpts принимает pseudo source без '/'.
		{name: "devpts", fsType: "devpts"},
		// cgroup не должен проходить generic source rule.
		{name: "cgroup", fsType: "cgroup"},
		// cgroup2 разделяет pseudo semantics с cgroup.
		{name: "cgroup2", fsType: "cgroup2"},
		// overlay допускает произвольный nonempty source.
		{name: "overlay", fsType: "overlay"},
		// Любой prefix fuse. включает pseudo source behavior.
		{name: "fuse subtype", fsType: "fuse.custom"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := requireNoErrors(t, "arbitrary /mnt/data "+test.fsType+" defaults 0 0")
			if got := withCode(report, "source.invalid"); len(got) != 0 {
				t.Fatalf("pseudo source ошибочно отклонён: %#v", got)
			}
		})
	}
}

// TestKnownAndDeprecatedFSTypes охватывает весь встроенный список FS, fuse.*,
// единственный deprecated smbfs и отсутствие fs.unknown у известных типов.
func TestKnownAndDeprecatedFSTypes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		fsType     string
		deprecated bool
	}{
		// ext2 входит во встроенный список локальных FS.
		{name: "ext2", fsType: "ext2"},
		// ext3 не должен получать fs.unknown.
		{name: "ext3", fsType: "ext3"},
		// ext4 является базовой известной FS.
		{name: "ext4", fsType: "ext4"},
		// xfs входит в список поддерживаемых типов.
		{name: "xfs", fsType: "xfs"},
		// btrfs известен validator без внешних утилит.
		{name: "btrfs", fsType: "btrfs"},
		// vfat известен и не требует сетевого source.
		{name: "vfat", fsType: "vfat"},
		// exfat входит в перечень известных FS.
		{name: "exfat", fsType: "exfat"},
		// ntfs не должен порождать info fs.unknown.
		{name: "ntfs", fsType: "ntfs"},
		// ntfs3 отдельно перечислен как известный драйвер.
		{name: "ntfs3", fsType: "ntfs3"},
		// swap известен; его source и mount выбираются helper ниже.
		{name: "swap", fsType: "swap"},
		// tmpfs является одновременно known и pseudo FS.
		{name: "tmpfs", fsType: "tmpfs"},
		// proc является известной pseudo FS.
		{name: "proc", fsType: "proc"},
		// sysfs входит в known list.
		{name: "sysfs", fsType: "sysfs"},
		// devtmpfs входит в known list.
		{name: "devtmpfs", fsType: "devtmpfs"},
		// devpts входит в known list.
		{name: "devpts", fsType: "devpts"},
		// cgroup является известной pseudo FS.
		{name: "cgroup", fsType: "cgroup"},
		// cgroup2 отдельно учитывается в known list.
		{name: "cgroup2", fsType: "cgroup2"},
		// overlay известен без внешнего lookup.
		{name: "overlay", fsType: "overlay"},
		// squashfs использует generic absolute source.
		{name: "squashfs", fsType: "squashfs"},
		// iso9660 входит во встроенный список optical FS.
		{name: "iso9660", fsType: "iso9660"},
		// udf известен и принимает generic absolute source.
		{name: "udf", fsType: "udf"},
		// nfs известен и использует network source helper.
		{name: "nfs", fsType: "nfs"},
		// nfs4 отдельно входит в known list.
		{name: "nfs4", fsType: "nfs4"},
		// cifs известен и принимает //server/share.
		{name: "cifs", fsType: "cifs"},
		// smbfs известен, но обязан дополнительно дать fs.deprecated/warning.
		{name: "smbfs", fsType: "smbfs", deprecated: true},
		// sshfs известен и использует host:/path.
		{name: "sshfs", fsType: "sshfs"},
		// Базовый fuse входит в known list, хотя source остаётся generic.
		{name: "fuse", fsType: "fuse"},
		// Любой fuse.* считается известным без исчерпывающего списка subtype.
		{name: "fuse subtype", fsType: "fuse.custom"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source, mount := sourceAndMountForFS(test.fsType)
			report := requireNoErrors(t, source+" "+mount+" "+test.fsType+" defaults 0 0")
			if got := withCode(report, "fs.unknown"); len(got) != 0 {
				t.Fatalf("известная FS получила fs.unknown: %#v", got)
			}
			deprecated := withCode(report, "fs.deprecated")
			if test.deprecated {
				if len(deprecated) != 1 || deprecated[0].Severity != models.SeverityWarning {
					t.Fatalf("smbfs должен дать один warning fs.deprecated: %#v", deprecated)
				}
			} else if len(deprecated) != 0 {
				t.Fatalf("неожиданный fs.deprecated: %#v", deprecated)
			}
		})
	}
}

// TestUnknownFSType требует info для неизвестного типа, но сохраняет остальные
// проверки активными: корректные source/mount/numbers не дают error.
func TestUnknownFSType(t *testing.T) {
	t.Parallel()

	report := requireNoErrors(t, "/dev/sda1 /mnt/data mysteryfs defaults 0 0")
	unknown := withCode(report, "fs.unknown")
	if len(unknown) != 1 || unknown[0].Severity != models.SeverityInfo {
		t.Fatalf("ожидался один info fs.unknown: %#v", report.Diagnostics)
	}
	if unknown[0].Field != "fs_type" || unknown[0].Value != "mysteryfs" {
		t.Fatalf("fs.unknown потерял сведения о поле: %#v", unknown[0])
	}
}

// TestSourceDiagnosticRedaction проверяет malformed CIFS с credentials: ни
// Value, ни Params, ни Message source diagnostic не должны копировать source.
func TestSourceDiagnosticRedaction(t *testing.T) {
	t.Parallel()

	const source = "user:secret@example/share"
	report := validator.Validate(parser.Parse([]byte(source + " /mnt/data cifs defaults 0 0")))
	sourceDiagnostics := withCode(report, "source.invalid")
	if len(sourceDiagnostics) != 1 {
		t.Fatalf("ожидался один source.invalid: %#v", report.Diagnostics)
	}
	for _, diagnostic := range sourceDiagnostics {
		if strings.Contains(diagnostic.Value, source) || strings.Contains(diagnostic.Message, source) {
			t.Errorf("source diagnostic раскрывает credentials: %#v", diagnostic)
		}
		for key, value := range diagnostic.Params {
			if strings.Contains(key, source) || strings.Contains(value, source) {
				t.Errorf("Params раскрывает credentials: %#v", diagnostic.Params)
			}
		}
		if diagnostic.Value != "" {
			t.Errorf("Value network source должен оставаться пустым: %q", diagnostic.Value)
		}
	}
}

// sourceAndMountForFS возвращает заведомо допустимые source/mount, чтобы FS test
// изолировал fs.unknown/fs.deprecated от самостоятельных source и mount rules.
func sourceAndMountForFS(fsType string) (string, string) {
	switch fsType {
	case "cifs", "smbfs":
		return "//server/share", "/mnt/data"
	case "nfs", "nfs4":
		return "server:/export", "/mnt/data"
	case "sshfs":
		return "server:/home/user", "/mnt/data"
	case "proc", "sysfs", "tmpfs", "devtmpfs", "devpts", "cgroup", "cgroup2", "overlay":
		return "arbitrary", "/mnt/data"
	case "swap":
		return "/dev/sda1", "none"
	default:
		if strings.HasPrefix(fsType, "fuse.") {
			return "arbitrary", "/mnt/data"
		}
		return "/dev/sda1", "/mnt/data"
	}
}
