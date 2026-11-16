package servercli

import (
	"errors"
	"fmt"
	"testing"

	"goodkind.io/tack/internal/ops"
)

// TestExitCodeOfDistinguishesMarkerAndRestoreFailures checks restart status.
// A marker write failure exits 3 after successful restores. The unit does not
// restart status 3. Other failures exit 1 and cause the unit to restart.
func TestExitCodeOfDistinguishesMarkerAndRestoreFailures(t *testing.T) {
	markerFailure := &ops.RestoreDrillMarkerError{
		Err: errors.New("put object tack-backups/backup-status/rehearsal.json: StatusCode: 500"),
	}
	if got := exitCodeOf(markerFailure); got != 3 {
		t.Fatalf("marker failure exit code = %d, want 3", got)
	}

	if got := exitCodeOf(fmt.Errorf("audited: %w", markerFailure)); got != 3 {
		t.Fatalf("a wrapped marker failure must keep its exit code, got %d", got)
	}

	legFailure := errors.New(
		"restore-drill: 1 leg(s) failed: fdb: restore did not complete at the target version",
	)
	if got := exitCodeOf(legFailure); got != 1 {
		t.Fatalf("leg failure exit code = %d, want 1", got)
	}

	// A child process exit status must not select the marker failure status.
	childExit := &childExitError{code: 3}
	if got := exitCodeOf(fmt.Errorf("run child: %w", childExit)); got != 1 {
		t.Fatalf("process exit code = %d for a child error, want 1", got)
	}
}

// childExitError implements the child process ExitCode contract without
// starting a process.
type childExitError struct {
	code int
}

func (e *childExitError) Error() string { return "exit status 3" }
func (e *childExitError) ExitCode() int { return e.code }
