// Пакет backup реализует создание резервных копий fstab с sidecar-метаданными.
//
// Этот тестовый файл покрывает Task 3 (Rotate): безопасная ротация backups с
// default keep=10, oldest-first удалением только собственных проверенных пар,
// защитой protected ID и partial result при сбоях удаления. Тесты используют
// только t.TempDir; root не требуется, реальный /etc/fstab не трогается.
package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// twelveMoments возвращает 12 моментов с шагом в час от старого к новому:
// moments[0] — самый старый backup, moments[11] — самый новый.
func twelveMoments() []time.Time {
	moments := make([]time.Time, 0, 12)
	for age := 11; age >= 0; age-- {
		moments = append(moments, fixedNow.Add(-time.Duration(age)*time.Hour))
	}
	return moments
}

// pathExists сообщает, существует ли путь (symlink считается существующим).
func pathExists(t *testing.T, path string) bool {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		return true
	} else if errors.Is(err, os.ErrNotExist) {
		return false
	} else {
		t.Fatalf("прочитать metadata %s: %v", path, err)
		return false
	}
}

// backupIDs возвращает backup ID списка в исходном порядке для сравнений.
func backupIDs(backups []Backup) []string {
	ids := make([]string, 0, len(backups))
	for _, backup := range backups {
		ids = append(ids, backup.ID)
	}
	return ids
}

// assertIDsEqual сравнивает два списка backup ID точно и в порядке.
func assertIDsEqual(t *testing.T, got []Backup, wantIDs []string) {
	t.Helper()
	gotIDs := backupIDs(got)
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("IDs: получено %v, ожидалось %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("IDs: получено %v, ожидалось %v", gotIDs, wantIDs)
		}
	}
}

// TestRotateKeepsNewestRemovesOldestAndProtects проверяет основной сценарий
// ротации: из 12 candidates с keep=10 удаляются два самых старых (data+sidecar),
// newest 10 остаются, protected ID из candidates не удаляется, а чужой valid
// backup, пара с чужим prefix, corrupt sidecar и symlink на чужой sidecar
// остаются нетронутыми. Removed идут oldest→newest, Failed пуст.
func TestRotateKeepsNewestRemovesOldestAndProtects(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)

	// Чужой valid backup другого target с default prefix, новее всех своих:
	// ротация не должна его увидеть и тем более удалить.
	foreign := newTargetSnapshot(t, []byte("UUID=zzz / xfs defaults 0 0\n"))
	foreignBackup, err := Create(foreign, CreateOptions{
		Directory:          fixture.dir,
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("создать чужой backup: %v", err)
	}

	// Валидная пара того же target, но с чужим prefix.
	otherPrefixBackup, err := Create(newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n")), CreateOptions{
		Directory:          fixture.dir,
		Prefix:             "other.bak",
		Operation:          OperationManual,
		SourceVerification: SourceNotChecked,
		Now:                fixedNow.Add(45 * time.Minute),
	})
	if err != nil {
		t.Fatalf("создать backup с другим prefix: %v", err)
	}

	// Corrupt sidecar с подходящим именем default prefix.
	corruptData := filepath.Join(fixture.dir, DefaultBackupPrefix+".2020-01-01_00-00-00.000000001Z")
	if err := os.WriteFile(corruptData, []byte("data"), 0o600); err != nil {
		t.Fatalf("создать corrupt data: %v", err)
	}
	if err := os.WriteFile(corruptData+".json", []byte("{"), 0o600); err != nil {
		t.Fatalf("создать corrupt sidecar: %v", err)
	}

	// Symlink на чужой valid sidecar: ротация не читает и не удаляет ссылки.
	linkSidecar := filepath.Join(fixture.dir, DefaultBackupPrefix+".2021-01-01_00-00-00.000000001Z.json")
	if err := os.Symlink(foreignBackup.MetadataPath, linkSidecar); err != nil {
		t.Fatalf("создать symlink sidecar: %v", err)
	}

	// Protected: один реальный ID из retained-зоны и один заведомо отсутствующий.
	protectedIDs := []string{fixture.ids[2], "2099-01-01_00-00-00.000000001Z"}
	result, err := Rotate(fixture.target, "", fixture.dir, 10, protectedIDs)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Удалены ровно два самых старых, oldest→newest; Failed пуст.
	assertIDsEqual(t, result.Removed, fixture.ids[:2])
	assertIDsEqual(t, result.Failed, nil)
	// Skipped содержит retained и protected кандидатов oldest→newest.
	assertIDsEqual(t, result.Skipped, fixture.ids[2:])

	// Удалённая пара исчезла целиком: data и sidecar.
	for _, backup := range fixture.backups[:2] {
		if pathExists(t, backup.Path) {
			t.Errorf("data удалённого backup осталась: %s", backup.Path)
		}
		if pathExists(t, backup.MetadataPath) {
			t.Errorf("sidecar удалённого backup остался: %s", backup.MetadataPath)
		}
	}

	// Protected и остальные retained копии целы: оба файла каждой пары.
	for _, backup := range fixture.backups[2:] {
		if !pathExists(t, backup.Path) {
			t.Errorf("data retained backup удалена: %s", backup.Path)
		}
		if !pathExists(t, backup.MetadataPath) {
			t.Errorf("sidecar retained backup удалён: %s", backup.MetadataPath)
		}
	}

	// Чужие и повреждённые файлы нетронуты: нет hidden deletion.
	for _, path := range []string{
		foreignBackup.Path, foreignBackup.MetadataPath,
		otherPrefixBackup.Path, otherPrefixBackup.MetadataPath,
		corruptData, corruptData + ".json", linkSidecar,
	} {
		if !pathExists(t, path) {
			t.Errorf("чужой/повреждённый файл удалён ротацией: %s", path)
		}
	}
	info, err := os.Lstat(linkSidecar)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink-запись %s перестала быть symlink", linkSidecar)
	}

	// Удалённая пара больше не возвращается List: остаются newest 10.
	listed, err := List(fixture.target, "", fixture.dir)
	if err != nil {
		t.Fatalf("List после Rotate: %v", err)
	}
	assertIDsEqual(t, listed, reverseIDs(fixture.ids[2:]))
}

// reverseIDs разворачивает список ID: newest-first ожидание для List.
func reverseIDs(ids []string) []string {
	reversed := make([]string, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		reversed = append(reversed, ids[i])
	}
	return reversed
}

// TestRotateKeepVariations проверяет нормализацию keep: keep <= 0 заменяется
// DefaultKeepBackups, keep >= len(candidates) ничего не удаляет, промежуточные
// значения удаляют ровно len-keep самых старых. Отсутствующий protected ID
// не влияет на результат.
func TestRotateKeepVariations(t *testing.T) {
	cases := []struct {
		name         string
		keep         int
		protectedIDs []string
		wantRemoved  []string
	}{
		{name: "keep=10 удаляет две oldest", keep: 10, wantRemoved: nil},
		{name: "keep=0 нормализуется в default", keep: 0, wantRemoved: nil},
		{name: "keep<0 нормализуется в default", keep: -5, wantRemoved: nil},
		{name: "keep=11 удаляет одну oldest", keep: 11, wantRemoved: nil},
		{name: "keep=12 ничего не удаляет", keep: 12, wantRemoved: nil},
		{name: "keep>len ничего не удаляет", keep: 100, wantRemoved: nil},
		{
			name:         "отсутствующий protected не мешает",
			keep:         10,
			protectedIDs: []string{"2099-01-01_00-00-00.000000001Z"},
			wantRemoved:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)
			result, err := Rotate(fixture.target, "", fixture.dir, tc.keep, tc.protectedIDs)
			if err != nil {
				t.Fatalf("Rotate: %v", err)
			}

			removedCount := 12 - tc.keep
			if tc.keep <= 0 {
				removedCount = 12 - DefaultKeepBackups
			}
			if removedCount < 0 {
				removedCount = 0
			}
			wantRemoved := fixture.ids[:removedCount]
			if len(tc.wantRemoved) > 0 {
				wantRemoved = tc.wantRemoved
			}
			assertIDsEqual(t, result.Removed, wantRemoved)
			assertIDsEqual(t, result.Failed, nil)
			assertIDsEqual(t, result.Skipped, fixture.ids[removedCount:])

			// Каждая удалённая пара исчезла целиком, каждая retained цела.
			for _, backup := range fixture.backups[:removedCount] {
				if pathExists(t, backup.Path) || pathExists(t, backup.MetadataPath) {
					t.Errorf("backup %s должен быть удалён целиком", backup.ID)
				}
			}
			for _, backup := range fixture.backups[removedCount:] {
				if !pathExists(t, backup.Path) || !pathExists(t, backup.MetadataPath) {
					t.Errorf("backup %s должен остаться целиком", backup.ID)
				}
			}
		})
	}
}

// TestRotateSkipsIncompletePair проверяет, что заранее удалённая data или
// metadata не ломают ротацию: такая пара не проходит verify seam внутри List
// и исключается из candidates, а её осиротевший файл не трогается.
func TestRotateSkipsIncompletePair(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)

	// Удаляем data самой старой копии: пара становится incomplete.
	if err := os.Remove(fixture.backups[0].Path); err != nil {
		t.Fatalf("удалить data самой старой копии: %v", err)
	}

	result, err := Rotate(fixture.target, "", fixture.dir, 10, nil)
	if err != nil {
		t.Fatalf("Rotate при incomplete pair: %v", err)
	}
	// Из 11 verified candidates удаляется одна самая старая — ids[1].
	assertIDsEqual(t, result.Removed, fixture.ids[1:2])
	assertIDsEqual(t, result.Failed, nil)
	// Осиротевший sidecar ids[0] не входил в candidates и не удалён.
	if !pathExists(t, fixture.backups[0].MetadataPath) {
		t.Errorf("осиротевший sidecar удалён: %s", fixture.backups[0].MetadataPath)
	}

	listed, err := List(fixture.target, "", fixture.dir)
	if err != nil {
		t.Fatalf("List после Rotate: %v", err)
	}
	// 12 - 1 incomplete - 1 удалённая = 10.
	if len(listed) != 10 {
		t.Fatalf("List вернул %d backups, ожидалось 10", len(listed))
	}
}

// TestRotateProtectedSurvivesZeroKeep проверяет защиту selected rollback:
// protected ID из зоны удаления не удаляется даже при keep=0 (keep нормализуется
// в DefaultKeepBackups, но protected проверяется явно и независимо).
func TestRotateProtectedSurvivesZeroKeep(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)

	result, err := Rotate(fixture.target, "", fixture.dir, 0, []string{fixture.ids[0]})
	if err != nil {
		t.Fatalf("Rotate с protected и keep=0: %v", err)
	}
	// ids[0] protected осталась, ids[1] удалена как вторая по старости.
	assertIDsEqual(t, result.Removed, fixture.ids[1:2])
	assertIDsEqual(t, result.Failed, nil)
	// Skipped: protected ids[0] и все retained ids[2:], oldest→newest.
	assertIDsEqual(t, result.Skipped, append([]string{fixture.ids[0]}, fixture.ids[2:]...))
	if !pathExists(t, fixture.backups[0].Path) || !pathExists(t, fixture.backups[0].MetadataPath) {
		t.Errorf("protected backup %s удалён", fixture.ids[0])
	}
}

// TestRotateResultSlicesNonNil проверяет, что RotationResult содержит non-nil
// slices при любом исходе: пустом каталоге и ошибке List до сканирования.
func TestRotateResultSlicesNonNil(t *testing.T) {
	source := newTargetSnapshot(t, []byte("UUID=abc / ext4 defaults 0 1\n"))

	// Пустой каталог: успех без кандидатов.
	result, err := Rotate(source.Path, "", t.TempDir(), 10, nil)
	if err != nil {
		t.Fatalf("Rotate по пустому каталогу: %v", err)
	}
	if result.Removed == nil || result.Skipped == nil || result.Failed == nil {
		t.Errorf("slices RotationResult nil при пустом результате: %+v", result)
	}

	// Несуществующий каталог: ошибка List, slices всё равно non-nil.
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	result, err = Rotate(source.Path, "", missing, 10, nil)
	if err == nil {
		t.Fatal("Rotate по несуществующему каталогу должен возвращать ошибку")
	}
	if result.Removed == nil || result.Skipped == nil || result.Failed == nil {
		t.Errorf("slices RotationResult nil при ошибке List: %+v", result)
	}
}

// TestRotateNilRemoveOperations проверяет, что неполный per-call config
// отклоняется до первого удаления: файлы кандидатов остаются нетронутыми.
func TestRotateNilRemoveOperations(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)

	result, err := rotateWithOperations(fixture.target, "", fixture.dir, 10, nil, rotateOperations{})
	if err == nil {
		t.Fatal("Rotate с nil remove должен возвращать ошибку")
	}
	assertIDsEqual(t, result.Removed, nil)
	assertIDsEqual(t, result.Failed, nil)
	for _, backup := range fixture.backups {
		if !pathExists(t, backup.Path) || !pathExists(t, backup.MetadataPath) {
			t.Errorf("backup %s удалён при nil remove", backup.ID)
		}
	}
}

// errInjectedRemove — sentinel ошибка fault injection удаления: tests проверяют
// её через errors.Is, отличая от реальных ошибок файловой системы.
var errInjectedRemove = errors.New("injected remove error")

// injectRemoveError оборачивает production remove так, что для путей из failOn
// возвращается заданная ошибка вместо реального удаления; остальные пути
// удаляются штатно. Реальное удаление внутри fake имитирует гонку исчезновения.
func injectRemoveError(real func(string) error, failOn map[string]error, alsoRemove map[string]bool) func(string) error {
	return func(path string) error {
		if err, ok := failOn[path]; ok {
			if alsoRemove[path] {
				_ = real(path)
			}
			return err
		}
		return real(path)
	}
}

// failingRotateOperations возвращает production config ротации с подменённым
// remove: ошибки для путей failOn, реальное удаление для alsoRemove до возврата
// os.ErrNotExist (имитация гонки).
func failingRotateOperations(failOn map[string]error, alsoRemove map[string]bool) rotateOperations {
	operations := productionRotateOperations()
	operations.remove = injectRemoveError(operations.remove, failOn, alsoRemove)
	return operations
}

// TestRotateRemoveSidecarError проверяет fault injection на sidecar: candidate
// попадает в Failed, первичная ошибка сохраняется через errors.Is, data этой
// пары не удаляется, а более новые non-protected копии после сбоя не трогаются.
func TestRotateRemoveSidecarError(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)

	// keep=8: зона удаления ids[0..3]; сбой на sidecar ids[2].
	failing := fixture.backups[2]
	operations := failingRotateOperations(map[string]error{
		failing.MetadataPath: errInjectedRemove,
	}, nil)

	result, err := rotateWithOperations(fixture.target, "", fixture.dir, 8, nil, operations)
	if err == nil {
		t.Fatal("Rotate при сбое sidecar должен возвращать ошибку")
	}
	if !errors.Is(err, errInjectedRemove) {
		t.Errorf("ошибка не сохраняет injected причину: %v", err)
	}
	// Старые копии до сбоя удалены, сбойная — в Failed, newer после сбоя целы.
	assertIDsEqual(t, result.Removed, fixture.ids[:2])
	assertIDsEqual(t, result.Failed, fixture.ids[2:3])
	assertIDsEqual(t, result.Skipped, fixture.ids[3:])
	// Partial result explicit: sidecar сбойной пары цел, data не удалялась.
	if !pathExists(t, failing.MetadataPath) {
		t.Errorf("sidecar failed backup удалён: %s", failing.MetadataPath)
	}
	if !pathExists(t, failing.Path) {
		t.Errorf("data failed backup удалена при сбое sidecar: %s", failing.Path)
	}
	// Кандидат после сбоя не удалялся дальше.
	if !pathExists(t, fixture.backups[3].Path) || !pathExists(t, fixture.backups[3].MetadataPath) {
		t.Errorf("backup после сбоя удалён: %s", fixture.ids[3])
	}
}

// TestRotateRemoveDataError проверяет fault injection на data: sidecar уже
// удалён, сбой на data помещает candidate в Failed с partial result explicit —
// sidecar отсутствует, data осталась, дальнейшие удаления остановлены.
func TestRotateRemoveDataError(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)

	failing := fixture.backups[2]
	operations := failingRotateOperations(map[string]error{
		failing.Path: errInjectedRemove,
	}, nil)

	result, err := rotateWithOperations(fixture.target, "", fixture.dir, 8, nil, operations)
	if err == nil {
		t.Fatal("Rotate при сбое data должен возвращать ошибку")
	}
	if !errors.Is(err, errInjectedRemove) {
		t.Errorf("ошибка не сохраняет injected причину: %v", err)
	}
	assertIDsEqual(t, result.Removed, fixture.ids[:2])
	assertIDsEqual(t, result.Failed, fixture.ids[2:3])
	assertIDsEqual(t, result.Skipped, fixture.ids[3:])
	// Sidecar уже удалён (порядок sidecar→data), data осталась: partial explicit.
	if pathExists(t, failing.MetadataPath) {
		t.Errorf("sidecar должен быть удалён до сбоя data: %s", failing.MetadataPath)
	}
	if !pathExists(t, failing.Path) {
		t.Errorf("data failed backup должна остаться: %s", failing.Path)
	}
}

// TestRotateNotExistRemovalCountsAsRemoved проверяет, что os.ErrNotExist при
// удалении одного файла пары (гонка исчезновения) не считается сбоем: второй
// файл всё равно удаляется, осиротевшие paths не остаются, candidate Removed.
func TestRotateNotExistRemovalCountsAsRemoved(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)

	// Sidecar самой старой копии уже исчезает к моменту remove (гонка): fake
	// реально удаляет файл и возвращает os.ErrNotExist.
	target := fixture.backups[0]
	operations := failingRotateOperations(map[string]error{
		target.MetadataPath: os.ErrNotExist,
	}, map[string]bool{target.MetadataPath: true})

	result, err := rotateWithOperations(fixture.target, "", fixture.dir, 10, nil, operations)
	if err != nil {
		t.Fatalf("Rotate при os.ErrNotExist: %v", err)
	}
	assertIDsEqual(t, result.Removed, fixture.ids[:2])
	assertIDsEqual(t, result.Failed, nil)
	// Оба файла пары отсутствуют после cleanup: осиротевших paths нет.
	if pathExists(t, target.Path) || pathExists(t, target.MetadataPath) {
		t.Errorf("осиротевшие paths остались для %s", target.ID)
	}
}

// TestRotateProtectedInRemovalZone проверяет защиту protected ID внутри зоны
// удаления: protected пропускается, остальные кандидаты зоны удаляются.
func TestRotateProtectedInRemovalZone(t *testing.T) {
	fixture := newSelectFixture(t, []byte("UUID=abc / ext4 defaults 0 1\n"), twelveMoments()...)

	// keep=8, зона ids[0..3]; ids[1] protected.
	result, err := Rotate(fixture.target, "", fixture.dir, 8, []string{fixture.ids[1]})
	if err != nil {
		t.Fatalf("Rotate с protected в зоне удаления: %v", err)
	}
	assertIDsEqual(t, result.Removed, []string{fixture.ids[0], fixture.ids[2], fixture.ids[3]})
	assertIDsEqual(t, result.Failed, nil)
	assertIDsEqual(t, result.Skipped, append([]string{fixture.ids[1]}, fixture.ids[4:]...))
	if !pathExists(t, fixture.backups[1].Path) || !pathExists(t, fixture.backups[1].MetadataPath) {
		t.Errorf("protected backup в зоне удаления удалён: %s", fixture.ids[1])
	}
}
