package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAwaitYBScratchStopsAfterCrashRestart checks that failure detection stops
// crash loops even when their file writes increase progress counters.
func TestAwaitYBScratchStopsAfterCrashRestart(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{
		readyAfter: 1000,
		counters:   risingTablets(1000),
		running:    true,
		failReason: "yugabyted restarted a crashed master or tablet server 1 time(s)",
		failAfter:  3,
	}

	_, err := awaitYBScratch(
		context.Background(),
		"start",
		engine.watch(),
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil || !strings.Contains(err.Error(), "start: yugabyted restarted a crashed master") {
		t.Fatalf("err = %v, want the restart diagnostic", err)
	}

	if engine.polls != 3 {
		t.Fatalf("polls = %d, want termination at the first failure reading", engine.polls)
	}
}

// TestAwaitYBScratchBoundsUnresponsiveProgressProbe checks a probe deadline
// and a subsequent stall identifying the unanswered progress read.
func TestAwaitYBScratchBoundsUnresponsiveProgressProbe(t *testing.T) {
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
		_, err := awaitYBScratch(
			context.Background(),
			"start",
			watch,
			ybWaitTestStall,
			ybWaitTestPoll,
			10*time.Millisecond,
		)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil ||
			!strings.Contains(err.Error(), "progress read: context deadline exceeded") {
			t.Fatalf("err = %v, want a stall reporting the probe deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an unresponsive probe must not block monitoring past its deadline")
	}
}

// TestAwaitYBScratchContinuesAfterInspectFailure checks that a failed inspect
// does not imply container exit when other probes still establish progress.
func TestAwaitYBScratchContinuesAfterInspectFailure(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 30, counters: risingTablets(30), running: true}
	watch := engine.watch()
	watch.Running = func(context.Context) (bool, error) {
		return false, errors.New("inspect tack-rtyb: connection refused")
	}

	if _, err := awaitYBScratch(
		context.Background(),
		"start",
		watch,
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	); err != nil {
		t.Fatalf("a failed inspect must not end monitoring: %v", err)
	}
}

// TestAwaitYBScratchEndsWhenItCannotCheckForFailure checks bounded failure
// after consecutive unreadable failure probes despite increasing counters.
func TestAwaitYBScratchEndsWhenItCannotCheckForFailure(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 1000, counters: risingTablets(1000), running: true}
	watch := engine.watch()
	watch.Failed = func(context.Context) (string, error) {
		return "", errors.New("count yugabyted restarts: exec create: no such file")
	}

	_, err := awaitYBScratch(
		context.Background(),
		"start",
		watch,
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil {
		t.Fatal("unreadable failure probes must not permit indefinite monitoring")
	}

	for _, want := range []string{
		"could not check whether the scratch yugabyted failed",
		"3 polls in a row",
		"exec create: no such file",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	if engine.polls > 3 {
		t.Fatalf("polls = %d, want termination at the tolerance", engine.polls)
	}
}

// TestAwaitYBScratchClearsRecoveredReadError checks that a recovered probe
// failure does not appear as the reason for later inactivity.
func TestAwaitYBScratchClearsRecoveredReadError(t *testing.T) {
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

	_, err := awaitYBScratch(
		context.Background(),
		"start",
		watch,
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil || !strings.Contains(err.Error(), "did not make progress for") {
		t.Fatalf("err = %v, want a stall", err)
	}

	if strings.Contains(err.Error(), "no such container") {
		t.Fatalf("stall %q reports a recovered read error", err)
	}
}

// TestAwaitYBScratchIncludesReadinessErrorInStall distinguishes unsuccessful
// readiness from a readiness probe that failed to return a valid answer.
func TestAwaitYBScratchIncludesReadinessErrorInStall(t *testing.T) {
	advanceClockPerRead(t, ybWaitTestClockStep)
	recordLedgerWaits(t)
	engine := &scriptedYBScratch{readyAfter: 1000, counters: risingTablets(1), running: true}
	watch := engine.watch()
	watch.Ready = func(context.Context) (bool, error) {
		return false, errors.New("exec create: no such container")
	}

	_, err := awaitYBScratch(
		context.Background(),
		"start",
		watch,
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "readiness check: exec create: no such container") {
		t.Fatalf("err = %v, want the readiness error in the stall", err)
	}
}
