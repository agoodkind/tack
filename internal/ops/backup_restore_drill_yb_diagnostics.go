// backup_restore_drill_yb_diagnostics.go puts the diagnostics ysqlsh prints
// back together before the roles-apply classification reads them.
//
// ysqlsh prints one diagnostic as several lines, and not every line is the
// client's own. The first line is the file locus, the severity token, the
// SQLSTATE, and the start of the server's message; then come the fields libpq
// prints under VERBOSITY=verbose, each opened by a label on its own line and
// ended by LOCATION. The server puts a role's name into the duplicate-role
// message unescaped, and the client prints the message as it is, so a name
// that carries a newline splits that one diagnostic across lines, with the
// rest of the name following bare: no prefix, no label, no indent. Classifying
// line by line sees a first fragment it cannot tolerate and a rest it drops.
//
// Every shape here was captured live from yugabytedb/yugabyte:2025.2.3.0-b149
// on 2026-09-01 under the environment ybRolesApplyEnv pins, with the apply
// reading a file: every psql message, notices included, opened with the locus;
// each of the eleven labels below appeared on its own line in English; a
// multi-line CONTEXT continued on bare lines of its own; and a duplicate role
// whose name held a newline rendered as
//
//	ysqlsh:/artifacts/roles.sql:17: ERROR:  42710: role "first
//	second" already exists
//	LOCATION:  CreateRole, user.c:319
//
// with a name holding two consecutive newlines putting an empty line inside the
// message.

package ops

import (
	"regexp"
	"strings"
)

// ybDiagnosticPattern matches the line that opens a server diagnostic under
// VERBOSITY=verbose: a severity token, the five-character SQLSTATE, then the
// first line of the message. The severity token is matched only to reach the
// code behind it and is never read, because the server localizes it; the code
// and the message are captured. Verified live against
// yugabytedb/yugabyte:2025.2.3.0-b149 on 2026-08-30, which renders
// "ERROR:  42710: role ..." under lc_messages=C and
// "FEHLER:  42710: Rolle ..." under lc_messages=de_DE.utf8, and puts the same
// code on a notice ("NOTICE:  00000: ...", "HINWEIS:  00000: ...").
var ybDiagnosticPattern = regexp.MustCompile(`(?:^|\s)[^\s:]+:\s+([0-9A-Z]{5}):\s+(.*)$`)

// ybDiagnosticLabelPattern matches a line the client opens with one of its own
// field labels, which is where the server's message ends. These are the labels
// libpq prints under VERBOSITY=verbose, in the C locale ybRolesApplyEnv pins
// on the client; LINE opens the syntax-cursor display and carries the line
// number rather than a second space. A line after a label that carries no
// label of its own continues that field (a multi-line CONTEXT, the cursor's
// caret line), never the message.
var ybDiagnosticLabelPattern = regexp.MustCompile(
	`^(?:DETAIL|HINT|QUERY|CONTEXT|SCHEMA NAME|TABLE NAME|COLUMN NAME|DATATYPE NAME|CONSTRAINT NAME|LOCATION):  |^LINE [0-9]+: `)

// ybRenderedDiagnostic is one diagnostic as ysqlsh printed it, reassembled
// from the lines it spanned.
type ybRenderedDiagnostic struct {
	// Line is the line that opened the diagnostic, verbatim.
	Line string
	// SQLState is the five-character code the opening line carried, or ""
	// when the line named the roles file and carried none, which is how an
	// apply that did not run under the verbose verbosity shows up.
	SQLState string
	// Message is the server's message with every line the client printed
	// for it, joined by the newlines the server put there.
	Message string
}

// ybRenderedDiagnostics reads the apply's stderr into the diagnostics it
// rendered. A line opens a diagnostic when it carries a SQLSTATE, or when it
// names the roles file without one; a line the client labels ends the open
// message; any other line that follows an open message is part of it, empty
// lines included, because the message is the only text on stderr that can
// carry one. A line that follows no open message and is none of those is not
// the apply's to report, and is dropped.
//
// A name whose own line begins with a label word, or with the file locus,
// cannot be told from the client's own line and is cut short here, so that
// name fails the drill as unexpected: the direction the classification is
// meant to err in.
func ybRenderedDiagnostics(stderr, rolesFilePath string) []ybRenderedDiagnostic {
	var diagnostics []ybRenderedDiagnostic
	open := -1
	// psql ends every message with one newline, so the last one terminates
	// the last message rather than opening an empty line inside it.
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
