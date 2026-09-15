package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAwaitYBScratchEndsOnACrashLoopThatKeepsWritingBytes is the defect the
// failure check exists for. yugabyted restarts a crashed master inside a
// container that stays up, and each restart writes more bytes, so the counters
// alone would read the loop as progress until the unit's timeout killed it.
func TestAwaitYBScratchEndsOnACrashLoopThatKeepsWritingBytes(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{
		readyAfter: 1000, counters: risingTablets(1000), running: true,
		failReason: "yugabyted restarted a crashed master or tablet server 1 time(s)", failAfter: 3,
	}

	_, err := awaitYBScratch(context.Background(), "start", engine.watch(), ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err == nil || !strings.Contains(err.Error(), "start: yugabyted restarted a crashed master") {
		t.Fatalf("err = %v, want the restart named", err)
	}
	if engine.polls != 3 {
		t.Fatalf("polls = %d, want the wait to end at the first failure reading", engine.polls)
	}
}

// TestAwaitYBScratchSurvivesAProbeThatNeverAnswers proves each probe runs
// under its own deadline: a progress read that blocks until its context ends
// returns as a failed read, the stall check still runs, and the stall names
// the read that did not answer.
func TestAwaitYBScratchSurvivesAProbeThatNeverAnswers(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	watch := ybScratchWatch{
		Running: func(context.Context) (bool, error) { return true, nil },
		Failed:  func(context.Context) (string, error) { return "", nil },
		Ready:   func(context.Context) (bool, error) { return false, nil },
		Progress: func(ctx context.Context) (map[string]int64, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := awaitYBScratch(context.Background(), "start", watch, ybWaitTestStall, ybWaitTestPoll, 10*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "progress read: context deadline exceeded") {
			t.Fatalf("err = %v, want a stall naming the probe deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a probe that never answers must not hold the wait past its deadline")
	}
}

// TestAwaitYBScratchKeepsWaitingThroughAFailedInspect proves an inspect that
// errors is a failed read, not a dead container: the engine that keeps moving
// is still waited for until it is ready.
func TestAwaitYBScratchKeepsWaitingThroughAFailedInspect(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 30, counters: risingTablets(30), running: true}
	watch := engine.watch()
	watch.Running = func(context.Context) (bool, error) {
		return false, errors.New("inspect tack-rtyb: connection refused")
	}

	if _, err := awaitYBScratch(context.Background(), "start", watch, ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe); err != nil {
		t.Fatalf("a failed inspect must not end the wait: %v", err)
	}
}

// TestAwaitYBScratchEndsWhenItCannotCheckForFailure proves the failure check
// is load bearing: while it cannot be read, a crash loop's bytes would keep
// resetting the stall timer, so a wait that stays blind gives up instead of
// running to the unit's backstop.
func TestAwaitYBScratchEndsWhenItCannotCheckForFailure(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 1000, counters: risingTablets(1000), running: true}
	watch := engine.watch()
	watch.Failed = func(context.Context) (string, error) {
		return "", errors.New("count yugabyted restarts: exec create: no such file")
	}

	_, err := awaitYBScratch(context.Background(), "start", watch, ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err == nil {
		t.Fatal("a wait that cannot check for failure must not run on")
	}
	for _, want := range []string{"could not check whether the scratch yugabyted failed", "3 polls in a row", "exec create: no such file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if engine.polls > 3 {
		t.Fatalf("polls = %d, want the wait to end at the tolerance", engine.polls)
	}
}

// TestAwaitYBScratchForgetsARecoveredReadError proves the stall names only a
// read that was still failing: a probe that failed once and then answered
// must not be reported as the reason a later stall happened.
func TestAwaitYBScratchForgetsARecoveredReadError(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 1000, counters: risingTablets(1), running: true}
	watch := engine.watch()
	watch.Ready = func(context.Context) (bool, error) {
		if engine.polls == 0 {
			return false, errors.New("exec create: no such container")
		}
		return false, nil
	}

	_, err := awaitYBScratch(context.Background(), "start", watch, ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err == nil || !strings.Contains(err.Error(), "no progress for") {
		t.Fatalf("err = %v, want a stall", err)
	}
	if strings.Contains(err.Error(), "no such container") {
		t.Fatalf("stall %q names a read that recovered", err)
	}
}

// TestAwaitYBScratchCarriesAReadinessErrorIntoTheStall keeps a readiness check
// that errors apart from one that answered no: the stall message names it.
func TestAwaitYBScratchCarriesAReadinessErrorIntoTheStall(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 1000, counters: risingTablets(1), running: true}
	watch := engine.watch()
	watch.Ready = func(context.Context) (bool, error) {
		return false, errors.New("exec create: no such container")
	}

	_, err := awaitYBScratch(context.Background(), "start", watch, ybWaitTestStall, ybWaitTestPoll, ybWaitTestProbe)
	if err == nil || !strings.Contains(err.Error(), "readiness check: exec create: no such container") {
		t.Fatalf("err = %v, want the readiness error in the stall", err)
	}
}
