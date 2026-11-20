package cliapp_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"fstab-edit/internal/backup"
	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
)

// validTarget — минимальный корректный fstab: заголовок и одна запись.
const validTarget = `# /etc/fstab
/dev/sda1 / ext4 defaults 0 1
`

// spacedTarget сохраняет нестандартное форматирование полей и записи.
const spacedTarget = `# заголовок
/dev/sda1	/mnt/old data   defaults,noatime	0   1
`

func strPtr(value string) *string { return &value }

func TestPrepareAddProducesDiffAndDiagnostics(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind: cliapp.MutationAdd,
		NewEntry: editor.NewEntry{
			Source: "/dev/sdb1", MountPoint: "/mnt/data",
			FSType: "ext4", Options: "defaults,noatime",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Changed || preview.Operation != backup.OperationBeforeAdd {
		t.Fatalf("%+v", preview)
	}
	if preview.Diff == "" || !bytes.Contains(preview.Candidate, []byte("/mnt/data")) {
		t.Fatalf("preview incomplete")
	}
	for _, diagnostic := range preview.Report.Diagnostics {
		if diagnostic.Severity == "error" {
			t.Fatalf("valid candidate must have no error diagnostics: %+v", preview.Report.Diagnostics)
		}
	}
}

func TestPrepareEditPreservesUnrelatedFormatting(t *testing.T) {
	document := parser.Parse([]byte(spacedTarget))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationEdit,
		Selector: editor.ByLine(2),
		Changes:  editor.Changes{MountPoint: strPtr("/mnt/new")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Operation != backup.OperationBeforeEdit {
		t.Fatalf("Operation = %q, want before-edit", preview.Operation)
	}
	if !preview.Changed {
		t.Fatalf("edit preview must be changed: %+v", preview)
	}
	if !bytes.Contains(preview.Candidate, []byte("/mnt/new")) {
		t.Fatalf("candidate misses new mount point: %q", preview.Candidate)
	}
	// Нестандартные separators и остальные поля затронуты не должны быть.
	if !bytes.Contains(preview.Candidate, []byte("\t/mnt/new data   defaults,noatime\t0   1")) {
		t.Fatalf("unrelated formatting lost: %q", preview.Candidate)
	}
	if bytes.Contains(preview.Candidate, []byte("/mnt/old")) {
		t.Fatalf("old mount point still present: %q", preview.Candidate)
	}
	// Исходный документ не изменён: preview строит candidate на копии.
	if !bytes.Equal(document.Serialize(), []byte(spacedTarget)) {
		t.Fatalf("original document mutated: %q", document.Serialize())
	}
}

func TestPrepareDeleteRemovesEntry(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationDelete,
		Selector: editor.ByLine(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Changed || preview.Operation != backup.OperationBeforeDelete {
		t.Fatalf("%+v", preview)
	}
	if bytes.Contains(preview.Candidate, []byte("/dev/sda1")) {
		t.Fatalf("deleted entry still present: %q", preview.Candidate)
	}
	if !bytes.Contains(preview.Candidate, []byte("# /etc/fstab")) {
		t.Fatalf("unrelated header lost: %q", preview.Candidate)
	}
}

func TestPrepareCommentActiveEntry(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationComment,
		Selector: editor.ByLine(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Changed || preview.Operation != backup.OperationBeforeComment {
		t.Fatalf("%+v", preview)
	}
	if !bytes.Contains(preview.Candidate, []byte("# /dev/sda1")) {
		t.Fatalf("entry must be commented: %q", preview.Candidate)
	}
}

func TestPrepareUncommentCommentedEntry(t *testing.T) {
	const commented = `# /etc/fstab
# /dev/sda1 / ext4 defaults 0 1
`
	document := parser.Parse([]byte(commented))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationUncomment,
		Selector: editor.ByLine(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Changed || preview.Operation != backup.OperationBeforeUncomment {
		t.Fatalf("%+v", preview)
	}
	if !bytes.Contains(preview.Candidate, []byte("/dev/sda1 / ext4 defaults 0 1")) {
		t.Fatalf("entry must be uncommented: %q", preview.Candidate)
	}
	if strings.Contains(string(preview.Candidate), "# /dev/sda1") {
		t.Fatalf("comment prefix still present: %q", preview.Candidate)
	}
}

func TestPrepareWrongStateReturnsValidationExit(t *testing.T) {
	const commented = `# /etc/fstab
# /dev/sda1 / ext4 defaults 0 1
`
	document := parser.Parse([]byte(commented))
	// Повторное комментирование уже отключённой записи — ErrWrongState.
	_, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationComment,
		Selector: editor.ByLine(2),
	})
	if err == nil || err.Code != 1 {
		t.Fatalf("wrong state: got %v, want code 1", err)
	}
	if !errors.Is(err, editor.ErrWrongState) || !errors.Is(err, cliapp.ErrValidation) {
		t.Fatalf("wrong state exit must preserve causes: %v", err)
	}
}

func TestPrepareAmbiguousReturnsCode8WithCandidates(t *testing.T) {
	document := parser.Parse([]byte(duplicateMountDocument))
	_, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationDelete,
		Selector: editor.ByMountPoint("/mnt/data"),
	})
	if err == nil || err.Code != 8 {
		t.Fatalf("ambiguous mutation: got %v, want code 8", err)
	}
	if !errors.Is(err, editor.ErrAmbiguous) {
		t.Fatalf("ambiguity exit must preserve editor.ErrAmbiguous: %v", err)
	}
	if len(err.Candidates) != 2 || err.Candidates[0] != 2 || err.Candidates[1] != 3 {
		t.Fatalf("Candidates = %v, want [2 3]", err.Candidates)
	}
}

func TestPrepareNoOpEditReturnsUnchangedPreview(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationEdit,
		Selector: editor.ByLine(2),
		Changes:  editor.Changes{Dump: strPtr("0")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Changed {
		t.Fatalf("no-op edit must report Changed=false: %+v", preview)
	}
	if preview.Diff != "" {
		t.Fatalf("no-op edit must have empty diff: %q", preview.Diff)
	}
	if !bytes.Equal(preview.Candidate, preview.Original) {
		t.Fatalf("no-op candidate must equal original")
	}
}

func TestPrepareInvalidChangeReturnsCode1(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	// Одновременный set и unset Dump — ErrInvalidChange.
	_, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationEdit,
		Selector: editor.ByLine(2),
		Changes:  editor.Changes{Dump: strPtr("2"), UnsetDump: true},
	})
	if err == nil || err.Code != 1 {
		t.Fatalf("invalid change: got %v, want code 1", err)
	}
	if !errors.Is(err, editor.ErrInvalidChange) {
		t.Fatalf("invalid change exit must preserve cause: %v", err)
	}
}

func TestPrepareAddWithEmptyRequiredFieldReturnsCode1(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	_, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind: cliapp.MutationAdd,
		NewEntry: editor.NewEntry{
			Source: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "", Options: "defaults",
		},
	})
	if err == nil || err.Code != 1 {
		t.Fatalf("empty required field: got %v, want code 1", err)
	}
	if !errors.Is(err, editor.ErrEmptyField) {
		t.Fatalf("empty field exit must preserve cause: %v", err)
	}
}

func TestPrepareMutationCandidateIsIndependentCopy(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationDelete,
		Selector: editor.ByLine(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range preview.Candidate {
		preview.Candidate[index] = '!'
	}
	if !bytes.Equal(document.Serialize(), []byte(validTarget)) {
		t.Fatalf("mutating preview.Candidate changed the document")
	}
}

func TestPrepareMutationReportsValidationDiagnostics(t *testing.T) {
	// Дубликат активной точки монтирования даёт warning/error в report,
	// но preview всё равно строится — сохранение запретит transaction.
	document := parser.Parse([]byte(duplicateMountDocument))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind: cliapp.MutationAdd,
		NewEntry: editor.NewEntry{
			Source: "/dev/sdd1", MountPoint: "/mnt/extra", FSType: "ext4", Options: "defaults",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Changed {
		t.Fatalf("add preview must be changed: %+v", preview)
	}
	found := false
	for _, diagnostic := range preview.Report.Diagnostics {
		if diagnostic.Code == "duplicate.mount" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected duplicate.mount diagnostic: %+v", preview.Report.Diagnostics)
	}
}

func TestPrepareMutationNilDocumentReturnsCode1(t *testing.T) {
	_, err := cliapp.PrepareMutation(nil, cliapp.MutationRequest{Kind: cliapp.MutationAdd})
	if err == nil || err.Code != 1 {
		t.Fatalf("nil document: got %v, want code 1", err)
	}
}

func TestPrepareMutationDiffHeaders(t *testing.T) {
	document := parser.Parse([]byte(validTarget))
	preview, err := cliapp.PrepareMutation(document, cliapp.MutationRequest{
		Kind:     cliapp.MutationDelete,
		Selector: editor.ByLine(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Diff, "--- <original>") || !strings.Contains(preview.Diff, "+++ <candidate>") {
		t.Fatalf("diff headers missing: %q", preview.Diff)
	}
}
