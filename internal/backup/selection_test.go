// Пакет backup реализует создание резервных копий fstab с sidecar-метаданными.
//
// Этот тестовый файл покрывает Task 2 (часть List/Select): безопасный scan
// backup directory с фильтрацией чужих и повреждённых файлов, детерминированный
// newest-first порядок и выбор копии по ID с exact/ambiguous/not-found
// сценариями. Тесты используют только t.TempDir; root не требуется.
package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selectFixture собирает каталог с несколькими backups одного target:
// moments задают время копий от старых к новым; explicit Directory изолирует
// копии от каталога target, чтобы fixture полностью контролировала scan.
type selectFixture struct {
	target  string
	content []byte
	dir     string
	ids     []string // backup ID по возрастанию времени: ids[0] — самый старый.
	backups []Backup // соответствуют ids.
}

// newSelectFixture создаёт target и directory, в котором Create размещает backups
// с заданными моментами времени. moments должны идти от старых к новым.
func newSelectFixture(t *testing.T, content []byte, moments ...time.Time) *selectFixture {
	t.Helper()
	source := newTargetSnapshot(t, content)
	directory := t.TempDir()
	fixture := &selectFixture{
		target:  source.Path,
		content: content,
		dir:     directory,
		ids:     make([]string, 0, len(moments)),
		backups: make([]Backup, 0, len(moments)),
	}
	for _, moment := range moments {
		backup, err := Create(source, CreateOptions{
			Directory:          directory,
			Operation:          OperationManual,
			SourceVerification: SourceNotChecked,
			Now:                moment,
		})
		if err != nil {
			t.Fatalf("создать backup fixture: %v", err)
		}
		fixture.ids = append(fixture.ids, backup.ID)
		fixture.backups = append(fixture.backups, backup)
	}
	return fixture
}

// backupPaths возвращает пути data всех backups в порядке их создания.
func backupPaths(backups []Backup) []string {
	paths := make([]string, 0, len(backups))
	for _, backup := range backups {
		paths = append(paths, backup.Path)
	}
	return paths
}

// TestListFiltersAndOrders проверяет, что List возвращает только backups текущего
// canonical target с заданным prefix, отсортированные newest first, и пропускает:
// чужой valid backup, symlink на чужой valid sidecar, corrupt JSON, чужой prefix,
// orphan sidecar без data и data-файлы без sidecar.
func TestListFiltersAndOrders(t *testing.T) {
	old := fixedNow.Add(-2 * time.Hour)
	middle := fixedNow.Add(-1 * time.Hour)
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), old, middle, fixedNow)

	// Чужой valid backup того же prefix в том же каталоге: source другого target.
	foreign := newTargetSnapshot(t, []byte("UUID=zzz / xfs defaults 0 0\n"))
	if _, err := Create(foreign, CreateOptions{
		Directory:          fixture.dir,
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow.Add(30 * time.Minute), // новее всех своих
	}); err != nil {
		t.Fatalf("создать чужой backup: %v", err)
	}

	// Corrupt sidecar с подходящим именем.
	corruptData := filepath.Join(fixture.dir, DefaultBackupPrefix+".2020-01-01_00-00-00.000000001Z")
	if err := os.WriteFile(corruptData, []byte("data"), 0o600); err != nil {
		t.Fatalf("создать corrupt data: %v", err)
	}
	if err := os.WriteFile(corruptData+".json", []byte("{"), 0o600); err != nil {
		t.Fatalf("создать corrupt sidecar: %v", err)
	}

	// Валидный backup своего target, но с другим prefix: чужой prefix.
	if _, err := Create(newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n")), CreateOptions{
		Directory:          fixture.dir,
		Prefix:             "other.bak",
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow.Add(45 * time.Minute),
	}); err != nil {
		t.Fatalf("создать backup с другим prefix: %v", err)
	}

	// Orphan sidecar без data.
	orphan := filepath.Join(fixture.dir, DefaultBackupPrefix+".2021-01-01_00-00-00.000000001Z.json")
	if err := os.WriteFile(orphan, marshalMetadataMap(t, validMetadataMap()), 0o600); err != nil {
		t.Fatalf("создать orphan sidecar: %v", err)
	}

	// Data-файл без sidecar — не backup.
	lonelyData := filepath.Join(fixture.dir, DefaultBackupPrefix+".2022-01-01_00-00-00.000000001Z")
	if err := os.WriteFile(lonelyData, []byte("lonely"), 0o600); err != nil {
		t.Fatalf("создать lonely data: %v", err)
	}

	// Symlink на чужой valid sidecar: scan не должен следовать по ссылке и не
	// должен удалять её (защита будущей rotation).
	linkSidecar := filepath.Join(fixture.dir, DefaultBackupPrefix+".2023-01-01_00-00-00.000000001Z.json")
	if err := os.Symlink(fixture.backups[0].MetadataPath, linkSidecar); err != nil {
		t.Fatalf("создать symlink sidecar: %v", err)
	}

	// Symlink на каталог с похожим именем: тоже пропускается без удаления.
	linkDir := filepath.Join(fixture.dir, DefaultBackupPrefix+".2024-01-01_00-00-00.000000001Z.json")
	if err := os.Symlink(t.TempDir(), linkDir); err != nil {
		t.Fatalf("создать directory symlink: %v", err)
	}

	listed, err := List(fixture.target, "", fixture.dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("List вернул %d backups, ожидалось 3: %v", len(listed), backupPaths(listed))
	}

	// Newest first: fixedNow, затем middle, затем old.
	wantNewestFirst := []Backup{fixture.backups[2], fixture.backups[1], fixture.backups[0]}
	for i, want := range wantNewestFirst {
		if listed[i].Path != want.Path {
			t.Errorf("List[%d]: получено %q, ожидалось %q", i, listed[i].Path, want.Path)
		}
		if listed[i].ID != want.ID {
			t.Errorf("List[%d].ID: получено %q, ожидалось %q", i, listed[i].ID, want.ID)
		}
	}

	// Symlink-записи не удалены scan.
	for _, link := range []string{linkSidecar, linkDir} {
		info, err := os.Lstat(link)
		if err != nil {
			t.Errorf("scan удалил symlink-запись %s: %v", link, err)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("запись %s перестала быть symlink", link)
		}
	}
}

// TestListTieOrderByPath проверяет детерминированный порядок равных timestamps:
// collision-копии одного ID (.1, .2) упорядочиваются по backup ID, затем по path,
// то есть base имя идёт первым. Порядок не зависит от порядка ReadDir.
func TestListTieOrderByPath(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), fixedNow, fixedNow, fixedNow)

	listed, err := List(fixture.target, "", fixture.dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("List вернул %d backups, ожидалось 3 (base, .1, .2)", len(listed))
	}
	// Все три копии одного момента: порядок фиксируется path — base, .1, .2.
	for i, suffix := range []string{"", ".1", ".2"} {
		want := filepath.Join(fixture.dir, DefaultBackupPrefix+"."+fixture.ids[0]+suffix)
		if listed[i].Path != want {
			t.Errorf("List[%d]: получено %q, ожидалось %q", i, listed[i].Path, want)
		}
	}
}

// TestListDefaultsToTargetDirectory проверяет, что пустой directory означает
// каталог canonical target: backups, созданные рядом с target, находятся без
// явного directory.
func TestListDefaultsToTargetDirectory(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	if _, err := Create(source, CreateOptions{
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	}); err != nil {
		t.Fatalf("создать backup рядом с target: %v", err)
	}

	listed, err := List(source.Path, "", "")
	if err != nil {
		t.Fatalf("List с пустым directory: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List вернул %d backups, ожидалось 1", len(listed))
	}
	if filepath.Dir(listed[0].Path) != filepath.Dir(source.Path) {
		t.Errorf("backup найден не в каталоге target: %q", listed[0].Path)
	}
}

// TestListEmptyDirectoryResult проверяет, что отсутствие подходящих backups —
// пустой результат без ошибки.
func TestListEmptyDirectoryResult(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	listed, err := List(source.Path, "", t.TempDir())
	if err != nil {
		t.Fatalf("List по пустому каталогу: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("List вернул backups из пустого каталога: %v", backupPaths(listed))
	}
}

// TestListUnreadableDirectory проверяет, что ошибка чтения самого каталога
// (несуществующий путь) возвращается вызывающему, а не превращается в пустой список.
func TestListUnreadableDirectory(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	if _, err := List(source.Path, "", missing); err == nil {
		t.Fatal("List по несуществующему каталогу должен возвращать ошибку")
	}
}

// TestSelectEmptyIDSelectsNewest проверяет, что пустой ID выбирает самую новую
// eligible копию и сохраняет исходный (пустой) запрос.
func TestSelectEmptyIDSelectsNewest(t *testing.T) {
	old := fixedNow.Add(-time.Hour)
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), old, fixedNow)

	selection, err := Select(fixture.target, "", fixture.dir, "")
	if err != nil {
		t.Fatalf("Select с пустым ID: %v", err)
	}
	if selection.RequestedID != "" {
		t.Errorf("RequestedID: получено %q, ожидалось пустой", selection.RequestedID)
	}
	if selection.Selected.Path != fixture.backups[1].Path {
		t.Errorf("Selected: получено %q, ожидалось newest %q", selection.Selected.Path, fixture.backups[1].Path)
	}
	if selection.Selected.ID != fixture.ids[1] {
		t.Errorf("Selected.ID: получено %q, ожидалось %q", selection.Selected.ID, fixture.ids[1])
	}
	if len(selection.Candidates) != 2 {
		t.Errorf("Candidates: получено %d, ожидалось 2", len(selection.Candidates))
	}
}

// TestSelectExistingID проверяет выбор точного ID: самая новая копия не
// перехватывает запрос, Selected — ровно запрошенная копия.
func TestSelectExistingID(t *testing.T) {
	old := fixedNow.Add(-time.Hour)
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), old, fixedNow)

	selection, err := Select(fixture.target, "", fixture.dir, fixture.ids[0])
	if err != nil {
		t.Fatalf("Select по существующему ID: %v", err)
	}
	if selection.RequestedID != fixture.ids[0] {
		t.Errorf("RequestedID: получено %q, ожидалось %q", selection.RequestedID, fixture.ids[0])
	}
	if selection.Selected.Path != fixture.backups[0].Path {
		t.Errorf("Selected: получено %q, ожидалось %q", selection.Selected.Path, fixture.backups[0].Path)
	}
}

// TestSelectCollisionSuffixOnly проверяет, что collision-копия ".1" выбирается
// по своему backup ID, когда она единственная с этим ID (base имя занято чужим
// файлом и в List не входит).
func TestSelectCollisionSuffixOnly(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	directory := t.TempDir()
	baseName := filepath.Join(directory, DefaultBackupPrefix+"."+expectedFixedID)

	// Base имя занято чужой парой с похожим именем: Create пойдёт на ".1".
	if err := os.WriteFile(baseName, []byte("чужой data"), 0o600); err != nil {
		t.Fatalf("создать чужой data: %v", err)
	}
	if err := os.WriteFile(baseName+".json", []byte("{}"), 0o600); err != nil {
		t.Fatalf("создать чужой sidecar: %v", err)
	}

	backup, err := Create(source, CreateOptions{
		Directory:          directory,
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Create collision backup: %v", err)
	}
	if backup.Path != baseName+".1" {
		t.Fatalf("collision path: получено %q, ожидалось %q.1", backup.Path, baseName)
	}

	selection, err := Select(source.Path, "", directory, expectedFixedID)
	if err != nil {
		t.Fatalf("Select collision ID: %v", err)
	}
	if selection.Selected.Path != backup.Path {
		t.Errorf("Selected: получено %q, ожидалось %q", selection.Selected.Path, backup.Path)
	}
	if len(selection.Candidates) != 1 {
		t.Errorf("Candidates: получено %d, ожидалось 1", len(selection.Candidates))
	}
}

// TestSelectMissingID проверяет, что отсутствующий ID возвращает ErrBackupNotFound
// с непустым Candidates — вызывающий видит доступные копии для диагностики.
func TestSelectMissingID(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), fixedNow)

	selection, err := Select(fixture.target, "", fixture.dir, "2020-01-01_00-00-00.000000001Z")
	if err == nil {
		t.Fatal("Select по отсутствующему ID должен возвращать ошибку")
	}
	if !errors.Is(err, ErrBackupNotFound) {
		t.Errorf("ошибка не оборачивает ErrBackupNotFound: %v", err)
	}
	if selection.RequestedID != "2020-01-01_00-00-00.000000001Z" {
		t.Errorf("RequestedID: получено %q", selection.RequestedID)
	}
	if len(selection.Candidates) == 0 {
		t.Error("Candidates должен быть непустым при ErrBackupNotFound")
	}
}

// TestSelectAmbiguousID проверяет, что два файла одного backup ID (base и ".1")
// дают ErrBackupAmbiguous, а Candidates содержат оба в детерминированном
// newest/suffix порядке: base имя раньше collision-суффикса.
func TestSelectAmbiguousID(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), fixedNow, fixedNow)

	selection, err := Select(fixture.target, "", fixture.dir, fixture.ids[0])
	if err == nil {
		t.Fatal("Select по неоднозначному ID должен возвращать ошибку")
	}
	if !errors.Is(err, ErrBackupAmbiguous) {
		t.Errorf("ошибка не оборачивает ErrBackupAmbiguous: %v", err)
	}
	if len(selection.Candidates) != 2 {
		t.Fatalf("Candidates: получено %d, ожидалось 2", len(selection.Candidates))
	}
	basePath := filepath.Join(fixture.dir, DefaultBackupPrefix+"."+fixture.ids[0])
	suffixPath := basePath + ".1"
	got := []string{selection.Candidates[0].Path, selection.Candidates[1].Path}
	if got[0] != basePath || got[1] != suffixPath {
		t.Errorf("порядок Candidates: получено %v, ожидалось [%s %s]", got, basePath, suffixPath)
	}
	if selection.Selected.Path != "" {
		t.Errorf("Selected должен быть пустым при ambiguous: %q", selection.Selected.Path)
	}
}

// TestSelectPrefixMismatch проверяет, что prefix ограничивает выбор: валидный
// backup с другим prefix в том же каталоге не находится, даже если sidecar валиден.
func TestSelectPrefixMismatch(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))
	directory := t.TempDir()
	if _, err := Create(source, CreateOptions{
		Directory:          directory,
		Prefix:             "custom.bak",
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow,
	}); err != nil {
		t.Fatalf("создать backup с custom prefix: %v", err)
	}

	// Default prefix не видит custom.bak-копию.
	selection, err := Select(source.Path, "", directory, expectedFixedID)
	if err == nil {
		t.Fatal("Select с несовпадающим prefix должен возвращать ErrBackupNotFound")
	}
	if !errors.Is(err, ErrBackupNotFound) {
		t.Errorf("ошибка не оборачивает ErrBackupNotFound: %v", err)
	}

	// А с правильным prefix копия находится.
	selection, err = Select(source.Path, "custom.bak", directory, expectedFixedID)
	if err != nil {
		t.Fatalf("Select с совпадающим prefix: %v", err)
	}
	if selection.Selected.ID != expectedFixedID {
		t.Errorf("Selected.ID: получено %q, ожидалось %q", selection.Selected.ID, expectedFixedID)
	}
}

// TestSelectSkipsSymlinkEntries проверяет, что symlink-записи в backup directory
// не сканируются как backup и не удаляются: Select игнорирует их, а записи целы
// после вызова. Это также защита будущей rotation от удаления чужих ссылок.
func TestSelectSkipsSymlinkEntries(t *testing.T) {
	old := fixedNow.Add(-time.Hour)
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), old, fixedNow)

	linkSidecar := filepath.Join(fixture.dir, DefaultBackupPrefix+".2099-01-01_00-00-00.000000001Z.json")
	if err := os.Symlink(fixture.backups[0].MetadataPath, linkSidecar); err != nil {
		t.Fatalf("создать symlink sidecar: %v", err)
	}
	linkDir := filepath.Join(fixture.dir, DefaultBackupPrefix+".2098-01-01_00-00-00.000000001Z.json")
	if err := os.Symlink(t.TempDir(), linkDir); err != nil {
		t.Fatalf("создать directory symlink: %v", err)
	}

	selection, err := Select(fixture.target, "", fixture.dir, "")
	if err != nil {
		t.Fatalf("Select при symlink-записях: %v", err)
	}
	if selection.Selected.Path != fixture.backups[1].Path {
		t.Errorf("Selected: получено %q, ожидалось newest %q", selection.Selected.Path, fixture.backups[1].Path)
	}
	if len(selection.Candidates) != 2 {
		t.Errorf("Candidates: получено %d, ожидалось 2", len(selection.Candidates))
	}

	for _, link := range []string{linkSidecar, linkDir} {
		info, statErr := os.Lstat(link)
		if statErr != nil {
			t.Errorf("Select удалил symlink-запись %s: %v", link, statErr)
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("запись %s перестала быть symlink", link)
		}
	}
}

// TestSelectForeignTargetNotVisible проверяет, что backup чужого target не
// участвует в выборе: даже единственная копия в каталоге не выбирается, если
// её source_path указывает на другой canonical target.
func TestSelectForeignTargetNotVisible(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), fixedNow)

	// Другой target в том же каталоге: своих копий нет.
	other := newTargetSnapshot(t, []byte("UUID=zzz / xfs defaults 0 0\n"))
	selection, err := Select(other.Path, "", fixture.dir, "")
	if err == nil {
		t.Fatal("Select чужого target без своих backups должен возвращать ошибку")
	}
	if !errors.Is(err, ErrBackupNotFound) {
		t.Errorf("ошибка не оборачивает ErrBackupNotFound: %v", err)
	}
	if selection.Selected.Path != "" {
		t.Errorf("Selected должен быть пустым: %q", selection.Selected.Path)
	}
}
