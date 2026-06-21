package main

import (
	"strings"
	"unicode"
)

// sanitizeForLog replaces control characters (including CR and LF) with spaces
// so untrusted values cannot forge or split log records. The audit key loader
// logs errors derived from an operator-supplied filesystem path, which gosec
// flags as a log-injection sink (G706); routing those strings through this
// function neutralizes the injection vector before they reach a log handler.
func sanitizeForLog(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
