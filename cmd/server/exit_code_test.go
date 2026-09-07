package main

import (
	"errors"
	"fmt"
	"testing"

	"goodkind.io/tack/internal/ops"
)

// TestExitCodeOfTellsAMarkerFailureFromARestoreFailure pins the status the
// restore drill's unit restarts on. A drill whose legs all passed and whose
// marker did not land exits 3, which the unit is told never to restart, so
// the restores are not repeated to reach the same put; every other failure,
// a failed leg included, still exits 1 and is restarted.
func TestExitCodeOfTellsAMarkerFailureFromARestoreFailure(t *testing.T) {
	markerFailure := &ops.RestoreDrillMarkerError{Err: errors.New("put object tack-backups/backup-status/rehearsal.json: StatusCode: 500")}
	if got := exitCodeOf(markerFailure); got != 3 {
		t.Fatalf("marker failure exit code = %d, want 3", got)
	}
	if got := exitCodeOf(fmt.Errorf("audited: %w", markerFailure)); got != 3 {
		t.Fatalf("a wrapped marker failure must keep its exit code, got %d", got)
	}
	legFailure := errors.New("restore-drill: 1 leg(s) failed: fdb: restore never reached the target")
	if got := exitCodeOf(legFailure); got != 1 {
		t.Fatalf("leg failure exit code = %d, want 1", got)
	}
	// An error carrying another process's exit status is not this process's:
	// a child that exited 3 must not read as the marker failure.
	childExit := &childExitError{code: 3}
	if got := exitCodeOf(fmt.Errorf("run child: %w", childExit)); got != 1 {
		t.Fatalf("a child's exit code leaked into the process exit code: got %d, want 1", got)
	}
}

// childExitError has the ExitCode method shape of a child process's exit
// error, so the test does not need to spawn one.
type childExitError struct {
	code int
}

func (e *childExitError) Error() string { return "exit status 3" }
func (e *childExitError) ExitCode() int { return e.code }
