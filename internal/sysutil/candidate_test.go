package sysutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// candidateInspectingRunner проверяет candidate непосредственно в момент вызова
// findmnt. Никакой внешний процесс не запускается: это единственная точка,
// через которую VerifyCandidate взаимодействует с системной утилитой.
type candidateInspectingRunner struct {
	t         *testing.T
	tempRoot  string
	candidate []byte
	result    RunResult
	calls     int
	ctx       context.Context
	name      string
	args      []string
	path      string
}

func (r *candidateInspectingRunner) Run(ctx context.Context, name string, args ...string) RunResult {
	r.calls++
	r.ctx = ctx
	r.name = name
	r.args = append([]string(nil), args...)

	if name != "findmnt" {
		r.t.Errorf("внешняя утилита: got %q, want findmnt", name)
	}
	if len(args) != 3 {
		r.t.Errorf("argv: got %#v, want --verify --tab-file <candidate>", args)
		return r.result
	}
	if args[0] != "--verify" || args[1] != "--tab-file" {
		r.t.Errorf("argv flags: got %#v, want --verify --tab-file", args[:2])
	}

	r.path = args[2]
	if !filepath.IsAbs(r.path) {
		r.t.Errorf("candidate path не абсолютный: %q", r.path)
	}
	if r.path == "/etc/fstab" || strings.Contains(r.path, "/etc/fstab") {
		r.t.Errorf("runner получил путь реального fstab: %q", r.path)
	}
	parent := filepath.Dir(r.path)
	if filepath.Dir(parent) != r.tempRoot {
		r.t.Errorf("candidate parent %q находится вне test temp root %q", parent, r.tempRoot)
	}
	if !strings.HasPrefix(filepath.Base(parent), "fstab-edit-candidate-") {
		r.t.Errorf("неожиданное имя private directory: %q", parent)
	}
	if filepath.Base(r.path) != "candidate.fstab" {
		r.t.Errorf("неожиданное имя candidate: %q", r.path)
	}

	data, err := os.ReadFile(r.path)
	if err != nil {
		r.t.Errorf("чтение candidate: %v", err)
	} else if !bytes.Equal(data, r.candidate) {
		r.t.Errorf("candidate bytes изменены:\ngot  %q\nwant %q", data, r.candidate)
	}
	info, err := os.Stat(r.path)
	if err != nil {
		r.t.Errorf("stat candidate: %v", err)
	} else {
		if !info.Mode().IsRegular() {
			r.t.Errorf("candidate не regular file: %v", info.Mode())
		}
		if got := info.Mode().Perm(); got != 0o600 {
			r.t.Errorf("candidate mode: got %04o, want 0600", got)
		}
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		r.t.Errorf("stat parent: %v", err)
	} else {
		if !parentInfo.IsDir() {
			r.t.Errorf("candidate parent не directory: %v", parentInfo.Mode())
		}
		if got := parentInfo.Mode().Perm(); got != 0o700 {
			r.t.Errorf("parent mode: got %04o, want 0700", got)
		}
	}
	return r.result
}

// isolateCandidateTemp направляет MkdirTemp в принадлежащий тесту каталог.
// Это позволяет проверить фактическое использование system temp API и после
// теста гарантированно обнаружить оставшиеся private candidate directories.
func isolateCandidateTemp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	return root
}

func newCandidateInspectingRunner(t *testing.T, tempRoot string, candidate []byte, result RunResult) *candidateInspectingRunner {
	t.Helper()
	return &candidateInspectingRunner{t: t, tempRoot: tempRoot, candidate: candidate, result: result}
}

// assertCandidateCleanup требует удаления и файла, и всего private directory.
func assertCandidateCleanup(t *testing.T, runner *candidateInspectingRunner) {
	t.Helper()
	if runner.calls != 1 {
		t.Fatalf("вызовов runner: got %d, want 1", runner.calls)
	}
	if runner.path == "" {
		t.Fatal("runner не зафиксировал candidate path")
	}
	if _, err := os.Stat(runner.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("candidate сохранился после VerifyCandidate: stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(runner.path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private directory сохранился после VerifyCandidate: stat err=%v", err)
	}
}

// assertCandidateReportRedacted сериализует именно публичный Report и исключает
// утечку временного пути или обращения к реальному системному fstab.
func assertCandidateReportRedacted(t *testing.T, verification Verification) {
	t.Helper()
	data, err := json.Marshal(verification.Report)
	if err != nil {
		t.Fatalf("Report не сериализуется: %v", err)
	}
	encoded := string(data)
	for _, forbidden := range []string{"/etc/fstab", "fstab-edit-candidate-", "candidate.fstab"} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("Report раскрывает %q: %s", forbidden, encoded)
		}
	}
}

// TestVerifyCandidateLifecycle проверяет полный успешный жизненный цикл на
// invalid UTF-8 без final newline: байты и исходный slice неизменны, файл и
// directory приватны, runner видит только candidate argv, всё удалено после return.
func TestVerifyCandidateLifecycle(t *testing.T) {
	tempRoot := isolateCandidateTemp(t)
	candidate := []byte{0xff, 0xfe, ' ', '/', ' ', 'e', 'x', 't', '4', ' ', 'd', 'e', 'f', 'a', 'u', 'l', 't', 's'}
	original := append([]byte(nil), candidate...)
	execution := RunResult{Path: "/usr/bin/findmnt", ExitCode: 0}
	runner := newCandidateInspectingRunner(t, tempRoot, candidate, execution)
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "candidate context")

	verification := VerifyCandidate(ctx, runner, candidate)

	if !bytes.Equal(candidate, original) {
		t.Fatalf("исходный candidate slice изменён:\ngot  %q\nwant %q", candidate, original)
	}
	if runner.ctx != ctx {
		t.Error("VerifyCandidate не сохранил исходный context")
	}
	if runner.name != "findmnt" || len(runner.args) != 3 {
		t.Errorf("неожиданный вызов runner: %q %#v", runner.name, runner.args)
	}
	if !reflect.DeepEqual(verification.Execution, execution) {
		t.Fatalf("Execution изменён:\ngot  %+v\nwant %+v", verification.Execution, execution)
	}
	assertCandidateCleanup(t, runner)
	assertCandidateReportRedacted(t, verification)
}

// TestVerifyCandidateRelativeTempDirUsesAbsolutePath проверяет absolute path
// contract при относительном TMPDIR, неизменные bytes и cleanup через fake runner.
func TestVerifyCandidateRelativeTempDirUsesAbsolutePath(t *testing.T) {
	tempRoot := isolateCandidateTemp(t)
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeRoot, err := filepath.Rel(workingDirectory, tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(relativeRoot) {
		t.Fatalf("test TMPDIR должен быть относительным: %q", relativeRoot)
	}
	t.Setenv("TMPDIR", relativeRoot)
	candidate := []byte("/dev/sda /mnt/data ext4 defaults 0 0")
	execution := RunResult{Path: "/usr/bin/findmnt", ExitCode: 0}
	runner := newCandidateInspectingRunner(t, tempRoot, candidate, execution)

	verification := VerifyCandidate(context.Background(), runner, candidate)

	if runner.path == "" || !filepath.IsAbs(runner.path) {
		t.Errorf("args[2] при относительном TMPDIR: got %q, want absolute path", runner.path)
	}
	if !reflect.DeepEqual(verification.Execution, execution) {
		t.Fatalf("Execution изменён:\ngot  %+v\nwant %+v", verification.Execution, execution)
	}
	assertFindmntCodes(t, verification.Report)
	assertCandidateCleanup(t, runner)
	assertCandidateReportRedacted(t, verification)
}

// TestVerifyCandidateEmpty проверяет, что пустой вход не заменяется newline или
// другим содержимым и всё равно проходит внешнюю проверку с последующим cleanup.
func TestVerifyCandidateEmpty(t *testing.T) {
	tempRoot := isolateCandidateTemp(t)
	candidate := []byte{}
	runner := newCandidateInspectingRunner(t, tempRoot, candidate, RunResult{ExitCode: 0})

	verification := VerifyCandidate(context.Background(), runner, candidate)

	if len(candidate) != 0 {
		t.Fatalf("пустой candidate изменён: %q", candidate)
	}
	if verification.Report.Diagnostics == nil || len(verification.Report.Diagnostics) != 0 {
		t.Fatalf("пустой candidate дал неожиданный Report: %+v", verification.Report)
	}
	if verification.Execution.ExitCode != 0 || verification.Execution.Err != nil {
		t.Fatalf("неожиданный Execution: %+v", verification.Execution)
	}
	assertCandidateCleanup(t, runner)
}

// TestVerifyCandidateMergesInternalAndExternalReports проверяет обязательный
// порядок internal error → external warning и блокировку сохранения.
func TestVerifyCandidateMergesInternalAndExternalReports(t *testing.T) {
	tempRoot := isolateCandidateTemp(t)
	candidate := []byte("broken\n")
	execution := RunResult{ExitCode: 0, Stdout: "/synthetic\n   [W] sensitive warning\n"}
	runner := newCandidateInspectingRunner(t, tempRoot, candidate, execution)

	verification := VerifyCandidate(context.Background(), runner, candidate)

	assertFindmntCodes(t, verification.Report, "parse.invalid/error", "findmnt.warning/warning")
	if !verification.Report.HasErrors() {
		t.Fatal("internal error потерян при MergeReports")
	}
	if !reflect.DeepEqual(verification.Execution, execution) {
		t.Fatalf("external Execution не сохранён: %+v", verification.Execution)
	}
	assertCandidateCleanup(t, runner)
	assertCandidateReportRedacted(t, verification)
}

// TestVerifyCandidateCleanupAfterRunnerFailure проверяет удаление candidate при
// обычной ошибке запуска и timeout, а также сохранение первичного Execution.Err.
func TestVerifyCandidateCleanupAfterRunnerFailure(t *testing.T) {
	tests := []struct {
		name      string
		execution RunResult
		wantCode  string
	}{
		{
			name:      "runner error",
			execution: RunResult{ExitCode: -1, Err: errors.New("sensitive runner failure")},
			wantCode:  "findmnt.unavailable/error",
		},
		{
			name: "timeout",
			execution: RunResult{ExitCode: -1, TimedOut: true,
				Err: context.DeadlineExceeded},
			wantCode: "findmnt.timeout/error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempRoot := isolateCandidateTemp(t)
			candidate := []byte("/dev/sda /mnt/data ext4 defaults 0 0")
			runner := newCandidateInspectingRunner(t, tempRoot, candidate, tt.execution)

			verification := VerifyCandidate(context.Background(), runner, candidate)

			assertFindmntCodes(t, verification.Report, tt.wantCode)
			if !reflect.DeepEqual(verification.Execution, tt.execution) {
				t.Fatalf("первичный Execution изменён:\ngot  %+v\nwant %+v", verification.Execution, tt.execution)
			}
			assertCandidateCleanup(t, runner)
			assertCandidateReportRedacted(t, verification)
		})
	}
}

// TestVerifyCandidateCreateFailure проверяет локальную ошибку MkdirTemp: runner
// и WriteFile не вызываются, техническая причина сохраняется только в Execution.
func TestVerifyCandidateCreateFailure(t *testing.T) {
	isolateCandidateTemp(t)
	originalMkdirTemp := candidateMkdirTemp
	originalWriteFile := candidateWriteFile
	originalRemoveAll := candidateRemoveAll
	t.Cleanup(func() {
		candidateMkdirTemp = originalMkdirTemp
		candidateWriteFile = originalWriteFile
		candidateRemoveAll = originalRemoveAll
	})
	createErr := errors.New("private create failure")
	candidateMkdirTemp = func(directory, pattern string) (string, error) {
		if directory != "" || pattern != "fstab-edit-candidate-*" {
			t.Errorf("MkdirTemp args: got %q, %q", directory, pattern)
		}
		return "", createErr
	}
	candidateWriteFile = func(path string, data []byte, perm os.FileMode) error {
		t.Errorf("WriteFile вызван после ошибки MkdirTemp: %q", path)
		return nil
	}
	candidateRemoveAll = func(path string) error {
		t.Errorf("RemoveAll вызван без созданного directory: %q", path)
		return nil
	}
	runner := &fakeFindmntRunner{}
	candidate := []byte("do-not-leak-candidate")

	verification := VerifyCandidate(context.Background(), runner, candidate)

	assertFindmntCodes(t, verification.Report, "findmnt.candidate/error")
	if runner.calls != 0 {
		t.Fatalf("runner вызван %d раз после ошибки MkdirTemp", runner.calls)
	}
	if verification.Execution.ExitCode != -1 || !errors.Is(verification.Execution.Err, createErr) {
		t.Fatalf("неожиданный Execution: %+v", verification.Execution)
	}
	assertCandidateReportRedacted(t, verification)
	if strings.Contains(mustFindmntReportJSON(t, verification.Report), string(candidate)) {
		t.Fatal("содержимое candidate попало в Report")
	}
}

// TestVerifyCandidateWriteFailure проверяет ошибку записи, отсутствие вызова
// findmnt и обязательную попытку удалить уже созданный private directory.
func TestVerifyCandidateWriteFailure(t *testing.T) {
	isolateCandidateTemp(t)
	originalMkdirTemp := candidateMkdirTemp
	originalWriteFile := candidateWriteFile
	originalRemoveAll := candidateRemoveAll
	t.Cleanup(func() {
		candidateMkdirTemp = originalMkdirTemp
		candidateWriteFile = originalWriteFile
		candidateRemoveAll = originalRemoveAll
	})
	candidateMkdirTemp = os.MkdirTemp
	writeErr := errors.New("private write failure")
	var candidatePath string
	candidate := []byte("do-not-leak-candidate")
	candidateWriteFile = func(path string, data []byte, perm os.FileMode) error {
		candidatePath = path
		if !bytes.Equal(data, candidate) || perm != 0o600 {
			t.Errorf("WriteFile получил data=%q perm=%04o", data, perm)
		}
		return writeErr
	}
	removedPath := ""
	candidateRemoveAll = func(path string) error {
		removedPath = path
		return os.RemoveAll(path)
	}
	runner := &fakeFindmntRunner{}

	verification := VerifyCandidate(context.Background(), runner, candidate)

	assertFindmntCodes(t, verification.Report, "findmnt.candidate/error")
	if runner.calls != 0 {
		t.Fatalf("runner вызван %d раз после ошибки WriteFile", runner.calls)
	}
	if verification.Execution.ExitCode != -1 || !errors.Is(verification.Execution.Err, writeErr) {
		t.Fatalf("неожиданный Execution: %+v", verification.Execution)
	}
	if candidatePath == "" || removedPath != filepath.Dir(candidatePath) {
		t.Fatalf("cleanup: path=%q removed=%q", candidatePath, removedPath)
	}
	if _, err := os.Stat(removedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private directory не удалён после ошибки записи: %v", err)
	}
	assertCandidateReportRedacted(t, verification)
}

func TestVerifyCandidateAbsFailureCleansCreatedDirectory(t *testing.T) {
	tempRoot := isolateCandidateTemp(t)
	originalMkdirTemp := candidateMkdirTemp
	originalWriteFile := candidateWriteFile
	originalRemoveAll := candidateRemoveAll
	originalAbs := candidateAbs
	t.Cleanup(func() {
		candidateMkdirTemp = originalMkdirTemp
		candidateWriteFile = originalWriteFile
		candidateRemoveAll = originalRemoveAll
		candidateAbs = originalAbs
	})
	candidateMkdirTemp = os.MkdirTemp
	createdDirectory := ""
	absErr := errors.New("private absolute path failure")
	candidateAbs = func(path string) (string, error) {
		createdDirectory = path
		return "", absErr
	}
	writeCalls := 0
	candidateWriteFile = func(path string, data []byte, perm os.FileMode) error {
		writeCalls++
		return nil
	}
	removedPaths := []string{}
	candidateRemoveAll = func(path string) error {
		removedPaths = append(removedPaths, path)
		return os.RemoveAll(path)
	}
	runner := &fakeFindmntRunner{}

	verification := VerifyCandidate(context.Background(), runner, []byte("do-not-leak-candidate"))

	assertFindmntCodes(t, verification.Report, "findmnt.candidate/error")
	if createdDirectory == "" || filepath.Dir(createdDirectory) != tempRoot {
		t.Fatalf("неожиданный созданный directory: %q, temp root: %q", createdDirectory, tempRoot)
	}
	if !reflect.DeepEqual(removedPaths, []string{createdDirectory}) {
		t.Errorf("RemoveAll paths: got %#v, want %#v", removedPaths, []string{createdDirectory})
	}
	if _, err := os.Stat(createdDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private directory не удалён после ошибки Abs: stat err=%v", err)
	}
	if writeCalls != 0 {
		t.Errorf("WriteFile вызван %d раз после ошибки Abs", writeCalls)
	}
	if runner.calls != 0 {
		t.Errorf("runner вызван %d раз после ошибки Abs", runner.calls)
	}
	if verification.Execution.ExitCode != -1 || !errors.Is(verification.Execution.Err, absErr) {
		t.Errorf("исходная ошибка Abs не сохранена: %+v", verification.Execution)
	}
	assertCandidateReportRedacted(t, verification)
}

// TestVerifyCandidateCleanupFailurePreservesPrimaryResult проверяет warning при
// ошибке RemoveAll и неизменность первичной findmnt-ошибки и Execution.Err.
func TestVerifyCandidateCleanupFailurePreservesPrimaryResult(t *testing.T) {
	tempRoot := isolateCandidateTemp(t)
	originalRemoveAll := candidateRemoveAll
	t.Cleanup(func() { candidateRemoveAll = originalRemoveAll })
	cleanupErr := errors.New("private cleanup failure")
	removedPath := ""
	candidateRemoveAll = func(path string) error {
		removedPath = path
		return cleanupErr
	}
	candidate := []byte("/dev/sda /mnt/data ext4 defaults 0 0")
	original := append([]byte(nil), candidate...)
	primaryErr := errors.New("sensitive primary failure")
	execution := RunResult{ExitCode: -1, Err: primaryErr}
	runner := newCandidateInspectingRunner(t, tempRoot, candidate, execution)

	verification := VerifyCandidate(context.Background(), runner, candidate)

	assertFindmntCodes(t, verification.Report,
		"findmnt.unavailable/error", "findmnt.candidate_cleanup/warning")
	if !errors.Is(verification.Execution.Err, primaryErr) || verification.Execution.ExitCode != -1 {
		t.Fatalf("первичный Execution перезаписан cleanup error: %+v", verification.Execution)
	}
	if removedPath != filepath.Dir(runner.path) {
		t.Fatalf("RemoveAll path: got %q, want %q", removedPath, filepath.Dir(runner.path))
	}
	if !bytes.Equal(candidate, original) {
		t.Fatalf("candidate изменён: got %q, want %q", candidate, original)
	}
	assertCandidateReportRedacted(t, verification)
}
