package tui

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"fstab-edit/internal/editor"
	"fstab-edit/pkg/models"
)

// ansiPattern выделяет ANSI SGS-последовательности: lipgloss оборачивает
// стилизованные ячейки кодами вида "\x1b[38;5;81m...\x1b[0m", значение внутри
// не разрывается, поэтому для проверки выравнивания колонок коды достаточно
// удалить и мерить оставшийся plain-текст.
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSI удаляет ANSI-коды из строки, оставляя видимый текст.
func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }

// problemFstab — документ с реальными диагностиками валидатора: первая строка
// содержит error (mount.relative: относительная точка монтирования), вторая —
// warning (fs.deprecated: smbfs устарел). Остальные поля валидны, чтобы
// кэш содержал ровно две записи.
const problemFstab = "UUID=aaa data ext4 defaults 0 1\n//srv/share /mnt/smb smbfs defaults 0 0\n"

// TestSeverityCacheOnLoad проверяет, что NewModel сразу строит кэш
// диагностик: error и warning на разных строках попадают в severityByLine,
// info-диагностики и чистые строки — нет.
func TestSeverityCacheOnLoad(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, problemFstab)))
	if got := m.severityByLine[1]; got != models.SeverityError {
		t.Fatalf("строка 1: severity %q, ожидался error", got)
	}
	if got := m.severityByLine[2]; got != models.SeverityWarning {
		t.Fatalf("строка 2: severity %q, ожидался warning", got)
	}
	if len(m.severityByLine) != 2 {
		t.Fatalf("кэш должен содержать ровно 2 строки, получено %d: %v", len(m.severityByLine), m.severityByLine)
	}
}

// TestSeverityCacheErrorBeatsWarning: на одной строке error и warning дают
// в кэше наихудшую severity (error).
func TestSeverityCacheErrorBeatsWarning(t *testing.T) {
	// mount "data" — относительный путь (error), smbfs — deprecated (warning):
	// обе диагностики на строке 1, побеждает error.
	content := "UUID=ccc data smbfs defaults 0 0\n"
	m := testModel(t, testOptions(t, writeTestFstab(t, content)))
	if got := m.severityByLine[1]; got != models.SeverityError {
		t.Fatalf("строка 1: severity %q, error должен побеждать warning", got)
	}
}

// TestSeverityCacheRebuildAfterMutationAndUndo: добавление записи с
// предупреждением через applyOp + rebuildDiagnostics добавляет строку в кэш;
// undo + rebuildDiagnostics возвращает кэш к исходному состоянию.
func TestSeverityCacheRebuildAfterMutationAndUndo(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, problemFstab)))

	add := func() {
		t.Helper()
		err := m.session.applyOp(m.document, func(d *models.Document) error {
			_, err := editor.Add(d, editor.NewEntry{
				Source: "//other/share", MountPoint: "/mnt/other", FSType: "smbfs", Options: "defaults",
			})
			return err
		})
		if err != nil {
			t.Fatalf("applyOp: %v", err)
		}
		// Точка пересчёта, как в updateTable/updateForm после успешной мутации.
		m.rebuildDiagnostics()
	}

	add()
	if got := m.severityByLine[3]; got != models.SeverityWarning {
		t.Fatalf("строка 3 (новая smbfs-запись): severity %q, ожидался warning", got)
	}
	if len(m.severityByLine) != 3 {
		t.Fatalf("после мутации кэш должен содержать 3 строки, получено %d", len(m.severityByLine))
	}

	if !m.session.undo(m.document) {
		t.Fatal("undo должен вернуть true")
	}
	m.rebuildDiagnostics()
	if len(m.severityByLine) != 2 {
		t.Fatalf("после undo кэш должен вернуться к 2 строкам, получено %d: %v",
			len(m.severityByLine), m.severityByLine)
	}
	if _, ok := m.severityByLine[3]; ok {
		t.Fatal("после undo строка 3 не должна оставаться в кэше")
	}
}

// TestSeverityCacheReloadRebuilds: reloadFromDisk (включая сценарий restore,
// который приходит через него же) пересчитывает кэш по фактическим байтам target.
func TestSeverityCacheReloadRebuilds(t *testing.T) {
	target := writeTestFstab(t, problemFstab)
	m := testModel(t, testOptions(t, target))
	if len(m.severityByLine) != 2 {
		t.Fatalf("исходный кэш: 2 строки, получено %d", len(m.severityByLine))
	}

	// На диске — чистый документ: после перезагрузки кэш пустеет. Mount "/"
	// нельзя использовать как «чистый»: валидатор даёт на него warning
	// «системная точка монтирования».
	clean := "UUID=aaa /mnt/clean ext4 defaults 0 1\n"
	if err := os.WriteFile(target, []byte(clean), 0o600); err != nil {
		t.Fatalf("перезаписать target: %v", err)
	}
	if err := m.reloadFromDisk(); err != nil {
		t.Fatalf("reloadFromDisk: %v", err)
	}
	if len(m.severityByLine) != 0 {
		t.Fatalf("после reload кэш должен быть пуст, получено %v", m.severityByLine)
	}
}

// TestSeverityCacheNilDocument: rebuildDiagnostics при document == nil
// (loadErr-экран) даёт пустую non-nil map — рендер таблицы читает кэш без
// nil-проверок.
func TestSeverityCacheNilDocument(t *testing.T) {
	m := model{}
	m.rebuildDiagnostics()
	if m.severityByLine == nil {
		t.Fatal("severityByLine не должен быть nil после rebuildDiagnostics")
	}
	if len(m.severityByLine) != 0 {
		t.Fatalf("severityByLine при nil-документе должен быть пуст, получено %v", m.severityByLine)
	}
}

// TestViewTableSeverityMarkers: viewTable показывает текстовые маркеры
// состояния по диагностикам — "!" для error, "?" для warning — и сохраняет
// прежние "+" для чистых активных записей. Маркеры читаются без цвета
// (ТЗ §9.4/§9.6), поэтому проверка по plain-тексту после снятия ANSI.
func TestViewTableSeverityMarkers(t *testing.T) {
	content := problemFstab + "UUID=ddd /home ext4 rw 0 2\n"
	m := testModel(t, testOptions(t, writeTestFstab(t, content)))
	view := stripANSI(m.viewTable())
	// Чистая третья запись: маркер "+" активной записи на месте.
	if !strings.Contains(view, " 0 2 +") {
		t.Fatalf("чистая запись должна показывать \"+\":\n%s", view)
	}
	// Маркеры проблемных строк: строка 1 (dump 0 pass 1) — "!", строка 2 —
	// "?" (dump 0 pass 0).
	if !strings.Contains(view, " 0 1 !") {
		t.Fatalf("error-строка должна показывать \"!\":\n%s", view)
	}
	if !strings.Contains(view, " 0 0 ?") {
		t.Fatalf("warning-строка должна показывать \"?\":\n%s", view)
	}
}

// TestViewTableStylizedCellsKeepValues: значения полей остаются в выводе
// целиком — lipgloss оборачивает ячейку ANSI-кодами снаружи и не разрывает
// значение, поэтому contains-проверки (в т.ч. старые тесты) не ломаются.
func TestViewTableStylizedCellsKeepValues(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, problemFstab)))
	m.cursor = 1 // курсорная строка оборачивается Selected поверх ячеек
	view := m.viewTable()
	for _, value := range []string{"UUID=aaa", "//srv/share", "/mnt/smb", "smbfs", "defaults"} {
		if !strings.Contains(view, value) {
			t.Fatalf("вывод таблицы должен содержать %q:\n%s", value, view)
		}
	}
}

// TestViewTableColumnAlignment: после снятия ANSI все строки таблицы имеют
// одну видимую ширину с заголовком — ячейки подогнаны под фиксированные
// ширины колонок и таблица не разъезжается от вложенных ANSI-кодов.
func TestViewTableColumnAlignment(t *testing.T) {
	content := problemFstab +
		"UUID=very-long-device-identifier-0123456789 /very/long/mount/point xfs " +
		"noatime,nodiratime,logbufs=8,allocsize=131072 0 0\n"
	m := testModel(t, testOptions(t, writeTestFstab(t, content)))
	plain := stripANSI(m.viewTable())
	lines := strings.Split(plain, "\n")
	// Заголовок — строка непосредственно перед первой строкой данных.
	dataStart := 0
	for i, line := range lines {
		if strings.Contains(line, "UUID=aaa") {
			dataStart = i
			break
		}
	}
	if dataStart == 0 {
		t.Fatalf("строки данных не найдены:\n%s", plain)
	}
	headerWidth := len([]rune(lines[dataStart-1]))
	rows := 0
	for _, line := range lines[dataStart:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Статусная строка под таблицей короче — она завершает проверку.
		if len([]rune(line)) != headerWidth {
			break
		}
		rows++
	}
	if rows != 3 {
		t.Fatalf("ожидалось 3 выровненные строки данных, найдено %d:\n%s", rows, plain)
	}
}

// TestFitCell проверяет подгонку ячеек: дополнение пробелами, усечение по
// рунам (в т.ч. многобайтовые символы — регрессия byte-wise усечения fmt).
func TestFitCell(t *testing.T) {
	if got := fitCell("abc", 6); got != "abc   " {
		t.Fatalf("padding: %q", got)
	}
	if got := fitCell("abcdefgh", 6); got != "abcdef" {
		t.Fatalf("truncation: %q", got)
	}
	// 4 двухбайтовых руны = 8 байт: по рунам умещаются все 4, по байтам fmt
	// урезал бы до 6 байт и ломал последний символ.
	if got := fitCell("тестовое", 4); got != "тест" {
		t.Fatalf("rune truncation: %q", got)
	}
	if got := fitCell("тест", 6); got != "тест  " {
		t.Fatalf("rune padding: %q", got)
	}
}

// TestHighlightPreview: полная raw-строка предпросмотра разбирается на 6
// полей и стилизуется Field-стилями (значения остаются читаемыми), неполная
// строка показывается как есть.
func TestHighlightPreview(t *testing.T) {
	m := testModel(t, testOptions(t, writeTestFstab(t, problemFstab)))
	preview := "UUID=aaa / ext4 defaults 0 1"
	out := m.highlightPreview(preview)
	for _, value := range []string{"UUID=aaa", "/", "ext4", "defaults", "0", "1"} {
		if !strings.Contains(out, value) {
			t.Fatalf("подсвеченный preview должен содержать %q: %q", value, out)
		}
	}
	// Пустой и неполный preview не паникуют и не теряют текст.
	if got := m.highlightPreview(""); stripANSI(got) != "" {
		t.Fatalf("пустой preview: %q", got)
	}
	if got := stripANSI(m.highlightPreview("UUID=aaa /")); got != "UUID=aaa /" {
		t.Fatalf("неполный preview должен вернуться как есть: %q", got)
	}
}
