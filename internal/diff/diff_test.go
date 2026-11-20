// Package diff реализует line-based unified diff для CLI preview.
package diff_test

import (
	"strings"
	"testing"

	"fstab-edit/internal/diff"
)

func TestUnifiedDiffAddAndContext(t *testing.T) {
	got := diff.Unified("a", "b", []byte("one\ntwo\n"), []byte("one\nTWO\nthree\n"))
	if !strings.Contains(got, "--- a") || !strings.Contains(got, "+++ b") {
		t.Fatalf("headers missing: %q", got)
	}
	if !strings.Contains(got, "-two") || !strings.Contains(got, "+TWO") || !strings.Contains(got, "+three") {
		t.Fatalf("hunk missing: %q", got)
	}
}

func TestUnifiedDiffNoFinalNewline(t *testing.T) {
	got := diff.Unified("a", "b", []byte("x"), []byte("y"))
	if !strings.Contains(got, `\ No newline at end of file`) {
		t.Fatalf("missing no-newline marker: %q", got)
	}
}

func TestUnifiedDiffUnchanged(t *testing.T) {
	if got := diff.Unified("a", "b", []byte("x\n"), []byte("x\n")); got != "" {
		t.Fatalf("unchanged diff: %q", got)
	}
}

func TestUnifiedDiffDeleteHunk(t *testing.T) {
	got := diff.Unified("a", "b", []byte("one\ntwo\nthree\n"), []byte("one\nthree\n"))
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++ ") {
			t.Fatalf("delete hunk must not contain added lines: %q", got)
		}
	}
	// Удалённая строка окружена тремя строками context с каждой стороны, где есть.
	if !strings.Contains(got, " one\n-two\n three\n") {
		t.Fatalf("context missing: %q", got)
	}
}

func TestUnifiedDiffHunkHeaderRanges(t *testing.T) {
	got := diff.Unified("a", "b", []byte("one\ntwo\n"), []byte("one\nTWO\nthree\n"))
	if !strings.Contains(got, "@@ -1,2 +1,3 @@") {
		t.Fatalf("hunk header ranges wrong: %q", got)
	}
}

func TestUnifiedDiffSeparateHunks(t *testing.T) {
	original := []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	candidate := []byte("1\nX\n3\n4\n5\n6\n7\n8\n9\nY\n")
	got := diff.Unified("a", "b", original, candidate)
	// Две далёкие правки не сливаются в один hunk.
	if strings.Count(got, "@@ ") != 2 {
		t.Fatalf("expected two hunks: %q", got)
	}
	if !strings.Contains(got, "-2\n+X") || !strings.Contains(got, "-10\n+Y") {
		t.Fatalf("hunks misplaced: %q", got)
	}
}

func TestUnifiedDiffDoesNotMutateInputs(t *testing.T) {
	original := []byte("x\ny")
	candidate := []byte("x\nz\n")
	originalCopy := append([]byte(nil), original...)
	candidateCopy := append([]byte(nil), candidate...)
	diff.Unified("a", "b", original, candidate)
	if string(original) != string(originalCopy) || string(candidate) != string(candidateCopy) {
		t.Fatalf("Unified mutated inputs: %q, %q", original, candidate)
	}
}

func TestUnifiedDiffEmptyToContent(t *testing.T) {
	got := diff.Unified("a", "b", nil, []byte("new\n"))
	if !strings.Contains(got, "+new") {
		t.Fatalf("empty original hunk missing: %q", got)
	}
	if diff.Unified("a", "b", nil, nil) != "" {
		t.Fatalf("two empty inputs must produce empty diff")
	}
}

func TestUnifiedDiffFinalNewlineOnly(t *testing.T) {
	got := diff.Unified("a", "b", []byte("x\n"), []byte("x"))
	if !strings.Contains(got, "-x") || !strings.Contains(got, "+x") {
		t.Fatalf("newline-only change must produce a hunk: %q", got)
	}
	if !strings.Contains(got, `\ No newline at end of file`) {
		t.Fatalf("missing no-newline marker: %q", got)
	}
}
