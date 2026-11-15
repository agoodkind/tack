package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAwaitFDBRestoreFailsARestoreThatStopsMoving checks that a stall reports
// the inactivity duration and last counter values.
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

	if !strings.Contains(err.Error(), "did not make progress for") {
		t.Fatalf("the failure must report inactivity, got: %v", err)
	}

	if !strings.Contains(err.Error(), "blocks=7") ||
		!strings.Contains(err.Error(), "byteswritten=28672") {
		t.Fatalf("the failure must report the last counter values, got: %v", err)
	}
}

// TestAwaitFDBRestoreIgnoresBlocksInProgress checks that an oscillating
// BlocksInProgress counter cannot prevent stall detection.
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
		t.Fatal("an oscillating counter must not indicate restore progress")
	}

	if !strings.Contains(err.Error(), "did not make progress for") {
		t.Fatalf("the failure must report inactivity, got: %v", err)
	}
}

// TestAwaitFDBRestoreCountsAFallingApplyVersionLagAsProgress checks that
// decreasing lag prevents a stall while block and byte counts remain constant
// during mutation log application, even across days of simulated time.
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
		t.Fatalf("a restore still applying its mutation log must not count as stalled: %v", err)
	}

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(progress.summary(), "applyversionlag=1") {
		t.Fatalf("progress must report the minimum lag, got %q", progress.summary())
	}
}

// TestAwaitFDBRestoreIgnoresAnApplyVersionLagThatClimbs checks that lag
// oscillation above the initial minimum does not prevent stall detection.
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
		t.Fatal("lag above the recorded minimum must not indicate restore progress")
	}

	if !strings.Contains(err.Error(), "did not make progress for") {
		t.Fatalf("the failure must report inactivity, got: %v", err)
	}
}

// TestAwaitFDBRestoreReportsAStatusItCannotRead checks that unreadable status
// ends in a stall with the last read failure rather than an unbounded wait.
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
		t.Fatal("unreadable status must not cause an unbounded wait")
	}

	if !strings.Contains(
		err.Error(),
		"fdbrestore status never reported readable progress counters",
	) {
		t.Fatalf("the failure must report unreadable counters, got: %v", err)
	}

	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the failure must include the last status read error, got: %v", err)
	}
}
