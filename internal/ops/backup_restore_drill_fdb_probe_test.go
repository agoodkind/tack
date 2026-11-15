package ops

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testWaitGuard limits real-time test execution to detect hung probes before
// the test binary's overall timeout.
const testWaitGuard = 10 * time.Second

// hangUntilDone simulates an exec that answers only after context cancellation.
func hangUntilDone(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// awaitOrHang runs monitoring in a goroutine and fails after the guard expires.
func awaitOrHang(t *testing.T, watch fdbRestoreWatch) (*fdbRestoreProgress, int, error) {
	t.Helper()
	type outcome struct {
		progress *fdbRestoreProgress
		exitCode int
		err      error
	}
	returned := make(chan outcome, 1)
	go func() {
		progress, exitCode, err := awaitFDBRestore(
			t.Context(),
			watch,
			testStallWindow,
			testPollInterval,
			testProbeTimeout,
		)
		returned <- outcome{progress: progress, exitCode: exitCode, err: err}
	}()
	select {
	case got := <-returned:
		return got.progress, got.exitCode, got.err
	case <-time.After(testWaitGuard):
		t.Fatal("monitoring blocked on an unresponsive probe instead of detecting its timeout")
		return nil, 0, nil
	}
}

// TestAwaitFDBRestoreBoundsAStatusProbeThatNeverAnswers checks a per-probe
// deadline and an observation failure after consecutive unanswered polls,
// rather than reporting a restore stall without observing one.
func TestAwaitFDBRestoreBoundsAStatusProbeThatNeverAnswers(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status:   hangUntilDone,
	}

	_, _, err := awaitOrHang(t, watch)
	if err == nil {
		t.Fatal("an unresponsive status probe must not pass restore monitoring")
	}

	if !strings.Contains(err.Error(), "could not be observed") {
		t.Fatalf("the failure must report unsuccessful observation, got: %v", err)
	}

	want := "3 consecutive polls had a probe that did not answer within " + testProbeTimeout.String()
	if !strings.Contains(err.Error(), want) {
		t.Fatalf(
			"the failure must report unanswered polls and the probe deadline, want %q in: %v",
			want,
			err,
		)
	}

	if !strings.Contains(err.Error(), errFDBRestoreProbeTimedOut.Error()) {
		t.Fatalf("the failure must include the last probe failure, got: %v", err)
	}
}

// TestAwaitFDBRestoreBoundsAnExitProbeThatNeverAnswers checks exit-probe
// deadlines and omission of status requests after an unanswered exit probe.
func TestAwaitFDBRestoreBoundsAnExitProbeThatNeverAnswers(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	var statusAsked atomic.Int32
	watch := fdbRestoreWatch{
		Finished: func(ctx context.Context) (int, bool, error) {
			_, err := hangUntilDone(ctx)
			return 0, false, err
		},
		Status: func(context.Context) (string, error) {
			statusAsked.Add(1)
			return fdbStatusLine(1, 10, 0, 4096, 0), nil
		},
	}

	_, _, err := awaitOrHang(t, watch)

	if err == nil || !strings.Contains(err.Error(), "could not be observed") {
		t.Fatalf("an unresponsive exit probe must fail as unobservable, got: %v", err)
	}

	if asked := statusAsked.Load(); asked != 0 {
		t.Fatalf("status requests = %d during the unanswered exit probe, want 0", asked)
	}
}

// TestAwaitFDBRestoreOutlivesProbeTimeoutsThatClear checks successful completion
// after fewer consecutive timeouts than fdbDrillProbeTimeoutTolerance.
func TestAwaitFDBRestoreOutlivesProbeTimeoutsThatClear(t *testing.T) {
	const polls = 20
	advanceClockPerRead(t, time.Second)
	var statusReads atomic.Int32
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) {
			return 0, statusReads.Load() >= polls, nil
		},
		Status: func(ctx context.Context) (string, error) {
			read := statusReads.Add(1)
			if read <= fdbDrillProbeTimeoutTolerance-1 {
				return hangUntilDone(ctx)
			}

			return fdbStatusLine(int64(read), polls, 0, int64(read)*4096, 0), nil
		},
	}

	progress, exitCode, err := awaitOrHang(t, watch)
	if err != nil {
		t.Fatalf("probe timeouts under the tolerance must not fail the drill: %v", err)
	}

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(progress.summary(), "byteswritten=81920") {
		t.Fatalf(
			"progress must report completed writes after probe recovery, got %q",
			progress.summary(),
		)
	}
}

// TestAwaitFDBRestoreDoesNotCountATimedOutProbeAsProgress checks that alternating
// timeouts neither exceed consecutive-timeout tolerance nor reset inactivity.
func TestAwaitFDBRestoreDoesNotCountATimedOutProbeAsProgress(t *testing.T) {
	advanceClockPerRead(t, testClockStep)
	var reads atomic.Int32
	wedged := fdbStatusLine(7, 900, 4, 28672, 0)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status: func(ctx context.Context) (string, error) {
			if reads.Add(1)%2 == 1 {
				return hangUntilDone(ctx)
			}

			return wedged, nil
		},
	}

	_, _, err := awaitOrHang(t, watch)
	if err == nil {
		t.Fatal("an inactive restore must fail even when every other probe times out")
	}

	if !strings.Contains(err.Error(), "did not make progress for") {
		t.Fatalf("the failure must report observed inactivity, got: %v", err)
	}
}
