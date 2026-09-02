package ops

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testWaitGuard is how long a scripted wait may take in real time. Every run
// below either finishes in milliseconds or has hung on a probe, and a hang has
// to fail in seconds rather than by the test binary's own timeout.
const testWaitGuard = 10 * time.Second

// hangUntilDone is a probe that never answers on its own. It returns only when
// its context ends, which is what a hung container exec looks like once the
// exec's stream is closed on that context.
func hangUntilDone(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// awaitOrHang runs the wait on its own goroutine and fails the test when it has
// not returned within the guard.
func awaitOrHang(t *testing.T, watch fdbRestoreWatch) (*fdbRestoreProgress, int, error) {
	t.Helper()
	type outcome struct {
		progress *fdbRestoreProgress
		exitCode int
		err      error
	}
	returned := make(chan outcome, 1)
	go func() {
		progress, exitCode, err := awaitFDBRestore(t.Context(), watch,
			testStallWindow, testPollInterval, testProbeTimeout)
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

// TestAwaitFDBRestoreBoundsAStatusProbeThatNeverAnswers is the defect this file
// exists for. A status probe that hangs, which a container exec can, blocked
// the wait before the stall check ever ran, so no window took effect and the
// drill hung for as long as the exec did. Each probe now has its own deadline,
// and a probe that does not answer is not progress: the wait ends naming the
// polls it was blind for, not a stall it never observed.
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
		t.Fatalf("the failure must count the blind polls and name the probe deadline, want %q in: %v", want, err)
	}
	if !strings.Contains(err.Error(), errFDBRestoreProbeTimedOut.Error()) {
		t.Fatalf("the failure must carry why the last probe failed, got: %v", err)
	}
}

// TestAwaitFDBRestoreBoundsAnExitProbeThatNeverAnswers proves the other probe
// is bounded the same way, and that the wait does not go on to ask for a status
// through the same hung path, which would only double the time spent blind.
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

// TestAwaitFDBRestoreOutlivesProbeTimeoutsThatClear proves the tolerance: a
// probe that times out short of fdbDrillProbeTimeoutTolerance times in a row is
// a transient the drill rides out, and the restore that then answers goes on
// to finish cleanly.
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
		t.Fatalf("progress must carry what the restore wrote once it answered, got %q", progress.summary())
	}
}

// TestAwaitFDBRestoreDoesNotCountATimedOutProbeAsProgress proves a probe that
// did not answer never moves the stall clock. The restore below is wedged and
// every other probe times out, so the blind polls never reach the tolerance,
// and the wait must still end as the stall it is rather than run on.
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
