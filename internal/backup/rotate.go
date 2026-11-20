// Этот файл реализует ротацию backups: Rotate повторно использует List как
// единственный источник candidates, поэтому удаляются только собственные
// проверенные пары текущего target с заданным prefix. Чужие, повреждённые,
// symlink и непроверенные записи в каталоге не входят в candidates и никогда
// не удаляются; os.RemoveAll не вызывается. Ошибка удаления останавливает
// дальнейшие удаления и возвращается с explicit partial RotationResult, где
// Failed содержит сбойную пару, а Skipped — всё, что осталось на диске.
// Rotate не реализует transaction, automatic/manual rollback и dry-run:
// будущая transaction обязана интерпретировать partial result сама.

package backup

import (
	"errors"
	"fmt"
	"os"
)

// RotationResult — детальный итог одной ротации. Сlices non-nil при любом
// исходе; Removed идут oldest→newest, Skipped тоже упорядочены oldest→newest.
// Failed содержит кандидата, чьё удаление не удалось: его файлы могут быть
// удалены частично, и вызов вернул ошибку.
type RotationResult struct {
	// Removed — успешно удалённые candidates в oldest→newest порядке.
	Removed []Backup
	// Skipped — сохранённые candidates: retained newest, protected и кандидаты
	// после первого сбоя удаления, которые больше не пытались удалять.
	Skipped []Backup
	// Failed — candidates, чьё удаление завершилось ошибкой (partial result).
	Failed []Backup
}

// rotateOperations — immutable per-call seam ротации. Тесты копируют production
// config и заменяют отдельные function fields; mutable global state не
// используется, внешних полей у структуры нет.
type rotateOperations struct {
	// remove удаляет один файл; os.ErrNotExist означает, что файл уже отсутствует.
	remove func(path string) error
}

// productionRotateOperations заполняет seam прямыми os-операциями.
func productionRotateOperations() rotateOperations {
	return rotateOperations{remove: os.Remove}
}

// Rotate удаляет oldest backups текущего target, сохраняя keep newest копий.
// keep <= 0 заменяется DefaultKeepBackups; keep >= len(candidates) ничего не
// удаляет. protectedIDs защищают точные backup ID: такие копии не удаляются,
// даже если попадают в зону удаления (защита выбранной для rollback копии).
// Ошибка чтения каталога возвращается до любого удаления; ошибка удаления
// помещает candidate в Failed, присоединяется к результату и останавливает
// дальнейшие удаления. Ошибки содержат пути, но не данные backup; target не
// изменяется никогда.
func Rotate(targetPath, prefix, directory string, keep int, protectedIDs []string) (RotationResult, error) {
	return rotateWithOperations(targetPath, prefix, directory, keep, protectedIDs, productionRotateOperations())
}

// rotateWithOperations реализует один вызов ротации с подменяемыми операциями.
// Candidates получены из List, то есть каждый из них — verified regular pair
// текущего canonical target; итерация oldest-first выполняется разворотом
// newest-first списка без дополнительной сортировки.
func rotateWithOperations(targetPath, prefix, directory string, keep int, protectedIDs []string, operations rotateOperations) (RotationResult, error) {
	result := RotationResult{
		Removed: make([]Backup, 0),
		Skipped: make([]Backup, 0),
		Failed:  make([]Backup, 0),
	}
	// Неполный config — программная ошибка; отклоняем до первого удаления.
	if operations.remove == nil {
		return result, fmt.Errorf("rotate operations: nil field remove")
	}

	// Ошибка List означает, что каталог недоступен: удалять нечего и нельзя,
	// потому что candidates не построены.
	candidates, err := List(targetPath, prefix, directory)
	if err != nil {
		return result, err
	}
	if keep <= 0 {
		keep = DefaultKeepBackups
	}

	// Protected set строится только из непустых точных backup ID: partial
	// совпадения имён файлов и пустые значения защитой не считаются.
	protected := make(map[string]struct{}, len(protectedIDs))
	for _, id := range protectedIDs {
		if id != "" {
			protected[id] = struct{}{}
		}
	}

	// candidates newest first: индексы [keep:] — oldest зона удаления.
	// Итерация с конца списка даёт stable oldest→newest порядок результата.
	var rotationErr error
	for i := len(candidates) - 1; i >= 0; i-- {
		backup := candidates[i]
		if i < keep {
			result.Skipped = append(result.Skipped, backup)
			continue
		}
		if _, isProtected := protected[backup.ID]; isProtected {
			result.Skipped = append(result.Skipped, backup)
			continue
		}
		// После первого сбоя удаления дальнейшие попытки запрещены: кандидаты
		// остаются на диске и попадают в Skipped как пропущенные.
		if rotationErr != nil {
			result.Skipped = append(result.Skipped, backup)
			continue
		}
		if err := removeBackupPair(backup, operations.remove); err != nil {
			result.Failed = append(result.Failed, backup)
			rotationErr = errors.Join(rotationErr, err)
			continue
		}
		result.Removed = append(result.Removed, backup)
	}
	return result, rotationErr
}

// removeBackupPair удаляет sidecar и data одного verified candidate. Sidecar
// удаляется первым: scan по каталогу идёт по sidecar-именам, и одиночная data
// без sidecar дешевле, чем orphan sidecar. os.ErrNotExist любого из файлов не
// является сбоем — удаление другого файла всё равно предпринимается, а пара,
// оба файла которой отсутствуют после попыток, считается Removed. Реальная
// ошибка останавливает удаление пары немедленно, чтобы не оставлять её
// половинчатой намеренно; первичная ошибка сохраняется через %w для errors.Is.
func removeBackupPair(backup Backup, remove func(string) error) error {
	if err := remove(backup.MetadataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("удалить sidecar backup %s: %w", backup.MetadataPath, err)
	}
	if err := remove(backup.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("удалить data backup %s: %w", backup.Path, err)
	}
	return nil
}
