package cliapp_test

import (
	"errors"
	"testing"

	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
)

// duplicateMountDocument содержит две активные записи с одной точкой монтирования.
const duplicateMountDocument = `/dev/sda1 / ext4 defaults 0 1
/dev/sdb1 /mnt/data ext4 defaults 0 2
/dev/sdc1 /mnt/data xfs noatime 0 0
`

type recordingChooser struct {
	called     bool
	candidates []int
	pick       int
}

func (c *recordingChooser) Choose(candidates []int) (int, error) {
	c.called = true
	c.candidates = append([]int(nil), candidates...)
	return c.pick, nil
}

func TestSelectorFromValuesRequiresExactlyOneWay(t *testing.T) {
	if _, err := cliapp.SelectorFromValues("", "", "", "", true); err == nil || err.Code != 2 {
		t.Fatalf("no ways for required selector: got %v, want code 2", err)
	}
	if _, err := cliapp.SelectorFromValues("12", "3", "", "", true); err == nil || err.Code != 2 {
		t.Fatalf("two ways: got %v, want code 2", err)
	}
	_, lastErr := cliapp.SelectorFromValues("12", "", "/mnt", "/src", true)
	if lastErr == nil || lastErr.Code != 2 {
		t.Fatalf("three ways: got %v, want code 2", lastErr)
	}
	if !errors.Is(lastErr, cliapp.ErrUsage) {
		t.Fatalf("usage error must preserve ErrUsage sentinel: %v", lastErr)
	}
}

func TestSelectorFromValuesOptionalAllowsNone(t *testing.T) {
	selector, err := cliapp.SelectorFromValues("", "", "", "", false)
	if err != nil {
		t.Fatalf("optional selector without ways: %v", err)
	}
	if selector != (editor.Selector{}) {
		t.Fatalf("optional selector without ways must be zero value, got %+v", selector)
	}
}

func TestSelectorFromValuesPositionalPrefersLine(t *testing.T) {
	selector, err := cliapp.SelectorFromValues("12", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if selector != editor.ByLine(12) {
		t.Fatalf("positional %q: got %+v, want ByLine(12)", "12", selector)
	}
}

func TestSelectorFromValuesPositionalFallsBackToMountPoint(t *testing.T) {
	selector, err := cliapp.SelectorFromValues("12x", "", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if selector != editor.ByMountPoint("12x") {
		t.Fatalf("positional %q: got %+v, want ByMountPoint", "12x", selector)
	}
}

func TestSelectorFromValuesNamedWays(t *testing.T) {
	line, err := cliapp.SelectorFromValues("", "7", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if line != editor.ByLine(7) {
		t.Fatalf("--line: got %+v, want ByLine(7)", line)
	}
	mount, err := cliapp.SelectorFromValues("", "", "/mnt/data", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if mount != editor.ByMountPoint("/mnt/data") {
		t.Fatalf("--mount: got %+v", mount)
	}
	source, err := cliapp.SelectorFromValues("", "", "", "/dev/sdb1", true)
	if err != nil {
		t.Fatal(err)
	}
	if source != editor.BySource("/dev/sdb1") {
		t.Fatalf("--source: got %+v", source)
	}
	if _, err := cliapp.SelectorFromValues("", "7x", "", "", true); err == nil || err.Code != 2 {
		t.Fatalf("non-numeric --line: got %v, want code 2", err)
	}
}

func TestResolveUniqueReturnsIndex(t *testing.T) {
	document := parser.Parse([]byte(duplicateMountDocument))
	index, err := cliapp.Resolve(document, editor.ByMountPoint("/"), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if index != 0 {
		t.Fatalf("Resolve by mount /: got %d, want 0", index)
	}
	index, err = cliapp.Resolve(document, editor.ByLine(2), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if index != 1 {
		t.Fatalf("Resolve by line 2: got %d, want 1", index)
	}
}

func TestResolveAmbiguousWithoutInteractiveReturnsCandidates(t *testing.T) {
	document := parser.Parse([]byte(duplicateMountDocument))
	chooser := &recordingChooser{pick: 3}
	index, err := cliapp.Resolve(document, editor.ByMountPoint("/mnt/data"), chooser, false)
	if err == nil || err.Code != 8 {
		t.Fatalf("ambiguous without interactive: got %v, want code 8", err)
	}
	if index != -1 {
		t.Fatalf("ambiguous resolve must return -1, got %d", index)
	}
	if len(err.Candidates) != 2 || err.Candidates[0] != 2 || err.Candidates[1] != 3 {
		t.Fatalf("Candidates = %v, want [2 3]", err.Candidates)
	}
	if !errors.Is(err, editor.ErrAmbiguous) || !errors.Is(err, cliapp.ErrAmbiguous) {
		t.Fatalf("ambiguity exit must preserve causes: %v", err)
	}
	if chooser.called {
		t.Fatalf("chooser must not be called when interactive=false")
	}
}

func TestResolveAmbiguousWithNilChooserReturnsCode8(t *testing.T) {
	document := parser.Parse([]byte(duplicateMountDocument))
	_, err := cliapp.Resolve(document, editor.ByMountPoint("/mnt/data"), nil, true)
	if err == nil || err.Code != 8 {
		t.Fatalf("ambiguous with nil chooser: got %v, want code 8", err)
	}
}

func TestResolveInteractiveChoosesCandidate(t *testing.T) {
	document := parser.Parse([]byte(duplicateMountDocument))
	chooser := &recordingChooser{pick: 3}
	index, err := cliapp.Resolve(document, editor.ByMountPoint("/mnt/data"), chooser, true)
	if err != nil {
		t.Fatal(err)
	}
	if index != 2 {
		t.Fatalf("interactive resolve picked line 3: got index %d, want 2", index)
	}
	if !chooser.called || len(chooser.candidates) != 2 {
		t.Fatalf("chooser must receive both candidates: %+v", chooser)
	}
}

func TestResolveInteractiveForeignChoiceReturnsCode8(t *testing.T) {
	document := parser.Parse([]byte(duplicateMountDocument))
	chooser := &recordingChooser{pick: 99}
	_, err := cliapp.Resolve(document, editor.ByMountPoint("/mnt/data"), chooser, true)
	if err == nil || err.Code != 8 {
		t.Fatalf("foreign choice: got %v, want code 8", err)
	}
}

func TestResolveNotFoundMapsToValidation(t *testing.T) {
	document := parser.Parse([]byte(duplicateMountDocument))
	_, err := cliapp.Resolve(document, editor.ByMountPoint("/missing"), nil, false)
	if err == nil || err.Code != 1 {
		t.Fatalf("not found: got %v, want code 1", err)
	}
	if !errors.Is(err, editor.ErrNotFound) {
		t.Fatalf("not found exit must preserve ErrNotFound: %v", err)
	}
}
