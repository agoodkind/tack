package main

import (
	"errors"
	"fmt"
	"testing"

	"goodkind.io/tack/internal/ops"
)

// TestExitCodeOfTellsAMarkerFailureFromARestoreFailure checks that a restore
// drill returns status 3 when every restore leg passed but the marker write
// failed, and status 1 for a failed restore leg. The distinct status lets
// the caller avoid repeating successful restores just to retry the marker.
func TestExitCodeOfTellsAMarkerFailureFromARestoreFailure(t *testing.T) {
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
		"restore-drill: 1 leg(s) failed: fdb: restore never reached the target",
	)
	if got := exitCodeOf(legFailure); got != 1 {
		t.Fatalf("leg failure exit code = %d, want 1", got)
	}

	// A child process's status 3 must not set this process's status or indicate
	// a restore marker failure.
	childExit := &childExitError{code: 3}
	if got := exitCodeOf(fmt.Errorf("run child: %w", childExit)); got != 1 {
		t.Fatalf("a child's exit code leaked into the process exit code: got %d, want 1", got)
	}
}

// childExitError implements a child process error's ExitCode method without
// starting a child process.
type childExitError struct {
	code int
}

func (e *childExitError) Error() string { return "exit status 3" }
func (e *childExitError) ExitCode() int { return e.code }
