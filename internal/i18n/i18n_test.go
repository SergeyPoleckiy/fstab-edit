// Package i18n предоставляет каталоги сообщений RU/EN и выбор языка по
// приоритету ТЗ §15. Тесты фиксируют приоритет Resolve, поведение T/Tf при
// отсутствующих ключах и локализованные формы подтверждения AcceptYes.
package i18n

import (
	"strings"
	"testing"
)

// contains — локальный helper: strings.Contains для читаемых assert-сообщений.
func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestResolvePriority фиксирует приоритет выбора языка:
// --lang > FSTAB_EDIT_LANG > LC_ALL > LANG > default "ru". Невалидные значения
// на каждом уровне игнорируются и управление переходит на следующий уровень.
func TestResolvePriority(t *testing.T) {
	cases := []struct {
		name, explicit string
		environ        []string
		lcAll, langEnv string
		want           string
	}{
		{"explicit wins", "en", []string{"FSTAB_EDIT_LANG=ru"}, "ru_RU.UTF-8", "ru_RU.UTF-8", "en"},
		{"env beats locale", "", []string{"FSTAB_EDIT_LANG=en"}, "ru_RU.UTF-8", "ru_RU.UTF-8", "en"},
		{"lc_all beats lang", "", nil, "en_US.UTF-8", "ru_RU.UTF-8", "en"},
		{"lang env", "", nil, "", "en_GB.UTF-8", "en"},
		{"ru locale", "", nil, "ru_RU.UTF-8", "", "ru"},
		{"default ru", "", nil, "", "", "ru"},
		{"invalid explicit falls through", "de", []string{"FSTAB_EDIT_LANG=en"}, "", "", "en"},
		{"invalid env falls through", "", []string{"FSTAB_EDIT_LANG=de"}, "", "en_US", "en"},
		{"posix empty locale ignored", "", nil, "C", "POSIX", "ru"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.explicit, tc.environ, tc.lcAll, tc.langEnv); got != tc.want {
				t.Fatalf("Resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTAndTf проверяет: ru/en переводы различаются; Tf подставляет аргументы;
// отсутствующий ключ возвращает сам ключ и не паникует (оба языка).
func TestTAndTf(t *testing.T) {
	if T("ru", "error.io.read_target") == T("en", "error.io.read_target") {
		t.Fatal("ru/en совпали")
	}
	if got := Tf("ru", "error.io.read_target", "/x"); !contains(got, "/x") {
		t.Fatalf("args: %q", got)
	}
	if got := T("ru", "no.such.key"); got != "no.such.key" {
		t.Fatalf("fallback: %q", got)
	}
	if got := T("en", "no.such.key"); got != "no.such.key" {
		t.Fatalf("en fallback: %q", got)
	}
}

// TestAcceptYes фиксирует localized формы «да» для обоих языков. Default —
// No: любая строка вне списка (включая «нет» и смешанные формы) отклоняется.
func TestAcceptYes(t *testing.T) {
	yes := map[string][]string{
		"ru": {"y", "Y", "yes", "д", "Д", "да", "ДА"},
		"en": {"y", "Y", "yes", "YES"},
	}
	for lang, answers := range yes {
		for _, a := range answers {
			if !AcceptYes(lang, a) {
				t.Fatalf("%s: %q должен быть yes", lang, a)
			}
		}
	}
	for _, a := range []string{"", "n", "no", "нет", "дд", "absolutely", "даdа"} {
		if AcceptYes("ru", a) || AcceptYes("en", a) {
			// "нет" в обоих языках — No; "даdа" (смесь) не принимается.
			t.Fatalf("%q не должен приниматься", a)
		}
	}
}
