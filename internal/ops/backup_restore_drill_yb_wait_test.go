package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These test timings differ from deployment values to detect hardcoded limits.
// Simulated time advances one minute per read without real-time delays.
const (
	ybWaitTestStall     = 7 * time.Minute
	ybWaitTestClockStep = time.Minute
	ybWaitTestPoll      = 5 * time.Second
	ybWaitTestProbe     = time.Second
)

// scriptedYBScratch supplies ordered counters, repeating the last, and reports
// readiness after readyAfter polls.
type scriptedYBScratch struct {
	readyAfter  int
	counters    []map[string]int64
	progressErr error
	running     bool
	polls       int
	// failReason reports a permanent failure from failAfter onward.
	failReason string
	failAfter  int
}

func (s *scriptedYBScratch) watch() ybScratchWatch {
	return ybScratchWatch{
		Running: func(context.Context) (bool, error) { return s.running, nil },
		Failed: func(context.Context) (string, error) {
			if s.failReason != "" && s.polls >= s.failAfter {
				return s.failReason, nil
			}

			return "", nil
		},
		Ready: func(context.Context) (bool, error) {
			return s.polls >= s.readyAfter, nil
		},
		Progress: func(context.Context) (map[string]int64, error) {
			counters := s.counters[min(s.polls, len(s.counters)-1)]
			s.polls++
			if s.progressErr != nil {
				return nil, s.progressErr
			}

			return counters, nil
		},
	}
}

// risingTablets supplies one additional SST file per poll.
func risingTablets(polls int) []map[string]int64 {
	counters := make([]map[string]int64, 0, polls)
	for i := range polls {
		counters = append(counters, map[string]int64{"sst_files": int64(i)})
	}

	return counters
}

// TestAwaitYBScratchContinuesWhileCountersIncrease checks a restoration lasting
// hours of simulated time without an overall completion deadline.
func TestAwaitYBScratchContinuesWhileCountersIncrease(t *testing.T) {
	const polls = 240
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: polls, counters: risingTablets(polls), running: true}

	took, err := awaitYBScratch(
		context.Background(),
		"restore",
		engine.watch(),
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err != nil {
		t.Fatalf("monitoring must continue while the restoration writes files: %v", err)
	}

	if took < 3*time.Hour {
		t.Fatalf("took = %s, want at least three simulated hours", took)
	}
}

// TestAwaitYBScratchFailsAfterInactivity checks stall timing, step context, and
// final maxima when engine counters stop increasing.
func TestAwaitYBScratchFailsAfterInactivity(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	frozen := []map[string]int64{
		{"sst_files": 3, "running_tablets": 9},
		{"sst_files": 12, "running_tablets": 9},
	}
	engine := &scriptedYBScratch{readyAfter: 1000, counters: frozen, running: true}

	_, err := awaitYBScratch(
		context.Background(),
		"restore",
		engine.watch(),
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil {
		t.Fatal("unchanged engine counters must produce a stall failure")
	}

	for _, want := range []string{
		"restore:",
		"did not make progress for",
		"running_tablets=9",
		"sst_files=12",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("stall error %q lacks %q", err, want)
		}
	}

	if engine.polls > 12 {
		t.Fatalf("polls = %d, monitoring must stop soon after the inactivity window", engine.polls)
	}
}

// TestAwaitYBScratchIgnoresCounterOscillation checks that returning to a prior
// maximum does not reset inactivity monitoring.
func TestAwaitYBScratchIgnoresCounterOscillation(t *testing.T) {
	const polls = 60
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)

	counters := make([]map[string]int64, 0, polls)
	for i := range polls {
		counters = append(counters, map[string]int64{"running_tablets": int64(10 + i%3)})
	}

	engine := &scriptedYBScratch{readyAfter: polls, counters: counters, running: true}

	_, err := awaitYBScratch(
		context.Background(),
		"start",
		engine.watch(),
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil || !strings.Contains(err.Error(), "did not make progress for") {
		t.Fatalf("err = %v, want a stall for counter oscillation", err)
	}
}

// TestAwaitYBScratchReportsUnreadableCounters checks a bounded failure with
// explicit unreadable counters and the final failed read.
func TestAwaitYBScratchReportsUnreadableCounters(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{
		readyAfter:  1000,
		counters:    risingTablets(1),
		running:     true,
		progressErr: errors.New("exec create: context deadline exceeded"),
	}

	_, err := awaitYBScratch(
		context.Background(),
		"start",
		engine.watch(),
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil {
		t.Fatal("unreadable engine status must not cause an unbounded wait")
	}

	for _, want := range []string{
		"scratch yugabyted never reported readable progress counters",
		"progress read: exec create: context deadline exceeded",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("stall error %q lacks %q", err, want)
		}
	}
}

// TestAwaitYBScratchEndsWhenTheContainerExits checks termination before
// reading progress from a stopped container.
func TestAwaitYBScratchEndsWhenTheContainerExits(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 1000, counters: risingTablets(50), running: false}

	_, err := awaitYBScratch(
		context.Background(),
		"start",
		engine.watch(),
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("err = %v, want the stopped container identifier", err)
	}

	if engine.polls != 0 {
		t.Fatalf("polls = %d, want termination before progress reads", engine.polls)
	}
}
