// shellquote.go quotes values for container shell commands. Go's %q uses
// double quotes, which permit shell expansion of $(...) and backticks.
// Unquoted spaces split a value into separate shell words.

package ops

import "strings"

// shellQuote renders value as a single-quoted POSIX shell word. The shell
// does not expand text inside single quotes. Each embedded single quote
// requires closing the quoted text, escaping the quote, and reopening it.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
