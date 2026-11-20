package i18n

// catalogRU — русский каталог сообщений: ключ → русский текст.
// RU — язык по умолчанию (Resolve без явных настроек возвращает "ru").
// Порядок ключей следует таблицам Task 3/Task 5 брифа; %s/%q/%v сохраняются.
var catalogRU = map[string]string{
	// --- Ключи cliapp (error.io, error.validation, error.external) ---
	"error.io.read_target":                  "прочитать target %s",
	"error.io.create_backup":                "создать backup target %s",
	"error.io.read_backup":                  "прочитать backup %s",
	"error.io.check_backup":                 "проверить backup %s",
	"error.io.replace_target":               "заменить target %s резервной копией %s",
	"error.io.select_backup":                "выбрать резервную копию",
	"error.io.save_changes":                 "сохранить изменения",
	"error.io.create_safety_backup":         "создать safety backup target %s",
	"error.io.mutation_not_saved":           "мутация не сохранена",
	"error.validation.mutation_blocked":     "кандидат не прошёл внутреннюю проверку (severity=error), изменения не сохранены",
	"error.validation.mutation_not_applied": "мутация не применена",
	"error.validation.document_not_set":     "документ не задан",
	"error.validation.unknown_mutation":     "неизвестный вид мутации",
	"error.validation.selector_ambiguous":   "селектор неоднозначен",
	"error.validation.backup_not_found":     "резервная копия не найдена",
	"error.validation.failed":               "проверка target %s завершилась с диагностиками severity=error",
	"error.external.verify_failed":          "внешняя проверка target %s завершилась с severity=error",
	"error.external.post_rollback":          "post-check target %s после восстановления завершился с severity=error",
	// --- Ключи cliapp/select.go и cliapp/Resolve (Task 4) ---
	"error.usage.selector_required":       "требуется селектор: позиционный аргумент, --line, --mount-point или --device",
	"error.usage.selector_multiple":       "селектор задан несколькими способами: используйте только один из позиционного аргумента, --line, --mount-point, --device",
	"error.usage.bad_line":                "--line требует положительный номер физической строки, получено %q",
	"error.ambiguous.resolve":             "селектор неоднозначен, уточните его или выберите строку",
	"error.ambiguous.choose_failed":       "не удалось выбрать строку из кандидатов",
	"error.ambiguous.out_of_range":        "выбранная строка %d не входит в список кандидатов",
	"error.validation.entry_not_selected": "запись не выбрана",

	// --- Ключи cmd (error.usage, prompt, status, chooser) ---
	"error.usage.unknown_output":   "неизвестный формат вывода %q: допустимы table, json, csv, tsv",
	"error.usage.bad_log_level":    "некорректный --log-level %q: допустимы debug, info, warn, error",
	"error.usage.bad_lang":         "некорректный --lang %q: допустимы ru, en",
	"error.usage.verbose_quiet":    "флаги --verbose и --quiet несовместимы: выберите один режим вывода",
	"error.usage.no_tty":           "подтверждение требует TTY: в неинтерактивном режиме используйте --yes",
	"error.usage.bad_flag":         "некорректный флаг или аргументы: %v",
	"error.usage.add_requires":     "add требует непустой флаг %s",
	"error.usage.edit_no_changes":  "edit требует хотя бы один изменяющий флаг: --device, --mount, --type, --options, --dump, --pass, --comment, --clear-comment, --unset-dump, --unset-pass",
	"error.usage.comment_conflict": "флаги --comment и --clear-comment несовместимы: выберите один",
	"prompt.apply_changes":         "Применить изменения? [y/N] ",
	"prompt.candidates":            "кандидаты: строки %s",
	"status.no_changes":            "изменений нет: target не изменён, backup и lock не создавались",
	"status.cancelled":             "операция отменена пользователем: target не изменён",
	"status.render_failed":         "не удалось сформировать вывод результата: ",
	"chooser.ambiguous_header":     "селектор неоднозначен, выберите строку:",
	"chooser.line_prompt":          "номер строки: ",
}
