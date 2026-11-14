// Package backfill validates the removal dates of temporary commands.
package backfill

import (
	"fmt"
	"io"
	"os"
	"time"

	"goodkind.io/tack/internal/clock"
)

const scanFailureExitCode = 2

// Run checks backfill declarations using the current UTC date.
//
// It reads the working directory and writes diagnostics to standard error.
// Check determines the exit code from the declarations and any scan error.
//
// A command entry point can use Run directly:
//
//	os.Exit(Run())
func Run() int {
	return Check(clock.Now().UTC(), os.Stderr)
}

// Check validates backfill declarations under the working directory.
//
// It writes diagnostics to out and compares removal dates with today.
// Test files and hidden directories do not participate in the scan.
//
// Check returns these exit codes:
//   - 0 means every declaration passes.
//   - 1 means a declaration is invalid or has expired.
//   - 2 means the scanner could not read or parse a file.
func Check(today time.Time, out io.Writer) int {
	problems, err := scanTree(today)
	if err != nil {
		fmt.Fprintf(out, "backfillcheck: %v\n", err)

		return scanFailureExitCode
	}

	for _, problem := range problems {
		fmt.Fprintln(out, problem)
	}

	if len(problems) > 0 {
		return 1
	}

	return 0
}
