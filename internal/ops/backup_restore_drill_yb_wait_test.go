package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The test window differs from the implementation's window to check that the
// wait uses its supplied duration. The simulated clock advances one minute
// per read; hundreds of polls represent hours without sleeping for that time.
const (
	ybWaitTestStall     = 7 * time.Minute
	ybWaitTestClockStep = time.Minute
	ybWaitTestPoll      = 5 * time.Second
	ybWaitTestProbe     = time.Second
)

// scriptedYBScratch supplies one awaitYBScratch run. Readiness stays false
// until readyAfter progress reads. Counter readings follow their supplied
// order and repeat the last value after the sequence ends.
type scriptedYBScratch struct {
	readyAfter  int
	counters    []map[string]int64
	progressErr error
	running     bool
	polls       int
	// The failure check returns a nonempty failReason starting at failAfter.
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

// risingTablets supplies an sst_files count that increases by one per poll,
// representing restoration of another tablet's files.
func risingTablets(polls int) []map[string]int64 {
	counters := make([]map[string]int64, 0, polls)
	for i := range polls {
		counters = append(counters, map[string]int64{"sst_files": int64(i)})
	}

	return counters
}

// TestAwaitYBScratchLetsASlowRestorationRunPastAnyFixedBudget checks the
// fixed-budget defect. The original scenario claimed a four-hour restoration
// with more files on every poll. The former five-minute limit stopped active
// restoration.
//
// The test requires success and at least three hours of simulated elapsed
// time. The clock advances on every opsNow call, including more than one
// call in a progressing iteration.
func TestAwaitYBScratchLetsASlowRestorationRunPastAnyFixedBudget(t *testing.T) {
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
		t.Fatalf("a restoration that keeps landing files must be waited for: %v", err)
	}

	if took < 3*time.Hour {
		t.Fatalf("took = %s, want a wait that outlived any fixed budget", took)
	}
}

// TestAwaitYBScratchFailsAnEngineThatStoppedMoving checks that fixed counters
// produce a stall. The error must include the step and maxima, and the wait
// must stop within the test's poll limit after the inactivity window.
func TestAwaitYBScratchFailsAnEngineThatStoppedMoving(t *testing.T) {
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
		t.Fatal("an engine whose counters stopped rising must be reported as stalled")
	}

	for _, want := range []string{
		"restore:",
		"no progress for",
		"running_tablets=9",
		"sst_files=12",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("stall error %q lacks %q", err, want)
		}
	}

	if engine.polls > 12 {
		t.Fatalf("polls = %d, want the wait to stop soon after the window", engine.polls)
	}
}

// TestAwaitYBScratchIgnoresACounterThatFallsAndRecovers checks that readings
// within a previous maximum do not keep resetting the timer. A count that
// decreases and returns to its maximum must still produce a stall.
func TestAwaitYBScratchIgnoresACounterThatFallsAndRecovers(t *testing.T) {
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
	if err == nil || !strings.Contains(err.Error(), "no progress for") {
		t.Fatalf("err = %v, want a stall for a counter that only returns to its mark", err)
	}
}

// TestAwaitYBScratchNamesAnEngineItCouldNotRead checks a persistent progress
// read failure. The stall must include that error and report that the wait
// never read a counter, distinguishing failed observation from stopped work.
func TestAwaitYBScratchNamesAnEngineItCouldNotRead(t *testing.T) {
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
		t.Fatal("an engine the drill cannot read must not be waited for forever")
	}

	for _, want := range []string{
		"no progress counter was ever readable",
		"progress read: exec create: context deadline exceeded",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("stall error %q lacks %q", err, want)
		}
	}
}

// TestAwaitYBScratchEndsWhenTheContainerExits checks that a stopped container
// ends the wait before a progress read rather than after the stall window.
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
		t.Fatalf("err = %v, want the exited container named", err)
	}

	if engine.polls != 0 {
		t.Fatalf("polls = %d, want the wait to end before reading progress", engine.polls)
	}
}
