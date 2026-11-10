// The diagnostic parser combines ysqlsh output lines before classifying a
// roles-file apply. The first line supplies the file location, severity,
// SQLSTATE, and message start. Under VERBOSITY=verbose, libpq prints labeled
// fields after the message and ends them with LOCATION.
//
// The server inserts role names without escaping newlines. A duplicate-role
// message can continue on bare lines without a prefix, label, or indentation.
// Classifying each line independently would reject the first fragment and
// discard the rest of that name.
//
// The original investigation captured these forms from
// yugabytedb/yugabyte:2025.2.3.0-b149 on September 1, 2026. It used
// ybRolesApplyEnv and a file-based apply. Every captured message,
// including notices, began with the file location. Each of the eleven labels
// below appeared on its own line in English. CONTEXT could continue on bare
// lines. A duplicate role with a newline produced this output:
//
//	ysqlsh:/artifacts/roles.sql:17: ERROR:  42710: role "first
//	second" already exists
//	LOCATION:  CreateRole, user.c:319
//
// Two consecutive newlines in a role name produced an empty message line.

package ops

import (
	"regexp"
	"strings"
)

// ybDiagnosticPattern matches a verbose diagnostic's opening severity token,
// five-character SQLSTATE, and first message line. It captures the code and
// message without interpreting the localized severity.
//
// The original investigation checked
// yugabytedb/yugabyte:2025.2.3.0-b149 on August 30, 2026. lc_messages=C
// produced
// "ERROR:  42710: role ..." and lc_messages=de_DE.utf8 produced
// "FEHLER:  42710: Rolle ...". Notices retained code 00000 with localized
// "NOTICE:  00000: ..." and "HINWEIS:  00000: ..." severity text.
var ybDiagnosticPattern = regexp.MustCompile(`(?:^|\s)[^\s:]+:\s+([0-9A-Z]{5}):\s+(.*)$`)

// ybDiagnosticLabelPattern recognizes libpq's verbose field labels in the
// client C locale that ybRolesApplyEnv selects. Labels end the server message.
// LINE starts a syntax-cursor display with a line number instead of a second
// space. Unlabeled lines after a field label continue that field, such as
// CONTEXT or a cursor caret, and do not continue the server message.
var ybDiagnosticLabelPattern = regexp.MustCompile(
	`^(?:DETAIL|HINT|QUERY|CONTEXT|SCHEMA NAME|TABLE NAME|COLUMN NAME|DATATYPE NAME|CONSTRAINT NAME|LOCATION):  |^LINE [0-9]+: `)

// ybRenderedDiagnostic records one ysqlsh diagnostic after combining its
// output lines.
type ybRenderedDiagnostic struct {
	// Line preserves the diagnostic's exact opening line.
	Line string
	// SQLState records the five-character code from the opening line. It remains
	// empty when a roles-file line lacks a code, including output without verbose
	// diagnostics.
	SQLState string
	// Message combines every server-message line with its original newlines.
	Message string
}

// ybRenderedDiagnostics parses apply stderr into diagnostics. A SQLSTATE line
// opens a message. A roles-file line without a code records a diagnostic
// without an open message. Client field labels end the open message. Other
// lines, including empty lines, continue an open message; unrelated lines
// outside a message do not enter the result.
//
// The parser cannot distinguish a role-name continuation that matches a client
// field label or file location from that client output. It truncates the
// message, and the classifier rejects the unexpected result. This ambiguity
// must cause a failed drill.
func ybRenderedDiagnostics(stderr, rolesFilePath string) []ybRenderedDiagnostic {
	var diagnostics []ybRenderedDiagnostic
	open := -1
	// psql ends each message with a newline. The final newline terminates the
	// last message instead of adding an empty line inside it.
	for line := range strings.SplitSeq(strings.TrimSuffix(stderr, "\n"), "\n") {
		if match := ybDiagnosticPattern.FindStringSubmatch(line); match != nil {
			diagnostics = append(diagnostics,
				ybRenderedDiagnostic{Line: line, SQLState: match[1], Message: match[2]})
			open = len(diagnostics) - 1
			continue
		}
		if strings.Contains(line, rolesFilePath+":") {
			diagnostics = append(diagnostics,
				ybRenderedDiagnostic{Line: line, SQLState: "", Message: ""})
			open = -1
			continue
		}
		if open < 0 {
			continue
		}
		if ybDiagnosticLabelPattern.MatchString(line) {
			open = -1
			continue
		}
		diagnostics[open].Message += "\n" + line
	}
	return diagnostics
}
