package ops

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testWaitGuard limits the real time for a scripted wait. These runs should
// finish in milliseconds. A hung probe must fail within seconds instead of
// waiting for the test binary's timeout.
const testWaitGuard = 10 * time.Second

// hangUntilDone waits until its context ends before returning. It models a
// hung container exec that returns when cancellation closes its stream.
func hangUntilDone(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// awaitOrHang runs the wait in a separate goroutine and fails the test if
// the guard expires before the wait returns.
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
		t.Fatal("the wait hung on a probe that never answered, so the stall check never ran")
		return nil, 0, nil
	}
}

// TestAwaitFDBRestoreBoundsAStatusProbeThatNeverAnswers checks the earlier
// defect in which a hung container exec blocked the wait before its stall
// check. The drill could then wait indefinitely without applying a limit.
// Each probe now has a deadline. An unanswered probe does not establish
// progress, and the wait reports the unanswered polls instead of claiming
// that it observed a restore stall.
func TestAwaitFDBRestoreBoundsAStatusProbeThatNeverAnswers(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status:   hangUntilDone,
	}

	_, _, err := awaitOrHang(t, watch)
	if err == nil {
		t.Fatal("a restore whose status probe never answers must not pass")
	}

	if !strings.Contains(err.Error(), "could not be observed") {
		t.Fatalf("the failure must say the drill could not observe the restore, got: %v", err)
	}

	want := "3 consecutive polls had a probe that did not answer within " + testProbeTimeout.String()
	if !strings.Contains(err.Error(), want) {
		t.Fatalf(
			"the failure must count the blind polls and name the probe deadline, want %q in: %v",
			want,
			err,
		)
	}

	if !strings.Contains(err.Error(), errFDBRestoreProbeTimedOut.Error()) {
		t.Fatalf("the failure must carry why the last probe failed, got: %v", err)
	}
}

// TestAwaitFDBRestoreBoundsAnExitProbeThatNeverAnswers checks the exit
// probe's deadline and verifies that the wait skips the status probe after
// timeout. Both probes use the same blocked access, and a second probe would
// double the time without observation.
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
		t.Fatalf("a restore whose exit probe never answers must fail as unobservable, got: %v", err)
	}

	if asked := statusAsked.Load(); asked != 0 {
		t.Fatalf("the status was asked for %d times while the exit probe was hanging", asked)
	}
}

// TestAwaitFDBRestoreOutlivesProbeTimeoutsThatClear checks that fewer than
// fdbDrillProbeTimeoutTolerance consecutive timeouts do not end the wait.
// The transient timeouts clear, and the restore finishes successfully.
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
			"progress must carry what the restore wrote once it answered, got %q",
			progress.summary(),
		)
	}
}

// TestAwaitFDBRestoreDoesNotCountATimedOutProbeAsProgress checks that an
// unanswered probe does not reset the stall clock. Every other probe times
// out while the restore remains inactive. The consecutive timeout count
// stays below tolerance, but the wait must still report the observed stall.
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
		t.Fatal("a wedged restore must fail even when every other probe times out")
	}

	if !strings.Contains(err.Error(), "made no progress for") {
		t.Fatalf("the failure must be the stall the drill observed, got: %v", err)
	}
}
