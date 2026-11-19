# План реализации — validator и diagnostics model

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** реализовать детерминированную внутреннюю проверку активных записей fstab с ошибками, предупреждениями и информационными диагностиками в единой машиночитаемой модели.

**Architecture:** публичные `Diagnostic`, `Severity` и `Report` находятся в `pkg/models`, чтобы их могли объединять validator, будущий findmnt adapter, CLI и transaction. Пакет `internal/validator` только читает `models.Document`, возвращает все независимые диагностики в порядке документа и не выполняет I/O. Семантические проверки отключённых записей и проверки среды не входят в этот план.

**Tech Stack:** Go 1.22+, стандартная библиотека, unit/table-driven/fuzz/race tests, `go vet`, `golangci-lint`; новых зависимостей нет.

## Global Constraints

- Рабочая область выполнения: `/home/sergey/projects/fstab-editor/work/.worktrees/stage-1-validator`.
- Модуль `fstab-edit`, минимальная версия Go `1.22.0`.
- Все файлы получают максимально содержательные русские комментарии согласно `docs/stage-1-tz.md`, раздел 4.3.
- Validator не читает и не изменяет `/etc/fstab`, не запускает команды и не изменяет переданный документ или его `BaseSHA256`.
- Diagnostic codes, severity и JSON keys — стабильные английские machine-readable значения; сообщения сейчас русские, будущая i18n использует Code/Params, а не разбор Message.
- Проверяются только `NodeEntry` и активные `NodeInvalid`. `NodeCommentedEntry`, обычные comments и blank nodes не создают семантических ошибок и не участвуют в duplicates.
- `Report.Diagnostics` всегда не nil, включая валидный документ, чтобы JSON давал `[]`, а не `null`.
- Severity: `error` запрещает сохранение, `warning` требует отображения/подтверждения, `info` не блокирует.
- Source и значения опций могут содержать секреты: validator не заполняет `Diagnostic.Value` для source/options и не помещает их в Params. Безопасные mount point и номера полей допустимы.
- Неизвестный FS type — info, неизвестная option — warning; встроенные списки не являются исчерпывающими whitelist.
- Offline/internal validation не включает фактическое наличие устройств, mount points, FS helpers и не заменяет будущий `findmnt --verify`.
- Этот план не создаёт CLI, findmnt adapter, writer, backup, config или TUI.

---

## Структура файлов и стабильные коды

- `pkg/models/diagnostic.go` — Severity, Diagnostic, Report и `HasErrors`.
- `pkg/models/diagnostic_test.go` — JSON keys, severity, empty report и blocking errors.
- `internal/validator/validator.go` — orchestration, diagnostics builder и проверки узлов/полей.
- `internal/validator/entry.go` — source, mount, FS type и dump/pass.
- `internal/validator/options.go` — список опций, duplicates/conflicts, known options и специальные подсказки.
- `internal/validator/validator_test.go` — invalid nodes, поля, mount/source/FS/numbers и отсутствие мутаций.
- `internal/validator/options_test.go` — опции, duplicates, special warnings и детерминированный порядок.
- `internal/validator/fuzz_test.go` — отсутствие panic, сохранение документа и допустимые severity.
- `README.md` — фактический статус validator и граница внутренней проверки.

Коды, которые должны появляться только в указанных ситуациях:

| Code | Severity | Ситуация |
|------|----------|----------|
| `document.nil` | error | nil document |
| `parse.invalid` | error | активная строка не разобрана |
| `field.empty` | error | пустое обязательное поле |
| `field.control` | error | raw control byte или decoded NUL |
| `field.control_escaped` | warning | decoded control, кроме допустимого escaped tab; NUL остаётся error |
| `escape.unknown` | warning | backslash без трёх последующих octal digits |
| `mount.relative` | error | mount не absolute и не разрешённый swap `none` |
| `mount.none` | error | `none` у не-swap записи |
| `swap.mount` | error | swap с mount point, отличным от `none` |
| `number.invalid` | error | dump/pass не состоят только из decimal digits |
| `number.unusual` | warning | dump не 0/1 или pass не 0/1/2 |
| `source.invalid` | error | пустой suffix или несовместимый синтаксис source |
| `fs.unknown` | info | неизвестный непустой FS type |
| `fs.deprecated` | warning | `smbfs` |
| `duplicate.mount` | error | повтор mount point среди active entries |
| `duplicate.source` | warning | повтор source среди active entries |
| `option.empty` | error | пустой элемент между/вокруг commas |
| `option.duplicate` | warning | повтор exact option |
| `option.conflict` | warning | mutually exclusive pair или разные значения одного key; last-wins |
| `option.unknown` | warning | неизвестное имя option |
| `mount.system` | warning | `/`, `/boot`, `/boot/efi`, `/usr` |
| `swap.review` | warning | active swap entry |
| `option.noauto` | info | `noauto` |
| `option.nofail` | info | `nofail` |
| `option.netdev` | info | `_netdev` или network FS |
| `option.bind` | info | `bind`/`rbind` |

### Task 1: Публичная модель диагностик

**Files:**
- Create: `pkg/models/diagnostic.go`
- Create: `pkg/models/diagnostic_test.go`

**Interfaces:**
- Consumes: только стандартную библиотеку.
- Produces: `type Severity string`, `SeverityError`, `SeverityWarning`, `SeverityInfo`; `type Diagnostic`; `type Report`; `func (Report) HasErrors() bool`.

- [ ] **Step 1: Написать failing tests модели**

Создать `pkg/models/diagnostic_test.go` с package comment и тремя тестами:

```go
package models_test

import (
	"encoding/json"
	"testing"

	"fstab-edit/pkg/models"
)

// TestDiagnosticJSON фиксирует machine-readable ключи и enum values первой схемы.
func TestDiagnosticJSON(t *testing.T) {
	t.Parallel()

	diagnostic := models.Diagnostic{
		Code: "mount.relative", Severity: models.SeverityError,
		Line: 4, Field: "mount_point", Value: "relative/path",
		Message: "Точка монтирования должна быть абсолютной",
		Params: map[string]string{"rule": "absolute"},
	}
	data, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"mount.relative","severity":"error","line":4,"field":"mount_point","value":"relative/path","message":"Точка монтирования должна быть абсолютной","params":{"rule":"absolute"}}`
	if string(data) != want {
		t.Fatalf("JSON:\ngot  %s\nwant %s", data, want)
	}
}

// TestReportHasErrors проверяет, что warning/info не блокируют запись,
// а хотя бы один error устанавливает блокирующий результат.
func TestReportHasErrors(t *testing.T) {
	t.Parallel()

	report := models.Report{Diagnostics: []models.Diagnostic{
		{Code: "fs.unknown", Severity: models.SeverityInfo},
		{Code: "option.unknown", Severity: models.SeverityWarning},
	}}
	if report.HasErrors() {
		t.Fatal("warning и info не должны считаться ошибками")
	}
	report.Diagnostics = append(report.Diagnostics, models.Diagnostic{Code: "parse.invalid", Severity: models.SeverityError})
	if !report.HasErrors() {
		t.Fatal("error должен блокировать сохранение")
	}
}
```

Добавить test, что `omitempty` удаляет Line/Field/Value/Params при нулевых значениях, а Severity и Message остаются обязательными. Не требовать от Report собственного JSON marshal: срез Diagnostics проверяется validator.

Run: `go test ./pkg/models -run='Test(Diagnostic|Report)' -v`
Expected: FAIL, типы ещё не определены.

- [ ] **Step 2: Реализовать diagnostic.go**

Создать файл с русскими file/package/API comments:

```go
// Package models содержит общие модели документа и диагностик. Модель Diagnostic
// не зависит от validator, CLI или внешних утилит и пригодна для объединения
// внутренних и внешних результатов проверки.
package models

// Severity определяет влияние диагностики на сохранение документа.
type Severity string

const (
	// SeverityError обозначает недопустимый документ и запрещает сохранение.
	SeverityError Severity = "error"
	// SeverityWarning обозначает риск, который требует отображения и подтверждения.
	SeverityWarning Severity = "warning"
	// SeverityInfo обозначает пояснение без блокировки сохранения.
	SeverityInfo Severity = "info"
)

// Diagnostic описывает один стабильный результат проверки. Line — one-based
// physical line или 0; Field — стабильное имя поля; Value заполняется только
// при отсутствии риска раскрытия секретов. Params содержит структурированные
// данные для i18n/output, а Message — текущее русское человекочитаемое сообщение.
type Diagnostic struct {
	Code     string            `json:"code"`
	Severity Severity          `json:"severity"`
	Line     int               `json:"line,omitempty"`
	Field    string            `json:"field,omitempty"`
	Value    string            `json:"value,omitempty"`
	Message  string            `json:"message"`
	Params   map[string]string `json:"params,omitempty"`
}

// Report объединяет диагностики всех внутренних и внешних проверок.
type Report struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// HasErrors сообщает, запрещает ли хотя бы одна диагностика сохранение.
// Метод не изменяет report и безопасен для нулевого Report.
func (r Report) HasErrors() bool {
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Проверить и закоммитить**

Run: `gofmt -w pkg/models/diagnostic.go pkg/models/diagnostic_test.go && go test ./pkg/models -v && go test ./...`
Expected: PASS.

```bash
git add pkg/models/diagnostic.go pkg/models/diagnostic_test.go
git commit -m "feat: add diagnostics model"
```

### Task 2: Основные проверки записей

**Files:**
- Create: `internal/validator/validator.go`
- Create: `internal/validator/entry.go`
- Create: `internal/validator/validator_test.go`

**Interfaces:**
- Consumes: `models.Document`, `models.NodeEntry`, `models.NodeInvalid`, `models.Report`.
- Produces: `func validator.Validate(document *models.Document) models.Report`; внутренние helpers принимают `*collector`, чтобы все пакеты правил использовали одинаковый порядок и модель.

- [ ] **Step 1: Написать failing tests orchestration и полей**

Создать `internal/validator/validator_test.go` с внешним package `validator_test`. Каждый table case получает русский комментарий о входе и обнаруживаемой регрессии. Обязательные сценарии:

1. Nil document → ровно `document.nil/error`, Line 0, непустой Message.
2. Пустой документ и comments/blanks/commented entries → валидный Report с `Diagnostics != nil` и len 0.
3. Активная `broken` строка → `parse.invalid/error` с физическим Line.
4. Корректная `/dev/sda1 / ext4 defaults 0 1` не даёт error; специальное warning `mount.system` для `/` появится и будет проверено в Task 3.
5. Все четыре обязательных поля с manually constructed empty Value → отдельные `field.empty`, без каскадных `mount.relative`, `source.invalid` и `fs.unknown` для пустых значений.
6. Raw NUL/control и decoded `\000` → `field.control/error`; decoded `\011` не даёт control diagnostic; другой escaped control → `field.control_escaped/warning`.
7. `LABEL=A\qB`, `LABEL=A\04` → `escape.unknown/warning`; `\040`, `\011`, `\134`, `\041` не дают escape diagnostic.
8. Relative mount, `none` у не-swap и swap с `/swap` → соответственно `mount.relative`, `mount.none`, `swap.mount`; swap с `none` не получает mount errors.
9. Dump/pass `-1`, `x`, `1.0` → `number.invalid/error`; dump 2 и pass 3 → `number.unusual/warning`; очень длинная строка digits не вызывает overflow/panic и получает только warning.
10. Validator не меняет Serialize и BaseSHA256, а повторный вызов возвращает идентичный Report (`reflect.DeepEqual`).

Пример helper для assertions:

```go
func codes(report models.Report) []string {
	result := make([]string, len(report.Diagnostics))
	for index, diagnostic := range report.Diagnostics {
		result[index] = string(diagnostic.Severity) + ":" + diagnostic.Code
	}
	return result
}

func requireCodes(t *testing.T, input string, want []string) {
	t.Helper()
	report := validator.Validate(parser.Parse([]byte(input)))
	if got := codes(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics: got %v, want %v; report=%#v", got, want, report)
	}
}
```

Для `/` теста использовать фильтр по коду, потому что Task 3 добавит info/warning правила; общий suite Task 2 после Task 3 не должен зависеть от числа необязательных info. Для правил, уже перечисленных в кодах Task 2, проверять отсутствие неожиданных error отдельно через `Report.HasErrors`.

Run: `go test ./internal/validator -v`
Expected: FAIL, пакет ещё не существует.

- [ ] **Step 2: Реализовать orchestration и collector**

`validator.go` должен содержать:

```go
// Package validator выполняет детерминированные внутренние проверки активных
// записей fstab. Он не обращается к среде, файлам и внешним командам.
package validator

import "fstab-edit/pkg/models"

// collector накапливает diagnostics в порядке обхода документа и правил.
type collector struct {
	report models.Report
}

func newCollector() *collector {
	return &collector{report: models.Report{Diagnostics: make([]models.Diagnostic, 0)}}
}

// add добавляет одну заполненную диагностику без дедупликации по тексту.
func (c *collector) add(diagnostic models.Diagnostic) {
	c.report.Diagnostics = append(c.report.Diagnostics, diagnostic)
}

// Validate возвращает все внутренние диагностики и не изменяет document.
func Validate(document *models.Document) models.Report {
	collector := newCollector()
	if document == nil {
		collector.add(models.Diagnostic{Code: "document.nil", Severity: models.SeverityError, Message: "Документ не задан"})
		return collector.report
	}
	for index, node := range document.Nodes {
		switch node.Kind {
		case models.NodeInvalid:
			collector.add(models.Diagnostic{Code: "parse.invalid", Severity: models.SeverityError, Line: index + 1, Message: "Активная строка не является корректной записью fstab"})
		case models.NodeEntry:
			validateEntry(collector, node.Entry)
		case models.NodeBlank, models.NodeComment, models.NodeCommentedEntry:
			// Отключённые записи и служебные узлы не влияют на активную конфигурацию.
		}
	}
	validateDuplicates(collector, document)
	return collector.report
}
```

`validateDuplicates` будет реализован в Task 3; в Task 2 создать в `validator.go` временную функцию с реальным пустым поведением нельзя, поэтому в Task 2 сразу реализовать только orchestration без вызова `validateDuplicates`, а Task 3 добавит вызов после per-entry обхода. В итоговом коде Task 2 `Validate` заканчивается `return collector.report`.

- [ ] **Step 3: Реализовать проверки полей и escapes**

В `validateEntry` последовательно проверить Source, MountPoint, FSType, Options, Dump/Pass. Общий helper:

```go
func validateField(collector *collector, line int, name string, field models.Field, required bool) {
	if required && field.Value == "" {
		collector.add(models.Diagnostic{Code: "field.empty", Severity: models.SeverityError, Line: line, Field: name, Message: "Обязательное поле не может быть пустым"})
		return
	}
	validateControls(collector, line, name, field)
	validateEscapes(collector, line, name, field.Raw)
}
```

`validateControls` сканирует bytes Raw: values `< 0x20` и `0x7f` дают `field.control/error`. В decoded Value NUL даёт `field.control/error`; другие control bytes, кроме `\t`, дают `field.control_escaped/warning`. Не дублировать decoded diagnostic, если тот же Raw уже получил `field.control`; escaped tab разрешён ТЗ. Diagnostic.Value для control не заполнять.

`validateEscapes` сканирует Raw слева направо. При backslash с тремя следующими octal digits пропускает четыре bytes; иначе добавляет `escape.unknown/warning` и продвигается на один byte. Не использовать decoded Value для поиска escapes.

- [ ] **Step 4: Реализовать mount, numbers, source и FS**

В `entry.go` реализовать отдельные функции `validateMount`, `validateNumber`, `validateSource`, `validateFSType`, каждая с полными русскими комментариями и guard против пустого Field.Value.

Mount:

```go
func validateMount(collector *collector, entry *models.Entry) {
	mount := entry.MountPoint.Value
	if mount == "" {
		return
	}
	if entry.FSType.Value == "swap" {
		if mount != "none" {
			collector.add(models.Diagnostic{Code: "swap.mount", Severity: models.SeverityError, Line: entry.Line, Field: "mount_point", Value: mount, Message: "Для swap точка монтирования должна быть none"})
		}
		return
	}
	if mount == "none" {
		collector.add(models.Diagnostic{Code: "mount.none", Severity: models.SeverityError, Line: entry.Line, Field: "mount_point", Value: mount, Message: "Специальная точка none допустима только для swap"})
		return
	}
	if !strings.HasPrefix(mount, "/") {
		collector.add(models.Diagnostic{Code: "mount.relative", Severity: models.SeverityError, Line: entry.Line, Field: "mount_point", Value: mount, Message: "Точка монтирования должна быть абсолютной"})
	}
}
```

Numbers: `number.invalid/error`, если Value содержит хотя бы один не-digit или пуст. Не вызывать strconv.Atoi: длинные decimal strings остаются синтаксически допустимыми. Типичные exact values dump `0/1`, pass `0/1/2`; остальные digit strings получают `number.unusual/warning`. Field names — `dump`, `pass`.

Source:

- Сначала проверяются network/pseudo FS правила, затем generic absolute path: network FS не должен обходить свой синтаксис через `/dev/...`.
- `UUID=`, `LABEL=`, `PARTUUID=`, `PARTLABEL=` требуют непустой suffix.
- Absolute path допустим для device/file/bind, но не заменяет network source для network FS.
- Для `cifs`/`smbfs` требуется `//host/share` с непустыми host и share.
- Для `nfs`/`nfs4` требуется непустой `host:/path`; source без такой структуры получает `source.invalid`.
- Для `sshfs` допустим непустой `[user@]host:/path`.
- Для pseudo FS `proc`, `sysfs`, `tmpfs`, `devtmpfs`, `devpts`, `cgroup`, `cgroup2`, `overlay` и строк с prefix `fuse.` допускается любое непустое source.
- Для остальных FS допускаются только четыре label prefix с suffix или absolute path; иной непустой source получает `source.invalid/error`.
- Не копировать source в Value/Params/Message: network source может содержать credentials.

FS: `smbfs` получает `fs.deprecated/warning`. Непустой тип вне встроенного списка получает `fs.unknown/info`. Встроенный список: `ext2`, `ext3`, `ext4`, `xfs`, `btrfs`, `vfat`, `exfat`, `ntfs`, `ntfs3`, `swap`, `tmpfs`, `proc`, `sysfs`, `devtmpfs`, `devpts`, `cgroup`, `cgroup2`, `overlay`, `squashfs`, `iso9660`, `udf`, `nfs`, `nfs4`, `cifs`, `smbfs`, `sshfs`, `fuse`; любой `fuse.*` считается известным. Неизвестный тип не отключает остальные проверки.

- [ ] **Step 5: Добавить source/FS tests и проверить**

Добавить отдельные table cases для каждого accepted/rejected source и всех перечисленных known/unknown/deprecated FS. Проверить, что diagnostics source не содержат исходный source в `Value`, `Params` и `Message` на malformed CIFS-входе `user:secret@example/share` без требуемого `//`. Для network valid case использовать `//server/share` без реальных credentials.

Run: `gofmt -w internal/validator && go test ./internal/validator -v && go test ./...`
Expected: PASS.

```bash
git add internal/validator
git commit -m "feat: validate fstab entry fields"
```

### Task 3: Options, duplicates, special diagnostics, fuzz и README

**Files:**
- Modify: `internal/validator/validator.go`
- Create: `internal/validator/options.go`
- Create: `internal/validator/options_test.go`
- Create: `internal/validator/fuzz_test.go`
- Modify: `internal/validator/validator_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `Validate`, collector и `validateEntry` из Task 2.
- Produces: полный внутренний Report, включая options/duplicates/special diagnostics; публичный API не меняется.

- [ ] **Step 1: Написать failing tests опций и duplicates**

Создать `options_test.go`. Все cases содержат русские comments и проверяют Code/Severity/Line/Field/Params, а не только количество diagnostics.

Обязательные сценарии:

1. `defaults`, `rw`, `noatime`, `nofail`, `_netdev`, `bind`, `rbind`, `uid=1000`, `errors=remount-ro` не получают `option.unknown`.
2. `rw,` и `,rw` и `rw,,noatime` → `option.empty/error`; пустое Options не разбирается повторно как option list после `field.empty`.
3. `rw,rw` → `option.duplicate/warning`; `rw,ro` → `option.conflict/warning` с Params `effective=ro`, `first=rw`, `last=ro`.
4. Аналогичные pairs: `auto/noauto`, `user/nouser`, `exec/noexec`, `suid/nosuid`, `dev/nodev`, `async/sync`, `atime/noatime`, `diratime/nodiratime`.
5. `size=100,size=200` → conflict last-wins; `size=100,size=100` → duplicate exact; `size=100,uid=1000` — не conflict.
6. `definitelyunknown` → `option.unknown/warning`; сообщение не содержит исходное значение option, Value пуст.
7. `/`, `/boot`, `/boot/efi`, `/usr` → `mount.system/warning`; `/mnt/data` — нет.
8. Active swap → `swap.review/warning`; noauto/nofail/_netdev/network FS и bind/rbind получают соответствующие info один раз на entry.
9. Две active entries с одинаковым decoded mount → `duplicate.mount/error` на второй строке с Params `first_line`; duplicate source → `duplicate.source/warning`. Escaped и decoded duplicates распознаются.
10. Active + commented с теми же mount/source не дают duplicate; два commented тоже не дают duplicate.
11. Три duplicates создают диагностики для второй и третьей active entry в порядке документа; первый line сохраняется в Params.
12. Результат двух вызовов Validate идентичен; document bytes/BaseSHA256 не изменяются.

Run: `go test ./internal/validator -run='Test(Options|Duplicates|Special)' -v`
Expected: FAIL по отсутствующим diagnostics/правилам, не по компиляции уже существующего API.

- [ ] **Step 2: Реализовать options.go**

В `validateEntry` после общей проверки поля Options добавить вызов `validateOptions(collector, entry)`, а после проверок FS/mount — `validateSpecial(collector, entry)`; оба вызова используют существующий collector и не меняют API Validate.

В `validateOptions` разобрать `entry.Options.Value` через `strings.Split(value, ",")` только если value непусто. Сохранять index и raw option для внутреннего анализа, но не выводить значение в diagnostic.

Для каждой option:

- Пустой item → `option.empty/error`, Field `options`, Params `index` с one-based позицией.
- Имя — часть до первого `=`. Пустое имя при nonempty item (`=x`) считать `option.empty/error`.
- Exact duplicate → `option.duplicate/warning`.
- Для mutually exclusive pairs присутствие двух сторон → одна `option.conflict/warning` на entry, с `first`, `last`, `effective`; last occurrence побеждает.
- Для `key=value` при повторе того же key с другим value → `option.conflict/warning`, Params только `key` и `effective=last`, без самих values.
- Unknown option name → один `option.unknown/warning` на уникальное имя; не копировать name/value в Message/Params/Value.

Known exact options:

```text
defaults ro rw auto noauto user nouser users owner group
exec noexec suid nosuid dev nodev async sync dirsync
atime noatime relatime norelatime strictatime nostrictatime
diratime nodiratime nofail _netdev bind rbind remount loop
mand nomand acl noacl discard nodiscard
```

Known prefixes с непустым suffix: `uid=`, `gid=`, `mode=`, `umask=`, `dmask=`, `fmask=`, `offset=`, `size=`, `errors=`, `credentials=`, `username=`, `password=`, `workdir=`, `lowerdir=`, `upperdir=`. Значения не валидировать и не выводить: это зона последующих FS-specific/environment checks, а credentials могут содержать секреты.

Special diagnostics выполняются после разбора options: mount.system, swap.review, noauto/nofail/netdev/bind. Network FS — `nfs`, `nfs4`, `cifs`, `smbfs`, `sshfs` и `fuse.*`; `_netdev` и network FS совместно создают только одну `option.netdev` info. `bind` и `rbind` совместно создают одну `option.bind` info.

- [ ] **Step 3: Реализовать duplicates после per-entry проверки**

Добавить вызов `validateDuplicates(collector, document)` после цикла в Validate. Использовать map decoded value → first physical line только для `NodeEntry` с `Entry != nil` и непустым значением:

```go
func validateDuplicates(collector *collector, document *models.Document) {
	firstMount := make(map[string]int)
	firstSource := make(map[string]int)
	for index, node := range document.Nodes {
		if node.Kind != models.NodeEntry || node.Entry == nil {
			continue
		}
		line := index + 1
		checkDuplicate(collector, firstMount, node.Entry.MountPoint.Value, line, "mount_point", "duplicate.mount", models.SeverityError)
		checkDuplicate(collector, firstSource, node.Entry.Source.Value, line, "source", "duplicate.source", models.SeverityWarning)
	}
}
```

`checkDuplicate` пропускает пустое value; при первом occurrence сохраняет line, при повторе добавляет diagnostic с Line текущей строки и Params `first_line` через `strconv.Itoa`. Не включать source/mount в Params duplicate source; для mount можно Value=mount. Сообщения русские, source не раскрывается.

- [ ] **Step 4: Добавить fuzz validator**

Создать `FuzzValidate` с seeds для valid, invalid UTF-8, controls, duplicates и escaped options. На произвольном input:

```go
document := parser.Parse(input)
before := append([]byte(nil), document.Serialize()...)
base := document.BaseSHA256
report := validator.Validate(document)
if !bytes.Equal(before, document.Serialize()) || base != document.BaseSHA256 {
	t.Fatal("Validate изменил документ")
}
for _, diagnostic := range report.Diagnostics {
	switch diagnostic.Severity {
	case models.SeverityError, models.SeverityWarning, models.SeverityInfo:
	default:
		t.Fatalf("неизвестная severity: %q", diagnostic.Severity)
	}
	if diagnostic.Code == "" || diagnostic.Message == "" {
		t.Fatalf("незаполненная diagnostic: %#v", diagnostic)
	}
}
if !reflect.DeepEqual(report, validator.Validate(document)) {
	t.Fatal("Validate не детерминирован")
}
```

Добавить explicit test, что secrets из source/options не встречаются в сериализованном Report JSON для malformed входа с marker `do-not-leak`. Не требовать redaction исходного Node.Raw: ограничение относится только к diagnostics.

- [ ] **Step 5: Обновить README и выполнить полную проверку**

В статусе README отметить реализованный внутренний validator и diagnostics model. Явно перечислить, что environment/device/helper checks и `findmnt --verify` ещё не подключены. Не заявлять готовность CLI или первого этапа.

Run:

```bash
gofmt -w internal/validator pkg/models
go test ./...
go vet ./...
go test -race ./...
golangci-lint run
go test ./... -cover
go test ./internal/validator -run=FuzzValidate -fuzz=FuzzValidate -fuzztime=3s
```

Expected: все PASS, lint 0 issues. Записать фактический coverage, не объявляя весь stage-1 завершённым.

```bash
git add internal/validator README.md
git commit -m "feat: validate fstab options and duplicates"
```

## Финальная приёмка плана

1. Все Diagnostics имеют стабильный Code, допустимую Severity и непустое Message.
2. Валидный документ возвращает non-nil пустой Diagnostics и `HasErrors() == false`.
3. Все error codes из таблицы имеют хотя бы один direct test; warnings/info проверяются по коду и severity.
4. Source/options secrets не попадают в Diagnostic Value/Message/Params/JSON.
5. Commented entries не влияют на save validation и duplicates.
6. Validator детерминирован, не изменяет документ/BaseSHA256 и проходит fuzz/race.
7. Общий whole-branch review подтверждает отсутствие Critical/Important findings.
8. После merge в `main` повторить `go test ./...`, проверить status и отправить `main` в уже настроенный SSH `origin`.
