package tui

import "fstab-edit/internal/i18n"

// Tr возвращает перевод TUI-строки. Ключи живут в общих каталогах под
// префиксом "tui."; отсутствующий ключ возвращается как есть (не panic).
// Каталоги ключей tui.* наполняются на Task 7, поэтому сейчас возвращается
// сам ключ — это ожидаемое поведение скелета.
func Tr(lang, key string) string { return i18n.T(lang, "tui."+key) }

// Trf подставляет аргументы в перевод; аргументы — самодостаточные значения.
func Trf(lang, key string, args ...any) string { return i18n.Tf(lang, "tui."+key, args...) }
