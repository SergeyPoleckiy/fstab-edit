// Package i18n предоставляет каталоги сообщений RU/EN и выбор языка по
// приоритету ТЗ §15: --lang > FSTAB_EDIT_LANG > LC_ALL > LANG > ru.
// Отсутствующий перевод возвращает English, затем сам ключ — panic
// исключён. Коды, enum values и JSON keys не переводятся и здесь не живут.
package i18n

import (
	"fmt"
	"strings"
)

// catalogs — единственный источник переводов: catalogs[lang][key].
var catalogs = map[string]map[string]string{
	"en": catalogEN,
	"ru": catalogRU,
}

// IsValidLanguage сообщает, поддерживается ли язык.
func IsValidLanguage(lang string) bool {
	_, ok := catalogs[lang]
	return ok
}

// Languages возвращает поддерживаемые языки в стабильном порядке.
func Languages() []string { return []string{"en", "ru"} }

// Resolve выбирает язык. Невалидное значение на любом уровне игнорируется
// без ошибки — переход к следующему уровню; default — "ru".
func Resolve(explicit string, environ []string, lcAll, langEnv string) string {
	if IsValidLanguage(explicit) {
		return explicit
	}
	for _, pair := range environ {
		name, value, ok := strings.Cut(pair, "=")
		if ok && name == "FSTAB_EDIT_LANG" && IsValidLanguage(value) {
			return value
		}
	}
	for _, locale := range []string{lcAll, langEnv} {
		if lang := languageFromLocale(locale); IsValidLanguage(lang) {
			return lang
		}
	}
	return "ru"
}

// languageFromLocale извлекает язык из locale вида "ru_RU.UTF-8", "en",
// "C"/"POSIX" (последние — пустой результат).
func languageFromLocale(locale string) string {
	base, _, _ := strings.Cut(locale, ".")
	base = strings.ReplaceAll(base, "-", "_")
	lang, _, _ := strings.Cut(base, "_")
	return lang
}

// T возвращает перевод ключа: lang → en → сам ключ. Паника исключена,
// неизвестный ключ виден в выводе как есть — это безопаснее молчаливой
// пустой строки при диагностике сбоев.
func T(lang, key string) string {
	if c, ok := catalogs[lang]; ok {
		if msg, ok := c[key]; ok {
			return msg
		}
	}
	if msg, ok := catalogs["en"][key]; ok {
		return msg
	}
	return key
}

// Tf подставляет аргументы в перевод. Аргументы должны быть самодостаточными
// значениями (пути, ID, имена флагов), не фразами: порядок %s фиксирован
// ключом, что допустимо для всех текущих сообщений.
func Tf(lang, key string, args ...any) string {
	return fmt.Sprintf(T(lang, key), args...)
}

// yesAnswers — документированные localized формы подтверждения (ТЗ §15).
// Default — No: любая строка вне списка отклоняется.
var yesAnswers = map[string]map[string]struct{}{
	"en": {"y": {}, "yes": {}},
	"ru": {"y": {}, "yes": {}, "д": {}, "да": {}},
}

// AcceptYes сообщает, является ли ответ localized формой «да».
func AcceptYes(lang, answer string) bool {
	forms, ok := yesAnswers[lang]
	if !ok {
		forms = yesAnswers["en"]
	}
	_, ok = forms[strings.ToLower(strings.TrimSpace(answer))]
	return ok
}
