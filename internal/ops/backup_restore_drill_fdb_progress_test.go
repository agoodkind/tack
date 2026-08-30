package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// testStallWindow, testClockStep, and testPollInterval keep a scripted run fast
// in real time while the fake clock moves half an hour per read. The window is
// deliberately not the deployed one, so the wait is exercised against a window
// it does not hardcode.
const (
	testStallWindow  = 90 * time.Minute
	testClockStep    = 30 * time.Minute
	testPollInterval = time.Millisecond
)

// fdbStatusLine renders one `fdbrestore status` reading in the exact shape
// FoundationDB 7.4.6 prints, so the parser is exercised against the real
// format rather than a convenient one.
func fdbStatusLine(blocksDone, blocksTotal, blocksInProgress, bytesWritten, applyLag int64) string {
	return fmt.Sprintf(
		"Tag: default  UID: 3a4b  State: running  Blocks: %d/%d  BlocksInProgress: %d  "+
			"Files: 12  BytesWritten: %d  ApplyVersionLag: %d  LastError: ''",
		blocksDone, blocksTotal, blocksInProgress, bytesWritten, applyLag)
}

// scriptedRestore drives one awaitFDBRestore run: statuses are handed out in
// order and the last one repeats, and the restore reports itself finished
// after finishAfter polls.
type scriptedRestore struct {
	statuses    []string
	finishAfter int
	exitCode    int
	polls       int
}

func (s *scriptedRestore) watch() fdbRestoreWatch {
	return fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) {
			if s.polls >= s.finishAfter {
				return s.exitCode, true, nil
			}
			return 0, false, nil
		},
		Status: func(context.Context) (string, error) {
			status := s.statuses[min(s.polls, len(s.statuses)-1)]
			s.polls++
			return status, nil
		},
	}
}

// advanceClockPerRead makes every opsNow call jump by step, so a scripted run
// of a few hundred polls covers days of wall time without sleeping.
func advanceClockPerRead(t *testing.T, step time.Duration) {
	t.Helper()
	current := time.Unix(0, 0).UTC()
	original := nowFunc
	nowFunc = func() time.Time {
		current = current.Add(step)
		return current
	}
	t.Cleanup(func() { nowFunc = original })
}

// TestAwaitFDBRestoreLetsALargeRestoreRunPastAnyFixedBudget is the defect this
// file exists for. The restore below runs for days of wall-clock time and
// finishes cleanly because it never stops writing bytes. Under the fixed
// `timeout 1800` the drill used to wrap fdbrestore in, it would have been
// killed for being large, failing the drill and pinning the rehearsal
// staleness alarm on with nothing wrong.
func TestAwaitFDBRestoreLetsALargeRestoreRunPastAnyFixedBudget(t *testing.T) {
	const polls = 200
	advanceClockPerRead(t, testClockStep)
	statuses := make([]string, 0, polls)
	for i := range polls {
		statuses = append(statuses, fdbStatusLine(int64(i), polls, 4, int64(i)*4096, 0))
	}
	restore := &scriptedRestore{statuses: statuses, finishAfter: polls, exitCode: 0, polls: 0}

	progress, exitCode, err := awaitFDBRestore(t.Context(), restore.watch(), testStallWindow, testPollInterval)
	if err != nil {
		t.Fatalf("a restore that keeps making progress must not be failed for its size: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(progress.summary(), "byteswritten=815104") {
		t.Fatalf("progress must carry what the restore wrote, got %q", progress.summary())
	}
}

// TestAwaitFDBRestoreCountsTheGrowingBlockTotalAsProgress proves the quiet
// phase before the first block lands is not read as a stall. While the backup
// agent enumerates the backup's files, only the block total moves.
func TestAwaitFDBRestoreCountsTheGrowingBlockTotalAsProgress(t *testing.T) {
	const polls = 40
	advanceClockPerRead(t, testClockStep)
	statuses := make([]string, 0, polls)
	for i := range polls {
		statuses = append(statuses, fdbStatusLine(0, int64(i)*1000, 0, 0, 0))
	}
	restore := &scriptedRestore{statuses: statuses, finishAfter: polls, exitCode: 0, polls: 0}

	_, _, err := awaitFDBRestore(t.Context(), restore.watch(), testStallWindow, testPollInterval)
	if err != nil {
		t.Fatalf("a restore still enumerating its files must not read as stalled: %v", err)
	}
}

// TestAwaitFDBRestoreReturnsTheRestoresExitCode proves a restore that fails on
// its own terms is reported as such rather than as a stall.
func TestAwaitFDBRestoreReturnsTheRestoresExitCode(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	restore := &scriptedRestore{
		statuses:    []string{fdbStatusLine(1, 10, 0, 4096, 0)},
		finishAfter: 3,
		exitCode:    2,
		polls:       0,
	}

	_, exitCode, err := awaitFDBRestore(t.Context(), restore.watch(), testStallWindow, testPollInterval)
	if err != nil {
		t.Fatalf("awaitFDBRestore: %v", err)
	}
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want the restore's own 2", exitCode)
	}
}
