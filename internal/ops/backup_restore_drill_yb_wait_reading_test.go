package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAwaitYBScratchEndsOnACrashLoopThatKeepsWritingBytes checks the failure
// reason that must stop a wait even while counters increase. yugabyted can
// restart a crashed master inside a running container and write more bytes
// on each restart. Counters alone could treat that loop as progress until
// the unit timeout. The failure check runs before the progress read. The
// test requires termination after three progress reads and before another
// progress read.
func TestAwaitYBScratchEndsOnACrashLoopThatKeepsWritingBytes(t *testing.T) {
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
		t.Fatalf("err = %v, want the restart named", err)
	}

	if engine.polls != 3 {
		t.Fatalf("polls = %d, want the wait to end at the first failure reading", engine.polls)
	}
}

// TestAwaitYBScratchSurvivesAProbeThatNeverAnswers checks a progress callback
// that waits for its context deadline and returns that error. The wait must
// still check inactivity and return a stall that includes the failed read.
// The other callbacks return immediately in this test.
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
			t.Fatalf("err = %v, want a stall naming the probe deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a probe that never answers must not hold the wait past its deadline")
	}
}

// TestAwaitYBScratchKeepsWaitingThroughAFailedInspect checks that an inspect
// error counts as a failed read rather than proof of container exit. The
// scripted engine keeps reporting progress and must finish when ready.
func TestAwaitYBScratchKeepsWaitingThroughAFailedInspect(t *testing.T) {
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
		t.Fatalf("a failed inspect must not end the wait: %v", err)
	}
}

// TestAwaitYBScratchEndsWhenItCannotCheckForFailure checks that consecutive
// failure-check errors end the wait at the tolerance despite rising counters.
// Crash-loop writes could otherwise reset inactivity until the unit backstop
// timeout. The error must identify the unreadable check and its cause.
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
		t.Fatal("a wait that cannot check for failure must not run on")
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
		t.Fatalf("polls = %d, want the wait to end at the tolerance", engine.polls)
	}
}

// TestAwaitYBScratchForgetsARecoveredReadError checks that a readiness probe
// error from an earlier poll does not appear in a later stall after that
// probe recovers.
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

	_, err := awaitYBScratch(
		context.Background(),
		"start",
		watch,
		ybWaitTestStall,
		ybWaitTestPoll,
		ybWaitTestProbe,
	)
	if err == nil || !strings.Contains(err.Error(), "no progress for") {
		t.Fatalf("err = %v, want a stall", err)
	}

	if strings.Contains(err.Error(), "no such container") {
		t.Fatalf("stall %q names a read that recovered", err)
	}
}

// TestAwaitYBScratchCarriesAReadinessErrorIntoTheStall checks that a readiness
// error remains distinct from a successful false result. The stall must
// include the persistent readiness-check error.
func TestAwaitYBScratchCarriesAReadinessErrorIntoTheStall(t *testing.T) {
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
