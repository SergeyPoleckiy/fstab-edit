// Файл e2e_test.go проверяет сквозные сценарии CLI: мутации с подтверждениями,
// селекторы и неоднозначность, dry-run, backup/rollback, usage errors,
// чистоту machine stdout и маппинг exit codes. Все сценарии используют только
// временные файлы, memory buffers, fake runner и scripted stdin: реальный
// /etc/fstab, findmnt и TTY не затрагиваются.
package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fstab-edit/cmd"
	"fstab-edit/internal/backup"
	"fstab-edit/internal/cliapp"
	"fstab-edit/internal/sysutil"
	"fstab-edit/internal/transaction"
	"fstab-edit/internal/writer"
	"fstab-edit/pkg/models"
)

// e2eFixture — типичный target: заголовок, активная корневая запись,
// активная запись с associated comment и закомментированная запись.
const e2eFixture = `# test fstab
/dev/sda1 / ext4 defaults 0 1
# данные диска
/dev/sdb1 /mnt/data ext4 defaults,noatime 0 2
# отключённая запись
# /dev/sdc1 /mnt/old ext4 defaults 0 2
`

// ambiguousFixture содержит две записи с одним mount point: активную и
// закомментированную. Любой нестрогий селектор по mount point неоднозначен.
const ambiguousFixture = `/dev/sda1 / ext4 defaults 0 1
/dev/sdb1 /mnt/dup ext4 defaults 0 2
# /dev/sdc1 /mnt/dup ext4 defaults 0 2
`

// invalidFixture содержит строку, которую parser не может разобрать как
// запись fstab: внутренняя проверка обязана вернуть severity=error.
const invalidFixture = `/dev/sda1 / ext4 defaults 0 1
это не запись fstab
`

// mutationDoc — минимальный разбор machine-документа результата мутации.
type mutationDoc struct {
	SchemaVersion int               `json:"schema_version"`
	Operation     string            `json:"operation"`
	Changed       bool              `json:"changed"`
	DryRun        bool              `json:"dry_run"`
	Target        string            `json:"target"`
	Diff          string            `json:"diff"`
	Diagnostics   []json.RawMessage `json:"diagnostics"`
	BackupID      string            `json:"backup_id"`
	Rollback      *rollbackDoc      `json:"rollback"`
}

// rollbackDoc — documented struct результата automatic rollback.
type rollbackDoc struct {
	RolledBack bool   `json:"rolled_back"`
	BackupID   string `json:"backup_id"`
	Error      string `json:"error"`
}

// decodeSingleMutationDoc декодирует ровно один JSON-документ из stdout и
// проверяет, что второго value в потоке нет: machine stdout обязан содержать
// ровно один structured document.
func decodeSingleMutationDoc(t *testing.T, stdout *bytes.Buffer) mutationDoc {
	t.Helper()
	var document mutationDoc
	decoder := json.NewDecoder(stdout)
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode mutation JSON: %v; stdout: %q", err, stdout.String())
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout contains more than one JSON document: %v", err)
	}
	return document
}

// readTargetBytes читает байты target для проверки его состояния.
func readTargetBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	return data
}

// listBackupData возвращает список data-файлов резервных копий в каталоге.
// Data-файлы именуются <prefix>.<id>, их sidecar <data>.json в список не входит.
func listBackupData(t *testing.T, directory string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, backup.DefaultBackupPrefix+".*"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	files := make([]string, 0, len(matches))
	for _, match := range matches {
		if !strings.HasSuffix(match, ".json") {
			files = append(files, match)
		}
	}
	return files
}

// TestAddDryRunLeavesFileUnchanged проверяет, что add --dry-run показывает
// structured preview (changed=true, dry_run=true), не требует --yes и
// оставляет байты target неизменными.
func TestAddDryRunLeavesFileUnchanged(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})

	code := app.Execute([]string{
		"add", "--file", target,
		"--device", "/dev/sdd1", "--mount", "/mnt/new", "--type", "ext4",
		"--options", "defaults", "--dry-run", "-o", "json",
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	document := decodeSingleMutationDoc(t, &buffers.stdout)
	if !document.Changed || !document.DryRun {
		t.Fatalf("document = %+v, want changed=true dry_run=true", document)
	}
	if document.Operation != string(backup.OperationBeforeAdd) {
		t.Fatalf("operation = %q", document.Operation)
	}
	if !strings.Contains(document.Diff, "/mnt/new") {
		t.Fatalf("diff missing new entry: %q", document.Diff)
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("dry-run changed target bytes")
	}
}

// TestAddNonTTYRequiresYesAndCreatesBackup проверяет границу подтверждения:
// non-TTY без --yes отклоняется кодом 2 и ничего не меняет, а с --yes
// транзакция применяется, target изменяется и backup создаётся.
func TestAddNonTTYRequiresYesAndCreatesBackup(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	args := []string{
		"add", "--file", target,
		"--device", "/dev/sdd1", "--mount", "/mnt/new", "--type", "ext4",
		"--options", "defaults", "-o", "json",
	}

	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute(args); code != 2 {
		t.Fatalf("code = %d, want 2; stderr: %s", code, buffers.stderr.String())
	}
	if !strings.Contains(buffers.stderr.String(), "--yes") {
		t.Fatalf("stderr missing --yes hint: %q", buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("target changed without confirmation")
	}

	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute(append(args, "--yes")); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	document := decodeSingleMutationDoc(t, &buffers.stdout)
	if document.DryRun || !document.Changed || document.BackupID == "" {
		t.Fatalf("document = %+v, want executed mutation with backup", document)
	}
	updated := readTargetBytes(t, target)
	if !bytes.Contains(updated, []byte("/mnt/new")) {
		t.Fatalf("target missing new entry: %q", updated)
	}
	if backups := listBackupData(t, filepath.Dir(target)); len(backups) != 1 {
		t.Fatalf("backup files = %v, want exactly 1", backups)
	}
}

// TestAddTTYConfirmationDeclineAndAccept проверяет интерактивное подтверждение:
// ответ по умолчанию No: «n» и EOF оставляют target неизменным с кодом 0,
// а «y» применяет изменение.
func TestAddTTYConfirmationDeclineAndAccept(t *testing.T) {
	buildArgs := func(target string) []string {
		return []string{
			"add", "--file", target,
			"--device", "/dev/sdd1", "--mount", "/mnt/new", "--type", "ext4",
			"--options", "defaults",
		}
	}

	declined := writeTempTarget(t, e2eFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader("n\n"), true, &fakeRunner{})
	if code := app.Execute(buildArgs(declined)); code != 0 {
		t.Fatalf("decline code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, declined), []byte(e2eFixture)) {
		t.Fatal("declined mutation changed target")
	}
	if !strings.Contains(buffers.stderr.String(), "[y/N]") {
		t.Fatalf("prompt [y/N] missing: %q", buffers.stderr.String())
	}

	eofTarget := writeTempTarget(t, e2eFixture)
	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), true, &fakeRunner{})
	if code := app.Execute(buildArgs(eofTarget)); code != 0 {
		t.Fatalf("EOF code = %d, want 0", code)
	}
	if !bytes.Equal(readTargetBytes(t, eofTarget), []byte(e2eFixture)) {
		t.Fatal("EOF-answer mutation changed target")
	}

	accepted := writeTempTarget(t, e2eFixture)
	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader("y\n"), true, &fakeRunner{})
	if code := app.Execute(buildArgs(accepted)); code != 0 {
		t.Fatalf("accept code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Contains(readTargetBytes(t, accepted), []byte("/mnt/new")) {
		t.Fatal("accepted mutation did not change target")
	}
}

// TestEditMutatesOnlySelectedFields проверяет, что edit по селектору mount
// point меняет только явно заданные поля и сохраняет остальное форматирование.
func TestEditMutatesOnlySelectedFields(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})

	code := app.Execute([]string{
		"edit", "--file", target, "--mount-point", "/mnt/data",
		"--options", "defaults,ro", "--yes",
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	updated := string(readTargetBytes(t, target))
	if !strings.Contains(updated, "/dev/sdb1 /mnt/data ext4 defaults,ro 0 2") {
		t.Fatalf("edited entry missing: %q", updated)
	}
	if !strings.Contains(updated, "/dev/sda1 / ext4 defaults 0 1") {
		t.Fatalf("unrelated entry reformatted: %q", updated)
	}
}

// TestEditNoChangingFlagRejected проверяет, что edit без единого изменяющего
// флага отклоняется usage error до любого обращения к target.
func TestEditNoChangingFlagRejected(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})

	code := app.Execute([]string{"edit", "--file", target, "--mount-point", "/mnt/data", "--yes"})
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr: %s", code, buffers.stderr.String())
	}
	if !strings.Contains(buffers.stderr.String(), "изменяющий") {
		t.Fatalf("stderr missing explanation: %q", buffers.stderr.String())
	}
}

// TestDeleteCommentUncommentSelectors проверяет селекторы трёх команд:
// delete по строке, comment по позиционному id и uncomment по строке.
func TestDeleteCommentUncommentSelectors(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})

	if code := app.Execute([]string{"delete", "--file", target, "--line", "6", "--yes"}); code != 0 {
		t.Fatalf("delete code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if bytes.Contains(readTargetBytes(t, target), []byte("/mnt/old")) {
		t.Fatal("delete left commented entry in target")
	}

	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute([]string{"comment", "--file", target, "/mnt/data", "--yes"}); code != 0 {
		t.Fatalf("comment code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Contains(readTargetBytes(t, target), []byte("# /dev/sdb1 /mnt/data ext4 defaults,noatime 0 2")) {
		t.Fatalf("comment did not disable entry: %q", readTargetBytes(t, target))
	}

	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute([]string{"uncomment", "--file", target, "--line", "4", "--yes"}); code != 0 {
		t.Fatalf("uncomment code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Contains(readTargetBytes(t, target), []byte("/dev/sdb1 /mnt/data ext4 defaults,noatime 0 2")) {
		t.Fatalf("uncomment did not enable entry: %q", readTargetBytes(t, target))
	}
}

// TestAmbiguousSelectorReturnsCode8 проверяет, что неоднозначный селектор
// завершается кодом 8, stderr перечисляет candidate lines и --yes не выбирает
// произвольный кандидат.
func TestAmbiguousSelectorReturnsCode8(t *testing.T) {
	buildArgs := func(target string, extra ...string) []string {
		return append([]string{"edit", "--file", target, "--mount-point", "/mnt/dup", "--options", "defaults"}, extra...)
	}

	target := writeTempTarget(t, ambiguousFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute(buildArgs(target)); code != 8 {
		t.Fatalf("code = %d, want 8; stderr: %s", code, buffers.stderr.String())
	}
	for _, fragment := range []string{"2", "3"} {
		if !strings.Contains(buffers.stderr.String(), fragment) {
			t.Fatalf("stderr missing candidate line %s: %q", fragment, buffers.stderr.String())
		}
	}

	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute(buildArgs(target, "--yes")); code != 8 {
		t.Fatalf("code with --yes = %d, want 8; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(ambiguousFixture)) {
		t.Fatal("--yes resolved ambiguity by changing target")
	}
}

// TestAmbiguousSelectorInteractiveChoice проверяет, что при TTY и без --yes
// интерактивный chooser может выбрать одну из candidate lines.
func TestAmbiguousSelectorInteractiveChoice(t *testing.T) {
	target := writeTempTarget(t, ambiguousFixture)
	buffers := &appBuffers{}
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:  &buffers.stdout,
		Stderr:  &buffers.stderr,
		Stdin:   strings.NewReader(""),
		TTY:     func() bool { return true },
		Runner:  &fakeRunner{},
		Chooser: fixedChooser{line: 2},
	})

	code := app.Execute([]string{
		"edit", "--file", target, "--mount-point", "/mnt/dup", "--options", "defaults", "--yes",
	})
	if code != 8 {
		t.Fatalf("code = %d, want 8 (--yes запрещает интерактивный выбор)", code)
	}

	// Без --yes при TTY chooser получает список кандидатов и выбирает строку 2.
	buffers = &appBuffers{}
	app = cmd.NewApp(cmd.AppOptions{
		Stdout:  &buffers.stdout,
		Stderr:  &buffers.stderr,
		Stdin:   strings.NewReader("y\n"),
		TTY:     func() bool { return true },
		Runner:  &fakeRunner{},
		Chooser: fixedChooser{line: 2},
	})
	code = app.Execute([]string{
		"edit", "--file", target, "--mount-point", "/mnt/dup", "--options", "defaults",
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Contains(readTargetBytes(t, target), []byte("/dev/sdb1 /mnt/dup ext4 defaults 0 2")) {
		t.Fatalf("chosen line not edited: %q", readTargetBytes(t, target))
	}
}

// fixedChooser всегда выбирает заданную строку из candidates.
type fixedChooser struct {
	line int
}

func (f fixedChooser) Choose(candidates []int) (int, error) { return f.line, nil }

// TestValidateOfflineWarningsAndErrors проверяет, что validate --offline
// не вызывает runner, warnings допускают код 0, а severity=error даёт код 1.
func TestValidateOfflineWarningsAndErrors(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	runner := &fakeRunner{}
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, runner)

	if code := app.Execute([]string{"validate", "--file", target, "--offline", "-o", "json"}); code != 0 {
		t.Fatalf("warnings code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	var document struct {
		SchemaVersion int               `json:"schema_version"`
		Diagnostics   []json.RawMessage `json:"diagnostics"`
	}
	if err := json.NewDecoder(&buffers.stdout).Decode(&document); err != nil {
		t.Fatalf("decode validate JSON: %v", err)
	}
	if document.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", document.SchemaVersion)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("offline validate invoked runner: %v", runner.calls)
	}

	invalid := writeTempTarget(t, invalidFixture)
	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute([]string{"validate", "--file", invalid, "--offline"}); code != 1 {
		t.Fatalf("invalid code = %d, want 1; stderr: %s", code, buffers.stderr.String())
	}
}

// TestVerifyMountFindmntArgv проверяет, что verify-mount вызывает ровно один
// статический findmnt с документированными аргументами и подставляет runner.
func TestVerifyMountFindmntArgv(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	runner := &fakeRunner{}
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, runner)

	if code := app.Execute([]string{"verify-mount", "--file", target}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %v, want exactly 1", runner.calls)
	}
	call := runner.calls[0]
	if call.name != "findmnt" || len(call.args) != 3 ||
		call.args[0] != "--verify" || call.args[1] != "--tab-file" || call.args[2] == "" {
		t.Fatalf("unexpected findmnt argv: %+v", call)
	}
}

// TestBackupCreatesFilesAndDryRunIsPure проверяет ручной backup: файлы
// создаются, JSON содержит backup_id, а dry-run не выполняет никакого I/O.
func TestBackupCreatesFilesAndDryRunIsPure(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	directory := t.TempDir()
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})

	if code := app.Execute([]string{"backup", "--file", target, "--backup-dir", directory, "-o", "json"}); code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	var document struct {
		SchemaVersion int    `json:"schema_version"`
		BackupID      string `json:"backup_id"`
		DataPath      string `json:"data_path"`
		MetadataPath  string `json:"metadata_path"`
		DryRun        bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(&buffers.stdout).Decode(&document); err != nil {
		t.Fatalf("decode backup JSON: %v", err)
	}
	if document.BackupID == "" || document.DryRun {
		t.Fatalf("document = %+v, want real backup", document)
	}
	for _, path := range []string{document.DataPath, document.MetadataPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("backup file missing: %v", err)
		}
	}

	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute([]string{"backup", "--file", target, "--backup-dir", directory, "--dry-run", "-o", "json"}); code != 0 {
		t.Fatalf("dry-run code = %d, want 0", code)
	}
	if backups := listBackupData(t, directory); len(backups) != 1 {
		t.Fatalf("dry-run created backups: %v", backups)
	}
}

// TestRollbackDryRunAndRealRestore проверяет ручной rollback: dry-run не
// меняет target, а реальный откат с подтверждением «y» восстанавливает байты
// и создаёт safety backup до восстановления.
func TestRollbackDryRunAndRealRestore(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	directory := t.TempDir()

	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute([]string{"backup", "--file", target, "--backup-dir", directory, "-o", "json"}); code != 0 {
		t.Fatalf("backup code = %d, want 0", code)
	}
	var backupDoc struct {
		BackupID string `json:"backup_id"`
	}
	if err := json.NewDecoder(&buffers.stdout).Decode(&backupDoc); err != nil {
		t.Fatalf("decode backup JSON: %v", err)
	}

	modified := []byte(e2eFixture + "/dev/sdd1 /mnt/extra ext4 defaults 0 0\n")
	if err := os.WriteFile(target, modified, 0o600); err != nil {
		t.Fatal(err)
	}

	// Dry-run оставляет target и не создаёт safety backup.
	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute([]string{
		"rollback", "--file", target, "--backup-dir", directory,
		"--restore", backupDoc.BackupID, "--dry-run", "-o", "json",
	}); code != 0 {
		t.Fatalf("dry-run code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, target), modified) {
		t.Fatal("dry-run rollback changed target")
	}
	if backups := listBackupData(t, directory); len(backups) != 1 {
		t.Fatalf("dry-run created safety backup: %v", backups)
	}

	// Non-TTY без --yes требует подтверждения.
	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
	if code := app.Execute([]string{
		"rollback", "--file", target, "--backup-dir", directory, "--restore", backupDoc.BackupID,
	}); code != 2 {
		t.Fatalf("non-TTY code = %d, want 2; stderr: %s", code, buffers.stderr.String())
	}

	// TTY с ответом «y» восстанавливает байты и создаёт safety backup.
	buffers = &appBuffers{}
	app = newApp(buffers, strings.NewReader("y\n"), true, &fakeRunner{})
	if code := app.Execute([]string{
		"rollback", "--file", target, "--backup-dir", directory, "--restore", backupDoc.BackupID, "-o", "json",
	}); code != 0 {
		t.Fatalf("restore code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatalf("target not restored: %q", readTargetBytes(t, target))
	}
	var rollbackDoc struct {
		SchemaVersion    int    `json:"schema_version"`
		SelectedBackupID string `json:"selected_backup_id"`
		SafetyBackupID   string `json:"safety_backup_id"`
		DryRun           bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(&buffers.stdout).Decode(&rollbackDoc); err != nil {
		t.Fatalf("decode rollback JSON: %v", err)
	}
	if rollbackDoc.SelectedBackupID != backupDoc.BackupID || rollbackDoc.SafetyBackupID == "" || rollbackDoc.DryRun {
		t.Fatalf("rollback document = %+v", rollbackDoc)
	}
	// Ровно две копии: ручная и safety backup перед восстановлением.
	if backups := listBackupData(t, directory); len(backups) != 2 {
		t.Fatalf("backups after restore = %v, want 2", backups)
	}
}

// TestUsageErrorsForMutations проверяет usage errors уровня команды:
// отсутствие обязательного поля add, конфликт явных селекторов и
// несовместимость --comment с --clear-comment у edit.
func TestUsageErrorsForMutations(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "add missing options",
			args: []string{"add", "--file", target, "--device", "/dev/sdd1", "--mount", "/mnt/new", "--type", "ext4", "--yes"},
			want: "--options",
		},
		{
			name: "conflicting selectors",
			args: []string{"delete", "--file", target, "--line", "2", "--mount-point", "/mnt/data", "--yes"},
			want: "селектор",
		},
		{
			name: "comment clear conflict",
			args: []string{"edit", "--file", target, "--mount-point", "/mnt/data", "--comment", "x", "--clear-comment", "--yes"},
			want: "--clear-comment",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			buffers := &appBuffers{}
			app := newApp(buffers, strings.NewReader(""), false, &fakeRunner{})
			if code := app.Execute(testCase.args); code != 2 {
				t.Fatalf("code = %d, want 2; stderr: %s", code, buffers.stderr.String())
			}
			if !strings.Contains(buffers.stderr.String(), testCase.want) {
				t.Fatalf("stderr %q missing %q", buffers.stderr.String(), testCase.want)
			}
		})
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("usage errors changed target")
	}
}

// TestMachineStdoutExcludesPromptsAndDiff проверяет, что machine stdout
// содержит ровно один JSON-документ без текста prompt и diff: весь diff и
// диагностика перед подтверждением идут только в stderr.
func TestMachineStdoutExcludesPromptsAndDiff(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	buffers := &appBuffers{}
	app := newApp(buffers, strings.NewReader("y\n"), true, &fakeRunner{})

	code := app.Execute([]string{
		"add", "--file", target, "--device", "/dev/sdd1", "--mount", "/mnt/new",
		"--type", "ext4", "--options", "defaults", "-o", "json",
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	stdout := buffers.stdout.String()
	if strings.Contains(stdout, "[y/N]") || strings.Contains(stdout, "--- <original>") {
		t.Fatalf("stdout mixed with prompt/diff: %q", stdout)
	}
	decodeSingleMutationDoc(t, &buffers.stdout)
	if !strings.Contains(buffers.stderr.String(), "[y/N]") {
		t.Fatalf("prompt missing in stderr: %q", buffers.stderr.String())
	}
}

// TestExitMappingIntegration проверяет сквозной маппинг ошибок транзакции на
// exit codes 1/4/5/6/7 через подменённую границу ExecuteTransaction, а также
// приоритет code 7 над прочими диагностиками результата.
func TestExitMappingIntegration(t *testing.T) {
	cases := []struct {
		kind transaction.FailureKind
		code int
	}{
		{transaction.FailureValidation, 1},
		{transaction.FailureConflict, 4},
		{transaction.FailureExternal, 5},
		{transaction.FailureIO, 6},
		{transaction.FailureRotation, 6},
		{transaction.FailureRollback, 7},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.kind), func(t *testing.T) {
			target := writeTempTarget(t, e2eFixture)
			buffers := &appBuffers{}
			kind := testCase.kind
			app := cmd.NewApp(cmd.AppOptions{
				Stdout: &buffers.stdout,
				Stderr: &buffers.stderr,
				Stdin:  strings.NewReader(""),
				TTY:    func() bool { return false },
				Runner: &fakeRunner{},
				Dependencies: &cliapp.Dependencies{
					ExecuteTransaction: func(_ context.Context, _ transaction.Plan, _ transaction.Options) (transaction.TransactionResult, error) {
						return transaction.TransactionResult{
								Changed: true,
								// Диагностики результата с severity=error не должны
								// перекрывать код критического сбоя rollback.
								PostVerification: sysutil.Verification{Report: reportWithError()},
							},
							&transaction.TransactionError{Kind: kind, Err: errors.New("fake failure")}
					},
				},
			})

			code := app.Execute([]string{
				"add", "--file", target, "--device", "/dev/sdd1", "--mount", "/mnt/new",
				"--type", "ext4", "--options", "defaults", "--yes", "-o", "json",
			})
			if code != testCase.code {
				t.Fatalf("code = %d, want %d; stderr: %s", code, testCase.code, buffers.stderr.String())
			}
			if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
				t.Fatal("failed transaction changed target")
			}
			// Structured результат публикуется даже при ошибке транзакции.
			decodeSingleMutationDoc(t, &buffers.stdout)
		})
	}
}

// reportWithError возвращает отчёт с одной severity=error диагностикой.
func reportWithError() models.Report {
	return models.Report{Diagnostics: []models.Diagnostic{
		{Severity: models.SeverityError, Code: "fake.error", Message: "fake"},
	}}
}

// transactionProbe фиксирует число вызовов подменённой границы Service:
// fake ExecuteTransaction/CreateBackup инкрементируют счётчики, поэтому тесты
// доказывают отсутствие backup/lock/write, а не только неизменность байтов.
type transactionProbe struct {
	transactions int
	backups      int
}

// dependencies собирает перекрытия cliapp.Dependencies: ExecuteTransaction
// инкрементирует счётчик и завершается успехом с заданным Changed.
func (p *transactionProbe) dependencies(changed bool) *cliapp.Dependencies {
	return &cliapp.Dependencies{
		ExecuteTransaction: func(_ context.Context, _ transaction.Plan, _ transaction.Options) (transaction.TransactionResult, error) {
			p.transactions++
			return transaction.TransactionResult{Changed: changed}, nil
		},
		CreateBackup: func(_ writer.Snapshot, _ backup.CreateOptions) (backup.Backup, error) {
			p.backups++
			return backup.Backup{}, nil
		},
	}
}

// TestMutationPreviewValidationErrorBlocksTransaction проверяет, что candidate
// с severity=error диагностиками (здесь — дубликат mount point) блокируется
// кодом 1 до transaction: ExecuteTransaction не вызывается, target не
// изменён, structured результат публикуется с диагностиками.
func TestMutationPreviewValidationErrorBlocksTransaction(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	probe := &transactionProbe{}
	buffers := &appBuffers{}
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:       &buffers.stdout,
		Stderr:       &buffers.stderr,
		Stdin:        strings.NewReader(""),
		TTY:          func() bool { return false },
		Runner:       &fakeRunner{},
		Dependencies: probe.dependencies(true),
	})

	// /mnt/data уже существует в fixture: валидатор кандидата выдаёт
	// duplicate.mount с severity=error ещё до подтверждения и transaction.
	code := app.Execute([]string{
		"add", "--file", target, "--device", "/dev/sdd1", "--mount", "/mnt/data",
		"--type", "ext4", "--options", "defaults", "--yes", "-o", "json",
	})
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr: %s", code, buffers.stderr.String())
	}
	if probe.transactions != 0 {
		t.Fatalf("ExecuteTransaction called %d times, want 0", probe.transactions)
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("blocked mutation changed target")
	}
	if !strings.Contains(buffers.stderr.String(), "внутреннюю проверку") {
		t.Fatalf("stderr missing validation explanation: %q", buffers.stderr.String())
	}
	document := decodeSingleMutationDoc(t, &buffers.stdout)
	if document.BackupID != "" {
		t.Fatalf("blocked mutation published backup_id %q", document.BackupID)
	}
	if len(document.Diagnostics) == 0 {
		t.Fatal("structured result missing diagnostics for blocked candidate")
	}
}

// TestNoOpEditSkipsTransactionAndBackup проверяет документированную семантику
// no-op: edit с тем же значением поля завершается кодом 0 без transaction и
// backup, а machine result публикует changed=false и dry_run=false.
func TestNoOpEditSkipsTransactionAndBackup(t *testing.T) {
	target := writeTempTarget(t, e2eFixture)
	probe := &transactionProbe{}
	buffers := &appBuffers{}
	app := cmd.NewApp(cmd.AppOptions{
		Stdout:       &buffers.stdout,
		Stderr:       &buffers.stderr,
		Stdin:        strings.NewReader(""),
		TTY:          func() bool { return false },
		Runner:       &fakeRunner{},
		Dependencies: probe.dependencies(true),
	})

	// --yes задан сознательно: no-op не должен требовать подтверждения и не
	// должен доходить до transaction даже с ним.
	code := app.Execute([]string{
		"edit", "--file", target, "--mount-point", "/mnt/data",
		"--options", "defaults,noatime", "--yes", "-o", "json",
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr: %s", code, buffers.stderr.String())
	}
	if probe.transactions != 0 || probe.backups != 0 {
		t.Fatalf("no-op invoked seams: transactions=%d backups=%d, want 0/0",
			probe.transactions, probe.backups)
	}
	if !bytes.Equal(readTargetBytes(t, target), []byte(e2eFixture)) {
		t.Fatal("no-op edit changed target bytes")
	}
	if backups := listBackupData(t, filepath.Dir(target)); len(backups) != 0 {
		t.Fatalf("no-op edit created backups: %v", backups)
	}
	document := decodeSingleMutationDoc(t, &buffers.stdout)
	if document.Changed || document.DryRun {
		t.Fatalf("document = %+v, want changed=false dry_run=false", document)
	}
	if !strings.Contains(buffers.stderr.String(), "изменений нет") {
		t.Fatalf("stderr missing no-op message: %q", buffers.stderr.String())
	}
}
