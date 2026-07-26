// Package textsafe makes untrusted text safe to print in a terminal.
package textsafe

import (
	"fmt"
	"strings"
	"unicode"
)

// Sanitize preserves line breaks for structured prose while replacing tabs and
// non-printing runes with inert text.
func Sanitize(value string) string {
	return sanitize(value, true)
}

// SingleLine makes value safe for a single terminal line. Line breaks and all
// other non-printing runes are rendered as visible escape sequences.
func SingleLine(value string) string {
	return sanitize(value, false)
}

func sanitize(value string, preserveNewlines bool) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, character := range value {
		switch {
		case character == '\n' && preserveNewlines:
			result.WriteByte('\n')
		case character == '\t' && preserveNewlines:
			result.WriteString("    ")
		case unicode.IsPrint(character):
			result.WriteRune(character)
		default:
			writeEscape(&result, character)
		}
	}
	return result.String()
}

func writeEscape(result *strings.Builder, character rune) {
	switch character {
	case '\n':
		result.WriteString(`\n`)
	case '\r':
		result.WriteString(`\r`)
	case '\t':
		result.WriteString(`\t`)
	default:
		_, _ = fmt.Fprintf(result, `\u{%x}`, character)
	}
}
