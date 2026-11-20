package logger

import (
	"regexp"
	"strings"
)

// credentialKeys маскирует value чувствительных ключей вида key=value,
// где разделителем пары служит '=', а пары перечислены через запятую либо
// '&' (query string). Ключи фиксированы и не зависят от пользовательского
// ввода — заменяется только value.
var credentialKeys = regexp.MustCompile(
	`(?i)\b(credentials|password|passwd|token|secret|username|user|pass)=([^,&\s]+)`)

// urlUserinfoPattern маскирует пароль в user:pass@ — пароль заменяется
// независимо от содержимого, user сохраняется для диагностики.
var urlUserinfoPattern = regexp.MustCompile(`(://[^:/\s]*):([^@\s]+)@`)

// Redact готовит строку для журнала: экранирует управляющие символы,
// чтобы одно событие оставалось одной строкой, и маскирует секреты
// (пароли, токены, credentials, userinfo URL). Порядок: сначала маскировка
// по исходным границам, затем escaping — маскировка не должна ломаться
// экранированием обратных слешей.
func Redact(s string) string {
	s = urlUserinfoPattern.ReplaceAllString(s, "$1:***@")
	s = credentialKeys.ReplaceAllString(s, "$1=***")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
