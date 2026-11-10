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

// answerAtDeadline returns the probe context's error when that context ends,
// as a context-aware exec does. The answer and the done channel become ready
// together. Either select branch must produce the same classification.
func answerAtDeadline(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", fmt.Errorf("read the fdbrestore status: %w", ctx.Err())
}

// answerDeadlineAtOnce returns a nested operation's deadline error before
// the probe's own context expires. That result counts as an answered failure.
func answerDeadlineAtOnce(context.Context) (string, error) {
	return "", fmt.Errorf("read the fdbrestore status: %w", context.DeadlineExceeded)
}

// TestClassifyFDBRestoreProbeAnswerTakesOnlyTheProbesOwnDeadline checks the
// answer branch directly because the test cannot control the select race.
// A deadline error counts as unanswered only after the probe's own deadline
// expires while the drill context remains active.
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

// TestAwaitFDBRestoreCountsAnAnsweredDeadlineAsATimeout checks the earlier
// race in which select accepted a deadline error as an answered failure.
// That classification tolerated failures for the entire stall window instead
// of the three-poll timeout tolerance. Every status probe here answers at its
// deadline. The wait must report lost observation after three polls instead
// of reporting a stall.
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

// TestAwaitFDBRestoreToleratesAnExitProbeThatAnsweredWithItsDeadline checks
// the exit probe's deadline classification. Previously, an answered deadline
// counted as an exit-probe failure and ended the wait on its first poll.
// Unanswered probes receive the timeout tolerance. The first two exit probes
// here answer at their deadline, and the restore then finishes successfully.
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

// TestAwaitFDBRestoreKeepsAnInstantDeadlineErrorAProbeError checks the probe
// context's deadline rather than the error text. An immediate deadline error
// before that context expires counts as an answered status failure. The wait
// retains that failure until the stall window ends and reports a stall rather
// than unanswered polls.
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
