package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// answerAtDeadline is a probe that answers with its deadline's error the
// moment its context ends, which is what a context-aware exec does: the exec
// returns the deadline error as the probe's context expires, so its answer and
// the context's done channel are ready together and the select may take
// either. Whichever it takes, the reading must come out the same.
func answerAtDeadline(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", fmt.Errorf("read the fdbrestore status: %w", ctx.Err())
}

// answerDeadlineAtOnce is a probe that returns a deadline error before its own
// context has expired, the way a nested operation of the probe's own could.
// That is a probe that answered, with a failure, and not one that did not
// answer.
func answerDeadlineAtOnce(context.Context) (string, error) {
	return "", fmt.Errorf("read the fdbrestore status: %w", context.DeadlineExceeded)
}

// TestClassifyFDBRestoreProbeAnswerTakesOnlyTheProbesOwnDeadline pins the
// classification on the answered side of the select, where the race cannot be
// scripted: an answer carrying the deadline error counts as a probe that did
// not answer only when the probe's context expired with that deadline and the
// drill's context is still live.
func TestClassifyFDBRestoreProbeAnswerTakesOnlyTheProbesOwnDeadline(t *testing.T) {
	deadlineErr := fmt.Errorf("read the fdbrestore status: %w", context.DeadlineExceeded)
	answered := fdbRestoreReading{exitCode: 0, done: false, status: "", statusErr: deadlineErr, timedOut: false}
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancelExpired()
	live, cancelLive := context.WithTimeout(t.Context(), time.Hour)
	defer cancelLive()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	got := classifyFDBRestoreProbeAnswer(answered, expired, t.Context(), time.Minute)
	if !got.timedOut {
		t.Fatalf("an answer carrying the deadline after the probe's context expired must be a timeout, got %+v", got)
	}
	if !errors.Is(got.statusErr, errFDBRestoreProbeTimedOut) || !errors.Is(got.statusErr, context.DeadlineExceeded) {
		t.Fatalf("the timeout must carry both the probe's verdict and the exec's error, got %v", got.statusErr)
	}
	if got := classifyFDBRestoreProbeAnswer(answered, live, t.Context(), time.Minute); got.timedOut || got.statusErr != deadlineErr {
		t.Fatalf("a deadline error while the probe's context is live is a probe error, got %+v", got)
	}
	if got := classifyFDBRestoreProbeAnswer(answered, expired, cancelled, time.Minute); got.timedOut {
		t.Fatalf("a deadline under a cancelled drill is not a probe timeout, got %+v", got)
	}
}

// TestAwaitFDBRestoreCountsAnAnsweredDeadlineAsATimeout is the race this test
// exists for. A probe whose exec ended on the probe deadline returned that
// deadline as its error, and when the select took the answer rather than the
// done channel, the reading counted as a probe that answered with a failure:
// tolerated for the whole stall window instead of the three polls a probe that
// did not answer is given. Here every status probe answers at the deadline,
// and the wait must end as blind after three polls, not as a stall.
func TestAwaitFDBRestoreCountsAnAnsweredDeadlineAsATimeout(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status:   answerAtDeadline,
	}

	_, _, err := awaitOrHang(t, watch)

	if err == nil {
		t.Fatal("a restore whose status probe keeps hitting its deadline must not pass")
	}
	if !strings.Contains(err.Error(), "3 consecutive polls had a probe that did not answer") {
		t.Fatalf("a probe that returned its deadline must count as one that did not answer, got: %v", err)
	}
}

// TestAwaitFDBRestoreToleratesAnExitProbeThatAnsweredWithItsDeadline proves
// the same for the exit probe, whose failures are not tolerated at all: an exit
// probe that returned its deadline ended the wait on the first poll as a probe
// failure, when a probe that did not answer is ridden out under the tolerance.
// The first two exit probes below answer at the deadline and the restore then
// finishes cleanly.
func TestAwaitFDBRestoreToleratesAnExitProbeThatAnsweredWithItsDeadline(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	var exitReads atomic.Int32
	watch := fdbRestoreWatch{
		Finished: func(ctx context.Context) (int, bool, error) {
			if exitReads.Add(1) <= fdbDrillProbeTimeoutTolerance-1 {
				_, err := answerAtDeadline(ctx)
				return 0, false, err
			}
			return 0, true, nil
		},
		Status: func(context.Context) (string, error) { return fdbStatusLine(1, 10, 0, 4096, 0), nil },
	}

	_, exitCode, err := awaitOrHang(t, watch)
	if err != nil {
		t.Fatalf("exit probes that hit their deadline under the tolerance must not fail the drill: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
}

// TestAwaitFDBRestoreKeepsAnInstantDeadlineErrorAProbeError proves the
// classification is the probe's own deadline and not the error's text. A
// status probe that returns a deadline error at once, before its context has
// expired, answered with a failure: the wait carries that failure through the
// stall window and ends as a stall, never as polls it was blind for.
func TestAwaitFDBRestoreKeepsAnInstantDeadlineErrorAProbeError(t *testing.T) {
	advanceClockPerRead(t, testClockStep)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status:   answerDeadlineAtOnce,
	}

	_, _, err := awaitOrHang(t, watch)

	if err == nil {
		t.Fatal("a restore whose status probe keeps failing must not pass")
	}
	if strings.Contains(err.Error(), "could not be observed") {
		t.Fatalf("a deadline error the probe returned at once must not count as a blind poll, got: %v", err)
	}
	if !strings.Contains(err.Error(), "made no progress for") ||
		!strings.Contains(err.Error(), "the last status read failed: read the fdbrestore status: context deadline exceeded") {
		t.Fatalf("the failure must be the stall with the probe's own error, got: %v", err)
	}
}
