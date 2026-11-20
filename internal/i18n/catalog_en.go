package i18n

// catalogEN — английский каталог сообщений: ключ → английский текст.
// Используется для lang == "en" и как fallback T() при отсутствующем
// переводе в ru. Ключи и порядок — зеркально catalogRU.
var catalogEN = map[string]string{
	// --- Ключи cliapp (error.io, error.validation, error.external) ---
	"error.io.read_target":                  "read target %s",
	"error.io.create_backup":                "create backup of target %s",
	"error.io.read_backup":                  "read backup %s",
	"error.io.check_backup":                 "verify backup %s",
	"error.io.replace_target":               "replace target %s with backup %s",
	"error.io.select_backup":                "select backup",
	"error.io.save_changes":                 "save changes",
	"error.io.create_safety_backup":         "create safety backup of target %s",
	"error.io.mutation_not_saved":           "mutation not saved",
	"error.validation.mutation_blocked":     "candidate failed internal validation (severity=error), changes not saved",
	"error.validation.mutation_not_applied": "mutation not applied",
	"error.validation.document_not_set":     "document is nil",
	"error.validation.unknown_mutation":     "unknown mutation kind",
	"error.validation.selector_ambiguous":   "selector is ambiguous",
	"error.validation.backup_not_found":     "backup not found",
	"error.validation.failed":               "target %s validation finished with severity=error diagnostics",
	"error.external.verify_failed":          "external check of target %s finished with severity=error",
	"error.external.post_rollback":          "post-check of target %s after restore finished with severity=error",
	// --- Ключи cliapp/select.go и cliapp/Resolve (Task 4) ---
	"error.usage.selector_required":       "selector required: positional argument, --line, --mount-point or --device",
	"error.usage.selector_multiple":       "selector given in multiple ways: use only one of positional argument, --line, --mount-point, --device",
	"error.usage.bad_line":                "--line requires a positive physical line number, got %q",
	"error.ambiguous.resolve":             "selector is ambiguous, refine it or choose a line",
	"error.ambiguous.choose_failed":       "failed to choose a line from the candidates",
	"error.ambiguous.out_of_range":        "selected line %d is not among the candidates",
	"error.validation.entry_not_selected": "entry not selected",

	// --- Ключи cmd (error.usage, prompt, status, chooser) ---
	"error.usage.unknown_output":   "unknown output format %q: valid values are table, json, csv, tsv",
	"error.usage.bad_log_level":    "invalid --log-level %q: valid values are debug, info, warn, error",
	"error.usage.bad_lang":         "invalid --lang %q: valid values are ru, en",
	"error.usage.verbose_quiet":    "flags --verbose and --quiet are mutually exclusive",
	"error.usage.no_tty":           "confirmation requires a TTY: use --yes in non-interactive mode",
	"error.usage.bad_flag":         "invalid flag or arguments: %v",
	"error.usage.add_requires":     "add requires a non-empty %s flag",
	"error.usage.edit_no_changes":  "edit requires at least one modifying flag",
	"error.usage.comment_conflict": "flags --comment and --clear-comment are mutually exclusive",
	"prompt.apply_changes":         "Apply changes? [y/N] ",
	"prompt.candidates":            "candidates: lines %s",
	"status.no_changes":            "no changes: target unchanged, no backup or lock created",
	"status.cancelled":             "operation cancelled by user: target unchanged",
	"status.render_failed":         "failed to render result output: ",
	"chooser.ambiguous_header":     "selector is ambiguous, choose a line:",
	"chooser.line_prompt":          "line number: ",
}
