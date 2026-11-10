package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// testStallWindow, testClockStep, and testPollInterval keep scripted runs
// short in real time while the test clock advances half an hour per read.
// The test window differs from the deployment window to detect a hardcoded
// limit. testProbeTimeout uses real time to end unanswered probes.
const (
	testStallWindow  = 90 * time.Minute
	testClockStep    = 30 * time.Minute
	testPollInterval = time.Millisecond
	testProbeTimeout = 5 * time.Millisecond
)

// fdbStatusLine renders the FoundationDB 7.4.6 fdbrestore status format.
// The tests exercise that format instead of an input tailored to the parser.
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

// scriptedRestore supplies statuses in order and repeats the last one.
// It reports completion after finishAfter polls of one awaitFDBRestore run.
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

// advanceClockPerRead advances each opsNow call by step. A few hundred
// scripted polls can represent days without sleeping for that duration.
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

// TestAwaitFDBRestoreLetsALargeRestoreRunPastAnyFixedBudget checks the
// earlier fixed-budget defect. The scripted restore represents days of
// elapsed time and completes while continually writing bytes. The previous
// timeout 1800 wrapper would have killed a restore of that duration, failed
// the drill, and kept the rehearsal staleness alarm active despite progress.
func TestAwaitFDBRestoreLetsALargeRestoreRunPastAnyFixedBudget(t *testing.T) {
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
		t.Fatalf("a restore that keeps making progress must not be failed for its size: %v", err)
	}

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(progress.summary(), "byteswritten=815104") {
		t.Fatalf("progress must carry what the restore wrote, got %q", progress.summary())
	}
}

// TestAwaitFDBRestoreCountsTheGrowingBlockTotalAsProgress checks progress
// before the first block completes. While the backup agent lists backup
// files, only the total block count increases; the wait must not report a
// stall during that phase.
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
		t.Fatalf("a restore still enumerating its files must not read as stalled: %v", err)
	}
}

// TestAwaitFDBRestoreReturnsTheRestoresExitCode checks that the wait reports
// the restore command's own failure instead of reporting a stall.
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
