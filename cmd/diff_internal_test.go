// Файл diff_internal_test.go — unit-тесты внутренних helper'ов cmd-пакета,
// недоступных внешнему тестовому пакету cmd_test.
package cmd

import "testing"

// TestDiffChangedLines проверяет извлечение изменённых строк из unified diff:
// '-'-строки (кроме заголовка '---') собираются в old, '+'-строки (кроме
// заголовка '+++') — в new, контекст и заголовки hunk игнорируются.
func TestDiffChangedLines(t *testing.T) {
	diffText := `--- <original>
+++ <candidate>
@@ -1,3 +1,4 @@
 # test fstab
 /dev/sda1 / ext4 defaults 0 1
-/dev/sdb1 /mnt/data ext4 defaults,noatime 0 2
+/dev/sdb1 /mnt/data ext4 defaults 0 2
+/dev/sdd1 /mnt/new ext4 defaults 0 0
`

	oldValue, newValue := diffChangedLines(diffText)
	wantOld := "/dev/sdb1 /mnt/data ext4 defaults,noatime 0 2"
	wantNew := "/dev/sdb1 /mnt/data ext4 defaults 0 2\n/dev/sdd1 /mnt/new ext4 defaults 0 0"
	if oldValue != wantOld {
		t.Fatalf("old = %q, want %q", oldValue, wantOld)
	}
	if newValue != wantNew {
		t.Fatalf("new = %q, want %q", newValue, wantNew)
	}

	// Пустой diff (no-op) даёт пустые значения без паники.
	oldValue, newValue = diffChangedLines("")
	if oldValue != "" || newValue != "" {
		t.Fatalf("empty diff gives old=%q new=%q, want empty", oldValue, newValue)
	}
}
