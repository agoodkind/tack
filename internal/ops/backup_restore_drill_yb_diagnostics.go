// ysqlsh diagnostics can span several lines, including bare lines and empty
// lines inside role names. Classification reconstructs the complete server
// message before checking duplicate-role errors. Verbose output starts with
// the file location, severity, SQLSTATE, and message. Client field labels end
// the message; LOCATION ends the diagnostic. QA verified these forms with
// yugabytedb/yugabyte:2025.2.3.0-b149 on 2026-09-01 under ybRolesApplyEnv.
// For example, a duplicate role with a newline produces:
//
//	ysqlsh:/artifacts/roles.sql:17: ERROR:  42710: role "first
//	second" already exists
//	LOCATION:  CreateRole, user.c:319

package ops

import (
	"regexp"
	"strings"
)

// ybDiagnosticPattern captures SQLSTATE and the first message line after a
// localized severity token. Classification ignores severity text. QA verified
// ERROR and FEHLER for 42710 and NOTICE and HINWEIS for 00000 on
// yugabytedb/yugabyte:2025.2.3.0-b149 on 2026-08-30 under lc_messages=C and
// lc_messages=de_DE.utf8.
var ybDiagnosticPattern = regexp.MustCompile(`(?:^|\s)[^\s:]+:\s+([0-9A-Z]{5}):\s+(.*)$`)

// ybDiagnosticLabelPattern matches client field labels under VERBOSITY=verbose
// in the C locale from ybRolesApplyEnv. LINE includes the source line number.
// Bare continuation lines after a label belong to that field, not the server
// message, including multiline CONTEXT and syntax cursor lines.
var ybDiagnosticLabelPattern = regexp.MustCompile(
	`^(?:DETAIL|HINT|QUERY|CONTEXT|SCHEMA NAME|TABLE NAME|COLUMN NAME|DATATYPE NAME|CONSTRAINT NAME|LOCATION):  |^LINE [0-9]+: `,
)

// ybRenderedDiagnostic reconstructs one diagnostic from ysqlsh output.
type ybRenderedDiagnostic struct {
	// Line preserves the opening line verbatim.
	Line string
	// SQLState records the five-character code, or an empty string when the
	// roles-file diagnostic omits verbose output.
	SQLState string
	// Message preserves server message lines and their separating newlines.
	Message string
}

// ybRenderedDiagnostics reconstructs stderr messages. SQLSTATE or a roles-file
// location starts a diagnostic; a client field label ends its message. Bare
// and empty lines continue an open message. Other output does not belong to
// the apply diagnostic and does not enter classification.
//
// A role name can mimic a client label or file location. Classification cannot
// distinguish those lines and rejects the truncated message as unexpected
// rather than accepting a potentially different error.
func ybRenderedDiagnostics(stderr, rolesFilePath string) []ybRenderedDiagnostic {
	var diagnostics []ybRenderedDiagnostic
	open := -1
	// The final newline terminates the last message rather than adding an
	// empty line to its body.
	for line := range strings.SplitSeq(strings.TrimSuffix(stderr, "\n"), "\n") {
		if match := ybDiagnosticPattern.FindStringSubmatch(line); match != nil {
			diagnostics = append(
				diagnostics,
				ybRenderedDiagnostic{Line: line, SQLState: match[1], Message: match[2]},
			)
			open = len(diagnostics) - 1
			continue
		}

		if strings.Contains(line, rolesFilePath+":") {
			diagnostics = append(
				diagnostics,
				ybRenderedDiagnostic{Line: line, SQLState: "", Message: ""},
			)
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
