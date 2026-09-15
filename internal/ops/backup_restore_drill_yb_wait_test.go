package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The window is deliberately not the deployed one, so the wait is exercised
// against a window it does not hardcode. The fake clock moves one minute per
// read, so a run of a few hundred polls covers hours without sleeping.
const (
	ybWaitTestStall     = 7 * time.Minute
	ybWaitTestClockStep = time.Minute
	ybWaitTestPoll      = 5 * time.Second
	ybWaitTestProbe     = time.Second
)

// scriptedYBScratch drives one awaitYBScratch run: the engine stays not ready
// for readyAfter polls, and the counters are handed out in order with the last
// one repeating.
type scriptedYBScratch struct {
	readyAfter  int
	counters    []map[string]int64
	progressErr error
	running     bool
	polls       int
	// failReason, when set, is reported by the failure check from poll
	// failAfter on.
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

// risingTablets renders a restoration landing one more tablet's files per poll.
func risingTablets(polls int) []map[string]int64 {
	counters := make([]map[string]int64, 0, polls)
	for i := range polls {
		counters = append(counters, map[string]int64{"sst_files": int64(i)})
	}
	return counters
}

// TestAwaitYBScratchLetsASlowRestorationRunPastAnyFixedBudget is the defect
// this wait exists for. The restoration below takes four hours of wall clock
// and finishes, because every poll shows more files landed. Under the fixed
// five minute wall the drill used to carry, it failed while it was working.
func TestAwaitYBScratchLetsASlowRestorationRunPastAnyFixedBudget(t *testing.T) {
	const polls = 240
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: polls, counters: risingTablets(polls), running: true}

	took, err := awaitYBScratch(context.Background(), "restore", engine.watch(), ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err != nil {
		t.Fatalf("a restoration that keeps landing files must be waited for: %v", err)
	}
	if took < 3*time.Hour {
		t.Fatalf("took = %s, want a wait that outlived any fixed budget", took)
	}
}

// TestAwaitYBScratchFailsAnEngineThatStoppedMoving proves the other half: an
// engine whose counters froze is reported as stalled after the window, with
// the marks and the step named, and the wait does not run on.
func TestAwaitYBScratchFailsAnEngineThatStoppedMoving(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	frozen := []map[string]int64{{"sst_files": 3, "running_tablets": 9}, {"sst_files": 12, "running_tablets": 9}}
	engine := &scriptedYBScratch{readyAfter: 1000, counters: frozen, running: true}

	_, err := awaitYBScratch(context.Background(), "restore", engine.watch(), ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err == nil {
		t.Fatal("an engine whose counters stopped rising must be reported as stalled")
	}
	for _, want := range []string{"restore:", "no progress for", "running_tablets=9", "sst_files=12"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("stall error %q lacks %q", err, want)
		}
	}
	if engine.polls > 12 {
		t.Fatalf("polls = %d, want the wait to stop soon after the window", engine.polls)
	}
}

// TestAwaitYBScratchIgnoresACounterThatFallsAndRecovers proves a counter is
// read against its high-water mark: a count that drops and climbs back to
// where it was is not movement, so a wedged engine cannot ratchet forward.
func TestAwaitYBScratchIgnoresACounterThatFallsAndRecovers(t *testing.T) {
	const polls = 60
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	counters := make([]map[string]int64, 0, polls)
	for i := range polls {
		counters = append(counters, map[string]int64{"running_tablets": int64(10 + i%3)})
	}
	engine := &scriptedYBScratch{readyAfter: polls, counters: counters, running: true}

	_, err := awaitYBScratch(context.Background(), "start", engine.watch(), ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err == nil || !strings.Contains(err.Error(), "no progress for") {
		t.Fatalf("err = %v, want a stall for a counter that only returns to its mark", err)
	}
}

// TestAwaitYBScratchNamesAnEngineItCouldNotRead keeps a wedged engine apart
// from one the drill never read: the stall carries the last failed read and
// says no counter arrived.
func TestAwaitYBScratchNamesAnEngineItCouldNotRead(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{
		readyAfter: 1000, counters: risingTablets(1), running: true,
		progressErr: errors.New("exec create: context deadline exceeded"),
	}

	_, err := awaitYBScratch(context.Background(), "start", engine.watch(), ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err == nil {
		t.Fatal("an engine the drill cannot read must not be waited for forever")
	}
	for _, want := range []string{"no progress counter was ever readable", "progress read: exec create: context deadline exceeded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("stall error %q lacks %q", err, want)
		}
	}
}

// TestAwaitYBScratchEndsWhenTheContainerExits proves a scratch container that
// died ends the wait at once rather than after the stall window.
func TestAwaitYBScratchEndsWhenTheContainerExits(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 1000, counters: risingTablets(50), running: false}

	_, err := awaitYBScratch(context.Background(), "start", engine.watch(), ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("err = %v, want the exited container named", err)
	}
	if engine.polls != 0 {
		t.Fatalf("polls = %d, want the wait to end before reading progress", engine.polls)
	}
}
