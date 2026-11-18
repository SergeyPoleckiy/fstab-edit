// Package parser содержит преобразования между decoded значениями и raw
// представлением fstab. Функции не обращаются к файлам и внешним утилитам.
package parser

import (
	"errors"
	"strings"
)

// ErrInvalidField означает, что decoded значение нельзя поместить в одно
// поле fstab без потери данных или создания дополнительной физической строки.
var ErrInvalidField = errors.New("поле содержит недопустимый управляющий символ")

// EncodeField экранирует separators и backslash трёхзначными octal escapes.
// Начальный '#' также экранируется, чтобы поле не превратило строку в комментарий.
// NUL, LF и CR отклоняются: NUL запрещён ТЗ, а line endings создали бы новую
// строку. Остальные байты, включая UTF-8, сохраняются без нормализации.
func EncodeField(value string) (string, error) {
	var encoded strings.Builder
	encoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case ' ':
			encoded.WriteString(`\040`)
		case '\t':
			encoded.WriteString(`\011`)
		case '\\':
			encoded.WriteString(`\134`)
		case '#':
			if index == 0 {
				encoded.WriteString(`\043`)
			} else {
				encoded.WriteByte(value[index])
			}
		case 0, '\n', '\r':
			return "", ErrInvalidField
		default:
			encoded.WriteByte(value[index])
		}
	}
	return encoded.String(), nil
}
