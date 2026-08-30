package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAwaitFDBRestoreFailsARestoreThatStopsMoving proves the replacement
// failure mode: a restore that stops advancing fails, and the failure names
// how long nothing moved and where the counters got to.
func TestAwaitFDBRestoreFailsARestoreThatStopsMoving(t *testing.T) {
	advanceClockPerRead(t, testClockStep)
	wedged := fdbStatusLine(7, 900, 4, 28672, 0)
	restore := &scriptedRestore{
		statuses:    []string{wedged},
		finishAfter: 1000,
		exitCode:    0,
		polls:       0,
	}

	_, _, err := awaitFDBRestore(t.Context(), restore.watch(), testStallWindow, testPollInterval)

	if err == nil {
		t.Fatal("a restore that stopped making progress must fail the drill")
	}
	if !strings.Contains(err.Error(), "made no progress for") {
		t.Fatalf("the failure must say the restore stopped, got: %v", err)
	}
	if !strings.Contains(err.Error(), "blocks=7") || !strings.Contains(err.Error(), "byteswritten=28672") {
		t.Fatalf("the failure must name where the counters got to, got: %v", err)
	}
}

// TestAwaitFDBRestoreIgnoresCountersThatCanFall proves only counters that can
// never fall are read as progress. BlocksInProgress rises and falls as blocks
// are dispatched and retired, and ApplyVersionLag falls as the restore catches
// up, so a wedged restore whose only moving fields are those must still fail.
func TestAwaitFDBRestoreIgnoresCountersThatCanFall(t *testing.T) {
	const polls = 60
	advanceClockPerRead(t, testClockStep)
	statuses := make([]string, 0, polls)
	for i := range polls {
		statuses = append(statuses, fdbStatusLine(7, 900, int64(i%9), 28672, int64(polls-i)))
	}
	restore := &scriptedRestore{statuses: statuses, finishAfter: polls, exitCode: 0, polls: 0}

	_, _, err := awaitFDBRestore(t.Context(), restore.watch(), testStallWindow, testPollInterval)

	if err == nil {
		t.Fatal("counters that rise and fall are not evidence the restore moved")
	}
	if !strings.Contains(err.Error(), "made no progress for") {
		t.Fatalf("the failure must say the restore stopped, got: %v", err)
	}
}

// TestAwaitFDBRestoreReportsAStatusItCannotRead proves a status the drill can
// never parse ends as a named stall carrying the read failure, rather than as
// a wait with no bound at all.
func TestAwaitFDBRestoreReportsAStatusItCannotRead(t *testing.T) {
	advanceClockPerRead(t, testClockStep)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status: func(context.Context) (string, error) {
			return "", errors.New("exec inspect: connection refused")
		},
	}

	_, _, err := awaitFDBRestore(t.Context(), watch, testStallWindow, testPollInterval)

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
