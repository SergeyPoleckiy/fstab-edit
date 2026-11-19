// Файл candidate.go реализует статическую проверку содержимого fstab до записи
// в системный файл. Candidate создаётся в отдельном приватном каталоге, а его
// байты не нормализуются и не дополняются newline перед вызовом findmnt.
package sysutil

import (
	"context"
	"os"
	"path/filepath"

	"fstab-edit/internal/parser"
	"fstab-edit/internal/validator"
	"fstab-edit/pkg/models"
)

var (
	// Файловые операции вынесены в неэкспортируемые переменные, чтобы тесты
	// детерминированно проверяли ошибки create/write/cleanup без привилегий,
	// заполнения диска или гонок с реальной файловой системой. Production использует
	// непосредственно стандартные os.MkdirTemp, os.WriteFile и os.RemoveAll.
	candidateMkdirTemp = os.MkdirTemp
	candidateWriteFile = os.WriteFile
	candidateRemoveAll = os.RemoveAll
	// candidateAbs позволяет детерминированно проверить cleanup при ошибке
	// преобразования пути; production использует непосредственно filepath.Abs.
	candidateAbs = filepath.Abs
)

// VerifyCandidate объединяет внутреннюю валидацию и внешнюю проверку candidate.
// Runner получает только findmnt --verify --tab-file с абсолютным путём к
// временному файлу: функция не монтирует ресурсы и никогда не обращается к
// /etc/fstab. Исходный candidate slice не изменяется. После создания private
// directory RemoveAll предпринимается при любом исходе; ошибка cleanup добавляет warning.
func VerifyCandidate(ctx context.Context, runner CommandRunner, candidate []byte) (verification Verification) {
	directory, err := candidateMkdirTemp("", "fstab-edit-candidate-*")
	if err != nil {
		return candidatePreparationFailure(err)
	}
	cleanupDirectory := directory
	defer func() {
		if err := candidateRemoveAll(cleanupDirectory); err != nil {
			// Cleanup warning дополняет первичный Report, но не заменяет его и не
			// перезаписывает Execution.Err исходной проверки или подготовки файла.
			appendFindmntDiagnostic(&verification.Report, "findmnt.candidate_cleanup", models.SeverityWarning,
				"Не удалось удалить временный файл-кандидат после проверки")
		}
	}()

	directory, err = candidateAbs(directory)
	if err != nil {
		return candidatePreparationFailure(err)
	}
	cleanupDirectory = directory
	path := filepath.Join(directory, "candidate.fstab")
	if err := candidateWriteFile(path, candidate, 0o600); err != nil {
		return candidatePreparationFailure(err)
	}

	internal := validator.Validate(parser.Parse(candidate))
	external := NewFindmntVerifier(runner).Verify(ctx, path)
	verification = external
	verification.Report = models.MergeReports(internal, external.Report)
	return verification
}

// candidatePreparationFailure сохраняет техническую ошибку только в Execution.
// Diagnostic намеренно generic: путь temp directory и содержимое candidate не
// должны попадать в безопасный для отображения Report.
func candidatePreparationFailure(err error) Verification {
	report := newFindmntReport()
	appendFindmntDiagnostic(&report, "findmnt.candidate", models.SeverityError,
		"Не удалось подготовить приватный файл-кандидат для проверки")
	return Verification{Report: report, Execution: RunResult{ExitCode: -1, Err: err}}
}
