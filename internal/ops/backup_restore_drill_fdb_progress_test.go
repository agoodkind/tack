package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Test timings differ from deployment constants to detect hardcoded limits.
// Simulated time advances half an hour per read; probe deadlines use real time.
const (
	testStallWindow  = 90 * time.Minute
	testClockStep    = 30 * time.Minute
	testPollInterval = time.Millisecond
	testProbeTimeout = 5 * time.Millisecond
)

// fdbStatusLine reproduces FoundationDB 7.4.6 progress status formatting.
func fdbStatusLine(blocksDone, blocksTotal, blocksInProgress, bytesWritten, applyLag int64) string {
	return fmt.Sprintf(
		"Tag: default  UID: 3a4b  State: running  Blocks: %d/%d  BlocksInProgress: %d  "+
			"Files: 12  BytesWritten: %d  ApplyVersionLag: %d  LastError: ''",
		blocksDone,
		blocksTotal,
		blocksInProgress,
		bytesWritten,
		applyLag,
	)
}

// scriptedRestore supplies ordered status readings, repeats the last, and
// reports completion after finishAfter polls.
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

// advanceClockPerRead advances each opsNow result by step without sleeping.
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

// TestAwaitFDBRestoreContinuesWhileCountersIncrease checks completion after
// days of simulated restore work without an overall execution deadline.
func TestAwaitFDBRestoreContinuesWhileCountersIncrease(t *testing.T) {
	const polls = 200
	advanceClockPerRead(t, testClockStep)

	statuses := make([]string, 0, polls)
	for i := range polls {
		statuses = append(statuses, fdbStatusLine(int64(i), polls, 4, int64(i)*4096, 0))
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
		t.Fatalf("increasing counters must permit continued restore work: %v", err)
	}

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(progress.summary(), "byteswritten=815104") {
		t.Fatalf("progress must report completed writes, got %q", progress.summary())
	}
}

// TestAwaitFDBRestoreCountsTheGrowingBlockTotalAsProgress checks file
// enumeration progress before any block completes.
func TestAwaitFDBRestoreCountsTheGrowingBlockTotalAsProgress(t *testing.T) {
	const polls = 40
	advanceClockPerRead(t, testClockStep)

	statuses := make([]string, 0, polls)
	for i := range polls {
		statuses = append(statuses, fdbStatusLine(0, int64(i)*1000, 0, 0, 0))
	}

	restore := &scriptedRestore{statuses: statuses, finishAfter: polls, exitCode: 0, polls: 0}

	_, _, err := awaitFDBRestore(
		t.Context(),
		restore.watch(),
		testStallWindow,
		testPollInterval,
		testProbeTimeout,
	)
	if err != nil {
		t.Fatalf("file enumeration must not count as inactivity: %v", err)
	}
}

// TestAwaitFDBRestoreReturnsTheRestoresExitCode checks a failed restore's
// own exit status rather than reporting an unrelated stall.
func TestAwaitFDBRestoreReturnsTheRestoresExitCode(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	restore := &scriptedRestore{
		statuses:    []string{fdbStatusLine(1, 10, 0, 4096, 0)},
		finishAfter: 3,
		exitCode:    2,
		polls:       0,
	}

	_, exitCode, err := awaitFDBRestore(
		t.Context(),
		restore.watch(),
		testStallWindow,
		testPollInterval,
		testProbeTimeout,
	)
	if err != nil {
		t.Fatalf("awaitFDBRestore: %v", err)
	}

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want the restore's own 2", exitCode)
	}
}
