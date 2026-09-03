// shellquote.go turns a value into one POSIX shell word for the container
// programs the ops commands build. Go's %q is Go quoting, not shell quoting:
// it wraps a value in double quotes, which a shell still expands, so a value
// carrying $(...) or a backtick reaches the shell as a command rather than as
// data, and a value carrying a space still splits when it lands outside
// quotes.

package ops

import "strings"

// shellQuote renders value as a single-quoted POSIX shell word. A shell expands
// nothing inside single quotes, so every byte of value reaches the program as
// data. The single quote is the one character that cannot appear there, so each
// one is closed, escaped outside the quotes, and reopened.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
