package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAwaitFDBRestoreFailsARestoreThatStopsMoving checks the stall failure
// that replaced the fixed total timeout. The expected error reports the
// inactivity duration and the last block and byte counts.
//
// The test checks for "made no progress for" and the counter values. It does
// not assert a numeric duration.
func TestAwaitFDBRestoreFailsARestoreThatStopsMoving(t *testing.T) {
	advanceClockPerRead(t, testClockStep)
	wedged := fdbStatusLine(7, 900, 4, 28672, 0)
	restore := &scriptedRestore{
		statuses:    []string{wedged},
		finishAfter: 1000,
		exitCode:    0,
		polls:       0,
	}

	_, _, err := awaitFDBRestore(
		t.Context(),
		restore.watch(),
		testStallWindow,
		testPollInterval,
		testProbeTimeout,
	)
	if err == nil {
		t.Fatal("a restore that stopped making progress must fail the drill")
	}

	if !strings.Contains(err.Error(), "made no progress for") {
		t.Fatalf("the failure must say the restore stopped, got: %v", err)
	}

	if !strings.Contains(err.Error(), "blocks=7") ||
		!strings.Contains(err.Error(), "byteswritten=28672") {
		t.Fatalf("the failure must name where the counters got to, got: %v", err)
	}
}

// TestAwaitFDBRestoreIgnoresBlocksInProgress checks that changes in
// BlocksInProgress do not prevent a stall. The engine increments and
// decrements that field as it dispatches and finishes blocks. A restore that
// changes only that field must still fail the drill.
func TestAwaitFDBRestoreIgnoresBlocksInProgress(t *testing.T) {
	const polls = 60
	advanceClockPerRead(t, testClockStep)

	statuses := make([]string, 0, polls)
	for i := range polls {
		statuses = append(statuses, fdbStatusLine(7, 900, int64(i%9), 28672, 0))
	}

	restore := &scriptedRestore{statuses: statuses, finishAfter: polls, exitCode: 0, polls: 0}

	_, _, err := awaitFDBRestore(
		t.Context(),
		restore.watch(),
		testStallWindow,
		testPollInterval,
		testProbeTimeout,
	)
	if err == nil {
		t.Fatal("a field that rises and falls is not evidence the restore moved")
	}

	if !strings.Contains(err.Error(), "made no progress for") {
		t.Fatalf("the failure must say the restore stopped, got: %v", err)
	}
}

// TestAwaitFDBRestoreCountsAFallingApplyVersionLagAsProgress checks progress
// during mutation-log application while completed blocks and written bytes
// stay fixed. A rising-only check previously stopped a healthy large restore
// after its final phase exceeded the inactivity window.
//
// The scripted restore represents days of elapsed time with decreasing lag.
// The test requires successful completion and the final lag in the summary.
func TestAwaitFDBRestoreCountsAFallingApplyVersionLagAsProgress(t *testing.T) {
	const polls = 200
	advanceClockPerRead(t, testClockStep)

	statuses := make([]string, 0, polls)
	for i := range polls {
		statuses = append(statuses, fdbStatusLine(900, 900, 0, 28672, int64(polls-i)))
	}

	restore := &scriptedRestore{statuses: statuses, finishAfter: polls, exitCode: 0, polls: 0}

	progress, exitCode, err := awaitFDBRestore(
		t.Context(),
		restore.watch(),
		testStallWindow,
		testPollInterval,
		testProbeTimeout,
	)
	if err != nil {
		t.Fatalf("a restore still applying its mutation log must not read as stalled: %v", err)
	}

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(progress.summary(), "applyversionlag=1") {
		t.Fatalf("progress must carry how far the lag came down, got %q", progress.summary())
	}
}

// TestAwaitFDBRestoreIgnoresAnApplyVersionLagThatClimbs checks that only a
// new minimum counts as falling-counter progress. The scripted lag oscillates
// and ends above its initial value. Its first reading establishes the minimum;
// later readings cannot reset the timer, and the test requires a stall.
func TestAwaitFDBRestoreIgnoresAnApplyVersionLagThatClimbs(t *testing.T) {
	const polls = 60
	advanceClockPerRead(t, testClockStep)

	statuses := make([]string, 0, polls)
	for i := range polls {
		statuses = append(statuses, fdbStatusLine(7, 900, 0, 28672, int64(100+i%7)))
	}

	restore := &scriptedRestore{statuses: statuses, finishAfter: polls, exitCode: 0, polls: 0}

	_, _, err := awaitFDBRestore(
		t.Context(),
		restore.watch(),
		testStallWindow,
		testPollInterval,
		testProbeTimeout,
	)
	if err == nil {
		t.Fatal("a lag that climbs back up is not evidence the restore moved")
	}

	if !strings.Contains(err.Error(), "made no progress for") {
		t.Fatalf("the failure must say the restore stopped, got: %v", err)
	}
}

// TestAwaitFDBRestoreReportsAStatusItCannotRead checks a status-read failure
// that persists until the stall window expires. The error must report that
// the drill never read a counter and include the last status-read failure.
func TestAwaitFDBRestoreReportsAStatusItCannotRead(t *testing.T) {
	advanceClockPerRead(t, testClockStep)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status: func(context.Context) (string, error) {
			return "", errors.New("exec inspect: connection refused")
		},
	}

	_, _, err := awaitFDBRestore(
		t.Context(),
		watch,
		testStallWindow,
		testPollInterval,
		testProbeTimeout,
	)
	if err == nil {
		t.Fatal("a status the drill can never read must not wait forever")
	}

	if !strings.Contains(err.Error(), "no progress counters were ever readable") {
		t.Fatalf("the failure must say no counter was ever read, got: %v", err)
	}

	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the failure must carry the last status read failure, got: %v", err)
	}
}
