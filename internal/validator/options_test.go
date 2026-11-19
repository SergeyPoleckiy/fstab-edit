// Файл options_test.go проверяет разбор опций, специальные диагностики и
// повторы mount/source, включая допустимость нескольких swap с точкой none.
// Все сценарии используют документы в памяти и не обращаются к /etc/fstab.
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

// requireOneDiagnostic проверяет единственную диагностику указанного кода со
// всеми машиночитаемыми атрибутами. Message проверяется на непустоту отдельно,
// чтобы тест не зависел от будущей русской формулировки.
func requireOneDiagnostic(t *testing.T, report models.Report, want models.Diagnostic) {
	t.Helper()
	diagnostics := withCode(report, want.Code)
	if len(diagnostics) != 1 {
		t.Fatalf("код %s: получено %d диагностик, ожидалась одна; report=%#v", want.Code, len(diagnostics), report.Diagnostics)
	}
	got := diagnostics[0]
	if got.Code != want.Code || got.Severity != want.Severity || got.Line != want.Line ||
		got.Field != want.Field || got.Value != want.Value || !reflect.DeepEqual(got.Params, want.Params) {
		t.Fatalf("диагностика не совпадает:\ngot  %#v\nwant %#v", got, want)
	}
	if got.Message == "" {
		t.Fatalf("диагностика %s имеет пустое сообщение", want.Code)
	}
}

// requireNoCode отдельно фиксирует отсутствие конкретного правила, не ограничивая
// независимые информационные диагностики, которые положены той же записи.
func requireNoCode(t *testing.T, report models.Report, code string) {
	t.Helper()
	if diagnostics := withCode(report, code); len(diagnostics) != 0 {
		t.Fatalf("неожиданный %s: %#v", code, diagnostics)
	}
}

// TestOptionsKnownExact охватывает полный встроенный список точных опций: ни одна
// из них не должна попадать в общий warning option.unknown.
func TestOptionsKnownExact(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		option string
	}{
		// defaults является стандартным сокращением набора опций.
		{name: "defaults", option: "defaults"},
		// ro задаёт доступ только для чтения.
		{name: "ro", option: "ro"},
		// rw задаёт доступ для чтения и записи.
		{name: "rw", option: "rw"},
		// auto разрешает автоматическое монтирование.
		{name: "auto", option: "auto"},
		// noauto отключает автоматическое монтирование.
		{name: "noauto", option: "noauto"},
		// user разрешает монтирование обычному пользователю.
		{name: "user", option: "user"},
		// nouser отменяет разрешение user.
		{name: "nouser", option: "nouser"},
		// users допускает размонтирование другим пользователем.
		{name: "users", option: "users"},
		// owner учитывает владельца устройства.
		{name: "owner", option: "owner"},
		// group учитывает группу устройства.
		{name: "group", option: "group"},
		// exec разрешает исполнение файлов.
		{name: "exec", option: "exec"},
		// noexec запрещает исполнение файлов.
		{name: "noexec", option: "noexec"},
		// suid разрешает set-user-ID биты.
		{name: "suid", option: "suid"},
		// nosuid отключает set-user-ID семантику.
		{name: "nosuid", option: "nosuid"},
		// dev разрешает файлы устройств.
		{name: "dev", option: "dev"},
		// nodev запрещает интерпретацию файлов устройств.
		{name: "nodev", option: "nodev"},
		// async выбирает асинхронный ввод-вывод.
		{name: "async", option: "async"},
		// sync выбирает синхронный ввод-вывод.
		{name: "sync", option: "sync"},
		// dirsync синхронно выполняет операции с каталогами.
		{name: "dirsync", option: "dirsync"},
		// atime включает обновление времени доступа.
		{name: "atime", option: "atime"},
		// noatime отключает обновление времени доступа.
		{name: "noatime", option: "noatime"},
		// relatime выбирает относительное обновление atime.
		{name: "relatime", option: "relatime"},
		// norelatime отключает relatime.
		{name: "norelatime", option: "norelatime"},
		// strictatime требует строгого обновления atime.
		{name: "strictatime", option: "strictatime"},
		// nostrictatime отключает strictatime.
		{name: "nostrictatime", option: "nostrictatime"},
		// diratime включает atime для каталогов.
		{name: "diratime", option: "diratime"},
		// nodiratime отключает atime для каталогов.
		{name: "nodiratime", option: "nodiratime"},
		// nofail разрешает продолжить загрузку при ошибке монтирования.
		{name: "nofail", option: "nofail"},
		// _netdev помечает зависимость от сети.
		{name: "_netdev", option: "_netdev"},
		// bind обозначает bind-монтирование.
		{name: "bind", option: "bind"},
		// rbind обозначает рекурсивное bind-монтирование.
		{name: "rbind", option: "rbind"},
		// remount обозначает повторное монтирование.
		{name: "remount", option: "remount"},
		// loop указывает использование loop-устройства.
		{name: "loop", option: "loop"},
		// mand включает обязательные блокировки.
		{name: "mand", option: "mand"},
		// nomand отключает обязательные блокировки.
		{name: "nomand", option: "nomand"},
		// acl включает списки контроля доступа.
		{name: "acl", option: "acl"},
		// noacl отключает списки контроля доступа.
		{name: "noacl", option: "noacl"},
		// discard включает немедленное освобождение блоков.
		{name: "discard", option: "discard"},
		// nodiscard отключает немедленное освобождение блоков.
		{name: "nodiscard", option: "nodiscard"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 " + test.option + " 0 0")))
			requireNoCode(t, report, "option.unknown")
		})
	}
}

// TestOptionsKnownPrefixes проверяет все разрешённые key=value формы. Значения
// намеренно разнообразны, но не должны попадать в диагностики validator.
func TestOptionsKnownPrefixes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		option string
	}{
		// uid задаёт числового владельца файлов.
		{name: "uid", option: "uid=1000"},
		// gid задаёт числовую группу файлов.
		{name: "gid", option: "gid=1000"},
		// mode задаёт режим доступа.
		{name: "mode", option: "mode=0755"},
		// umask задаёт маску прав.
		{name: "umask", option: "umask=022"},
		// dmask задаёт маску прав каталогов.
		{name: "dmask", option: "dmask=022"},
		// fmask задаёт маску прав файлов.
		{name: "fmask", option: "fmask=133"},
		// offset задаёт смещение внутри файла-образа.
		{name: "offset", option: "offset=4096"},
		// size ограничивает размер файловой системы.
		{name: "size", option: "size=100"},
		// errors выбирает реакцию на ошибки ФС.
		{name: "errors", option: "errors=remount-ro"},
		// credentials указывает файл с секретами и не требует раскрытия пути.
		{name: "credentials", option: "credentials=/root/cifs-secret"},
		// username передаёт имя пользователя сетевой ФС.
		{name: "username", option: "username=somebody"},
		// password может содержать секрет, который нельзя копировать в Report.
		{name: "password", option: "password=do-not-leak"},
		// workdir задаёт рабочий каталог overlay.
		{name: "workdir", option: "workdir=/tmp/work"},
		// lowerdir задаёт нижний слой overlay.
		{name: "lowerdir", option: "lowerdir=/lower"},
		// upperdir задаёт верхний слой overlay.
		{name: "upperdir", option: "upperdir=/upper"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 " + test.option + " 0 0")))
			requireNoCode(t, report, "option.unknown")
		})
	}
}

// TestOptionsEmptyItems проверяет пустые элементы вокруг и между запятыми, а
// также пустое имя перед '='. Индекс в Params всегда one-based.
func TestOptionsEmptyItems(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		options string
		index   string
	}{
		// Завершающая запятая создаёт пустой второй элемент.
		{name: "trailing comma", options: "rw,", index: "2"},
		// Начальная запятая создаёт пустой первый элемент.
		{name: "leading comma", options: ",rw", index: "1"},
		// Две запятые подряд создают пустой элемент между rw и noatime.
		{name: "double comma", options: "rw,,noatime", index: "2"},
		// Значение без имени опции недопустимо, даже если элемент непустой.
		{name: "missing name", options: "=x", index: "1"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 " + test.options + " 0 0")))
			requireOneDiagnostic(t, report, models.Diagnostic{
				Code: "option.empty", Severity: models.SeverityError, Line: 1,
				Field: "options", Params: map[string]string{"index": test.index},
			})
		})
	}
}

// TestOptionsEmptyFieldNotSplit использует ручную модель для пустого Options:
// после field.empty не должно появляться вторичное option.empty.
func TestOptionsEmptyFieldNotSplit(t *testing.T) {
	t.Parallel()

	document := &models.Document{Nodes: []models.Node{{
		Kind: models.NodeEntry,
		Entry: &models.Entry{
			Line: 7, Active: true,
			Source:     models.Field{Raw: "/dev/sda1", Value: "/dev/sda1"},
			MountPoint: models.Field{Raw: "/mnt/data", Value: "/mnt/data"},
			FSType:     models.Field{Raw: "ext4", Value: "ext4"},
		},
	}}}
	report := validator.Validate(document)
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "field.empty", Severity: models.SeverityError, Line: 7, Field: "options",
	})
	requireNoCode(t, report, "option.empty")
}

// TestOptionsExactDuplicate требует warning без копирования значения options в
// Diagnostic.Value или Params и проверяет физическую строку записи.
func TestOptionsExactDuplicate(t *testing.T) {
	t.Parallel()

	report := validator.Validate(parser.Parse([]byte("\n# пояснение\n/dev/sda1 /mnt/data ext4 rw,rw 0 0")))
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "option.duplicate", Severity: models.SeverityWarning, Line: 3, Field: "options",
	})
}

// TestOptionsMutuallyExclusivePairs проверяет каждую заданную пару и единый
// last-wins контракт: first/last содержат безопасные имена, effective равен last.
func TestOptionsMutuallyExclusivePairs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		first string
		last  string
	}{
		// rw и ro задают противоположные режимы записи.
		{name: "rw ro", first: "rw", last: "ro"},
		// auto и noauto противоположно управляют автомонтированием.
		{name: "auto noauto", first: "auto", last: "noauto"},
		// user и nouser противоположно управляют пользовательским монтированием.
		{name: "user nouser", first: "user", last: "nouser"},
		// exec и noexec противоположно управляют исполнением.
		{name: "exec noexec", first: "exec", last: "noexec"},
		// suid и nosuid противоположно управляют set-ID битами.
		{name: "suid nosuid", first: "suid", last: "nosuid"},
		// dev и nodev противоположно управляют файлами устройств.
		{name: "dev nodev", first: "dev", last: "nodev"},
		// async и sync выбирают противоположные режимы ввода-вывода.
		{name: "async sync", first: "async", last: "sync"},
		// atime и noatime противоположно управляют временем доступа.
		{name: "atime noatime", first: "atime", last: "noatime"},
		// diratime и nodiratime управляют atime каталогов.
		{name: "diratime nodiratime", first: "diratime", last: "nodiratime"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 " + test.first + "," + test.last + " 0 0")))
			requireOneDiagnostic(t, report, models.Diagnostic{
				Code: "option.conflict", Severity: models.SeverityWarning, Line: 1, Field: "options",
				Params: map[string]string{"first": test.first, "last": test.last, "effective": test.last},
			})
		})
	}
}

// TestOptionsPairLastOccurrenceWins проверяет чередование сторон одной пары:
// создаётся один conflict, а last/effective отражают последнее вхождение.
func TestOptionsPairLastOccurrenceWins(t *testing.T) {
	t.Parallel()

	report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 rw,ro,rw 0 0")))
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "option.conflict", Severity: models.SeverityWarning, Line: 1, Field: "options",
		Params: map[string]string{"first": "rw", "last": "rw", "effective": "rw"},
	})
}

// TestOptionsValuedDuplicatesAndConflicts различает exact duplicate, смену
// значения одного ключа и независимые ключи. Значения не входят в Params.
func TestOptionsValuedDuplicatesAndConflicts(t *testing.T) {
	t.Parallel()

	t.Run("разные значения одного ключа", func(t *testing.T) {
		// Последнее size должно победить без раскрытия старого или нового значения.
		report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 size=100,size=200 0 0")))
		requireOneDiagnostic(t, report, models.Diagnostic{
			Code: "option.conflict", Severity: models.SeverityWarning, Line: 1, Field: "options",
			Params: map[string]string{"key": "size", "effective": "last"},
		})
		requireNoCode(t, report, "option.duplicate")
	})
	t.Run("одинаковые значения одного ключа", func(t *testing.T) {
		// Полностью одинаковые size являются exact duplicate, а не conflict.
		report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 size=100,size=100 0 0")))
		requireOneDiagnostic(t, report, models.Diagnostic{
			Code: "option.duplicate", Severity: models.SeverityWarning, Line: 1, Field: "options",
		})
		requireNoCode(t, report, "option.conflict")
	})
	t.Run("разные ключи не конфликтуют", func(t *testing.T) {
		// size и uid независимы, несмотря на одинаковый key=value синтаксис.
		report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 size=100,uid=1000 0 0")))
		requireNoCode(t, report, "option.conflict")
		requireNoCode(t, report, "option.duplicate")
	})
}

// TestOptionsUnknownRedacted проверяет уникальность warning по имени и полное
// отсутствие исходного имени/значения в Message, Value и Params.
func TestOptionsUnknownRedacted(t *testing.T) {
	t.Parallel()

	const option = "definitelyunknown"
	report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 " + option + "," + option + " 0 0")))
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "option.unknown", Severity: models.SeverityWarning, Line: 1, Field: "options",
	})
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "option.duplicate", Severity: models.SeverityWarning, Line: 1, Field: "options",
	})
	diagnostic := withCode(report, "option.unknown")[0]
	if strings.Contains(diagnostic.Message, option) {
		t.Fatalf("Message раскрывает неизвестную опцию: %#v", diagnostic)
	}
}

// TestOptionsUnknownValuedNameRedacted проверяет повтор неизвестного ключа с
// разными значениями: даже побочный conflict не должен переносить имя в Params.
func TestOptionsUnknownValuedNameRedacted(t *testing.T) {
	t.Parallel()

	const name = "do-not-leak"
	input := "/dev/sda1 /mnt/data ext4 " + name + "=first," + name + "=second 0 0"
	report := validator.Validate(parser.Parse([]byte(input)))
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "option.unknown", Severity: models.SeverityWarning, Line: 1, Field: "options",
	})
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "option.conflict", Severity: models.SeverityWarning, Line: 1, Field: "options",
		Params: map[string]string{"effective": "last"},
	})
	for _, diagnostic := range report.Diagnostics {
		if strings.Contains(diagnostic.Message, name) || strings.Contains(diagnostic.Value, name) {
			t.Fatalf("диагностика раскрывает имя опции: %#v", diagnostic)
		}
		for key, value := range diagnostic.Params {
			if strings.Contains(key, name) || strings.Contains(value, name) {
				t.Fatalf("Params раскрывает имя опции: %#v", diagnostic.Params)
			}
		}
	}
}

// TestOptionsValidDocumentHasEmptyReport фиксирует JSON/API контракт валидной
// активной записи: Diagnostics non-nil и пуст, а HasErrors возвращает false.
func TestOptionsValidDocumentHasEmptyReport(t *testing.T) {
	t.Parallel()

	report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 defaults 0 0")))
	if report.Diagnostics == nil || len(report.Diagnostics) != 0 || report.HasErrors() {
		t.Fatalf("валидный документ должен иметь пустой non-nil Report: %#v", report)
	}
}

// TestSpecialSystemMounts выделяет системные точки монтирования предупреждением,
// но не помечает обычный каталог /mnt/data.
func TestSpecialSystemMounts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		mount string
		want  bool
	}{
		// Корневая ФС критична для загрузки и требует предупреждения.
		{name: "root", mount: "/", want: true},
		// Отдельный /boot содержит загрузчик и ядра.
		{name: "boot", mount: "/boot", want: true},
		// EFI system partition монтируется в /boot/efi.
		{name: "EFI", mount: "/boot/efi", want: true},
		// Отдельный /usr критичен для системных программ.
		{name: "usr", mount: "/usr", want: true},
		// Пользовательский каталог данных не относится к системному списку.
		{name: "ordinary data", mount: "/mnt/data", want: false},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := validator.Validate(parser.Parse([]byte("/dev/sda1 " + test.mount + " ext4 defaults 0 0")))
			if !test.want {
				requireNoCode(t, report, "mount.system")
				return
			}
			requireOneDiagnostic(t, report, models.Diagnostic{
				Code: "mount.system", Severity: models.SeverityWarning, Line: 1,
				Field: "mount_point", Value: test.mount,
			})
		})
	}
}

// TestSpecialSwap требует одно предупреждение для активной корректной swap entry.
func TestSpecialSwap(t *testing.T) {
	t.Parallel()

	report := validator.Validate(parser.Parse([]byte("/dev/sda1 none swap defaults 0 0")))
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "swap.review", Severity: models.SeverityWarning, Line: 1, Field: "fs_type",
	})
}

// TestSpecialOptionInfo проверяет стабильные info-коды для отдельных опций.
func TestSpecialOptionInfo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		options string
		code    string
	}{
		// noauto требует напомнить о ручном монтировании.
		{name: "noauto", options: "noauto", code: "option.noauto"},
		// nofail меняет поведение загрузки при недоступности записи.
		{name: "nofail", options: "nofail", code: "option.nofail"},
		// _netdev отмечает сетевую зависимость даже для локального FS type.
		{name: "netdev option", options: "_netdev", code: "option.netdev"},
		// bind связывает существующее поддерево.
		{name: "bind", options: "bind", code: "option.bind"},
		// rbind дополнительно распространяется на вложенные монтирования.
		{name: "rbind", options: "rbind", code: "option.bind"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 " + test.options + " 0 0")))
			requireOneDiagnostic(t, report, models.Diagnostic{
				Code: test.code, Severity: models.SeverityInfo, Line: 1, Field: "options",
			})
		})
	}
}

// TestSpecialNetworkFilesystems проверяет все network FS и fuse.*; обычный fuse
// не входит в заданный список и не должен получать option.netdev.
func TestSpecialNetworkFilesystems(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fsType string
		want   bool
	}{
		// NFS требует сетевой инициализации.
		{name: "NFS", fsType: "nfs", want: true},
		// NFSv4 имеет ту же сетевую зависимость.
		{name: "NFSv4", fsType: "nfs4", want: true},
		// CIFS обращается к удалённому SMB resource.
		{name: "CIFS", fsType: "cifs", want: true},
		// Устаревший smbfs также остаётся сетевой ФС.
		{name: "smbfs", fsType: "smbfs", want: true},
		// SSHFS использует удалённое SSH-соединение.
		{name: "SSHFS", fsType: "sshfs", want: true},
		// Любой fuse.* считается сетевым по правилу внутреннего validator.
		{name: "FUSE subtype", fsType: "fuse.custom", want: true},
		// Базовый fuse без подтипа не входит в явно заданное network правило.
		{name: "plain FUSE", fsType: "fuse", want: false},
		// ext4 является локальной ФС без неявного netdev info.
		{name: "ext4", fsType: "ext4", want: false},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source, mount := sourceAndMountForFS(test.fsType)
			report := validator.Validate(parser.Parse([]byte(source + " " + mount + " " + test.fsType + " defaults 0 0")))
			if !test.want {
				requireNoCode(t, report, "option.netdev")
				return
			}
			requireOneDiagnostic(t, report, models.Diagnostic{
				Code: "option.netdev", Severity: models.SeverityInfo, Line: 1, Field: "options",
			})
		})
	}
}

// TestSpecialOnlyOncePerEntry проверяет дедупликацию info при повторах опций и
// при одновременном наличии _netdev и сетевого FS type.
func TestSpecialOnlyOncePerEntry(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		code  string
	}{
		// Повтор noauto даёт duplicate warning, но info остаётся одним.
		{name: "noauto twice", input: "/dev/sda1 /mnt/data ext4 noauto,noauto 0 0", code: "option.noauto"},
		// Повтор nofail аналогично не умножает info.
		{name: "nofail twice", input: "/dev/sda1 /mnt/data ext4 nofail,nofail 0 0", code: "option.nofail"},
		// bind и rbind совместно описывают одну специальную подсказку.
		{name: "bind and rbind", input: "/dev/sda1 /mnt/data ext4 bind,rbind 0 0", code: "option.bind"},
		// _netdev на NFS не добавляет второе info поверх network FS правила.
		{name: "network and option", input: "server:/export /mnt/data nfs _netdev 0 0", code: "option.netdev"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			report := validator.Validate(parser.Parse([]byte(test.input)))
			requireOneDiagnostic(t, report, models.Diagnostic{
				Code: test.code, Severity: models.SeverityInfo, Line: 1, Field: "options",
			})
		})
	}
}

// TestSpecialFollowsOptionDiagnostics фиксирует порядок правил внутри записи:
// сначала разбор options, затем специальные info в стабильном порядке.
func TestSpecialFollowsOptionDiagnostics(t *testing.T) {
	t.Parallel()

	report := validator.Validate(parser.Parse([]byte("/dev/sda1 /mnt/data ext4 rw,rw,noauto,nofail 0 0")))
	want := []string{"warning:option.duplicate", "info:option.noauto", "info:option.nofail"}
	if got := codes(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок diagnostics: got %v, want %v", got, want)
	}
}

// TestDuplicatesActiveEntries проверяет mount error и source warning на второй
// активной строке, включая first_line и запрет раскрытия source.
func TestDuplicatesActiveEntries(t *testing.T) {
	t.Parallel()

	input := "/dev/sda1 /mnt/data ext4 defaults 0 0\n/dev/sda1 /mnt/data ext4 defaults 0 0\n"
	report := validator.Validate(parser.Parse([]byte(input)))
	if got, want := codes(report), []string{"error:duplicate.mount", "warning:duplicate.source"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics: got %v, want %v; report=%#v", got, want, report.Diagnostics)
	}
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "duplicate.mount", Severity: models.SeverityError, Line: 2,
		Field: "mount_point", Value: "/mnt/data", Params: map[string]string{"first_line": "1"},
	})
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "duplicate.source", Severity: models.SeverityWarning, Line: 2,
		Field: "source", Params: map[string]string{"first_line": "1"},
	})
}

// TestDuplicatesSwapNoneAllowed разрешает несколько активных swap-записей с
// разными source: специальная точка none не является общей точкой монтирования.
func TestDuplicatesSwapNoneAllowed(t *testing.T) {
	t.Parallel()

	input := "/dev/sda1 none swap defaults 0 0\n/dev/sdb1 none swap defaults 0 0\n"
	report := validator.Validate(parser.Parse([]byte(input)))
	want := []string{"warning:swap.review", "warning:swap.review"}
	if got := codes(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics: got %v, want %v; report=%#v", got, want, report.Diagnostics)
	}
	requireNoCode(t, report, "duplicate.mount")
	if report.HasErrors() {
		t.Fatalf("несколько swap с none не должны блокировать сохранение: %#v", report.Diagnostics)
	}
}

// TestDuplicatesSwapSourceStillChecked сохраняет независимую проверку source,
// даже когда повторная специальная точка none исключена из mount duplicates.
func TestDuplicatesSwapSourceStillChecked(t *testing.T) {
	t.Parallel()

	input := "/dev/sda1 none swap defaults 0 0\n/dev/sda1 none swap defaults 0 0\n"
	report := validator.Validate(parser.Parse([]byte(input)))
	want := []string{"warning:swap.review", "warning:swap.review", "warning:duplicate.source"}
	if got := codes(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics: got %v, want %v; report=%#v", got, want, report.Diagnostics)
	}
	requireNoCode(t, report, "duplicate.mount")
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "duplicate.source", Severity: models.SeverityWarning, Line: 2,
		Field: "source", Params: map[string]string{"first_line": "1"},
	})
}

// TestDuplicatesOrdinaryAbsoluteMount подтверждает, что исключение для swap none
// не ослабляет блокирующую диагностику повторной обычной точки монтирования.
func TestDuplicatesOrdinaryAbsoluteMount(t *testing.T) {
	t.Parallel()

	input := "/dev/sda1 /mnt/data ext4 defaults 0 0\n/dev/sdb1 /mnt/data ext4 defaults 0 0\n"
	report := validator.Validate(parser.Parse([]byte(input)))
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "duplicate.mount", Severity: models.SeverityError, Line: 2,
		Field: "mount_point", Value: "/mnt/data", Params: map[string]string{"first_line": "1"},
	})
	if !report.HasErrors() {
		t.Fatalf("повтор ordinary absolute mount должен блокировать сохранение: %#v", report.Diagnostics)
	}
}

// TestDuplicatesEscapedValues проверяет сравнение decoded значений: разные Raw
// записи одного source и mount должны распознаваться как duplicates.
func TestDuplicatesEscapedValues(t *testing.T) {
	t.Parallel()

	input := "LABEL=data /mnt/data ext4 defaults 0 0\nLABEL=d\\141ta /mnt/d\\141ta ext4 defaults 0 0\n"
	report := validator.Validate(parser.Parse([]byte(input)))
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "duplicate.mount", Severity: models.SeverityError, Line: 2,
		Field: "mount_point", Value: "/mnt/data", Params: map[string]string{"first_line": "1"},
	})
	requireOneDiagnostic(t, report, models.Diagnostic{
		Code: "duplicate.source", Severity: models.SeverityWarning, Line: 2,
		Field: "source", Params: map[string]string{"first_line": "1"},
	})
}

// TestDuplicatesIgnoreCommentedEntries гарантирует, что активная и отключённая
// записи, а также две отключённые записи не влияют на duplicate maps.
func TestDuplicatesIgnoreCommentedEntries(t *testing.T) {
	t.Parallel()

	input := "/dev/sda1 /mnt/data ext4 defaults 0 0\n" +
		"# /dev/sda1 /mnt/data ext4 defaults 0 0\n" +
		"# /dev/sda1 /mnt/data ext4 defaults 0 0\n"
	report := validator.Validate(parser.Parse([]byte(input)))
	if len(report.Diagnostics) != 0 {
		t.Fatalf("commented entries создали диагностики: %#v", report.Diagnostics)
	}
}

// TestDuplicatesThreeEntries проверяет диагностики второй и третьей строк в
// порядке документа и сохранение номера самой первой записи в Params.
func TestDuplicatesThreeEntries(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("/dev/sda1 /mnt/data ext4 defaults 0 0\n", 3)
	report := validator.Validate(parser.Parse([]byte(input)))
	wantCodes := []string{
		"error:duplicate.mount", "warning:duplicate.source",
		"error:duplicate.mount", "warning:duplicate.source",
	}
	if got := codes(report); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("порядок diagnostics: got %v, want %v", got, wantCodes)
	}
	for index, diagnostic := range report.Diagnostics {
		wantLine := index/2 + 2
		if diagnostic.Line != wantLine || diagnostic.Params["first_line"] != "1" {
			t.Errorf("неверные line/first_line у диагностики %d: %#v", index, diagnostic)
		}
	}
}

// TestOptionsReadOnlyAndDeterministic прогоняет новые правила дважды по одному
// документу и проверяет неизменность исходных байтов и BaseSHA256.
func TestOptionsReadOnlyAndDeterministic(t *testing.T) {
	t.Parallel()

	input := []byte("# /dev/sda1 /mnt/data ext4 defaults 0 0\n" +
		"/dev/sda1 /mnt/data ext4 rw,ro,noauto,nofail 0 0\n" +
		"/dev/sda1 /mnt/data ext4 size=100,size=200,bind,rbind 0 0\n")
	document := parser.Parse(input)
	before := append([]byte(nil), document.Serialize()...)
	base := document.BaseSHA256
	first := validator.Validate(document)
	second := validator.Validate(document)
	if !bytes.Equal(before, document.Serialize()) {
		t.Fatalf("Validate изменил документ:\nbefore %q\nafter  %q", before, document.Serialize())
	}
	if base != document.BaseSHA256 {
		t.Fatalf("Validate изменил BaseSHA256: got %x, want %x", document.BaseSHA256, base)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Validate не детерминирован:\nfirst  %#v\nsecond %#v", first, second)
	}
}
