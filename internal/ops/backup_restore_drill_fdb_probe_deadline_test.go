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

// answerAtDeadline returns the probe context's deadline error when it expires.
// The answer and done channel can become ready together; either select branch
// must produce the same timeout classification.
func answerAtDeadline(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", fmt.Errorf("read the fdbrestore status: %w", ctx.Err())
}

// answerDeadlineAtOnce simulates a nested operation's deadline failure while
// the probe's own context remains active.
func answerDeadlineAtOnce(context.Context) (string, error) {
	return "", fmt.Errorf("read the fdbrestore status: %w", context.DeadlineExceeded)
}

// TestClassifyFDBRestoreProbeAnswerTakesOnlyTheProbesOwnDeadline checks both
// context states and error identity independently of select scheduling.
func TestClassifyFDBRestoreProbeAnswerTakesOnlyTheProbesOwnDeadline(t *testing.T) {
	deadlineErr := fmt.Errorf("read the fdbrestore status: %w", context.DeadlineExceeded)
	answered := fdbRestoreReading{
		exitCode:  0,
		done:      false,
		status:    "",
		statusErr: deadlineErr,
		timedOut:  false,
	}
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancelExpired()
	live, cancelLive := context.WithTimeout(t.Context(), time.Hour)
	defer cancelLive()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	got := classifyFDBRestoreProbeAnswer(answered, expired, t.Context(), time.Minute)
	if !got.timedOut {
		t.Fatalf("the probe's own expired deadline must indicate timeout, got %+v", got)
	}

	if !errors.Is(got.statusErr, errFDBRestoreProbeTimedOut) ||
		!errors.Is(got.statusErr, context.DeadlineExceeded) {
		t.Fatalf("the timeout must include probe and exec error identities, got %v", got.statusErr)
	}

	if got := classifyFDBRestoreProbeAnswer(
		answered,
		live,
		t.Context(),
		time.Minute,
	); got.timedOut ||
		got.statusErr != deadlineErr {
		t.Fatalf(
			"a deadline error under an active probe context must remain a probe error, got %+v",
			got,
		)
	}

	if got := classifyFDBRestoreProbeAnswer(
		answered,
		expired,
		cancelled,
		time.Minute,
	); got.timedOut {
		t.Fatalf("drill cancellation must not count as a probe timeout, got %+v", got)
	}
}

// TestAwaitFDBRestoreCountsAnAnsweredDeadlineAsATimeout checks consecutive
// probe-timeout failure even when the exec returns its deadline error through
// the answer channel rather than the context-done branch.
func TestAwaitFDBRestoreCountsAnAnsweredDeadlineAsATimeout(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status:   answerAtDeadline,
	}

	_, _, err := awaitOrHang(t, watch)
	if err == nil {
		t.Fatal("repeated status-probe deadlines must fail monitoring")
	}

	if !strings.Contains(err.Error(), "3 consecutive polls had a probe that did not answer") {
		t.Fatalf("an answer at the probe deadline must count as an unanswered poll, got: %v", err)
	}
}

// TestAwaitFDBRestoreToleratesAnExitProbeThatAnsweredWithItsDeadline checks
// successful completion after exit-probe deadlines below the failure tolerance.
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
		Status: func(context.Context) (string, error) {
			return fdbStatusLine(1, 10, 0, 4096, 0), nil
		},
	}

	_, exitCode, err := awaitOrHang(t, watch)
	if err != nil {
		t.Fatalf("exit-probe deadlines below tolerance must permit recovery: %v", err)
	}

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
}

// TestAwaitFDBRestoreClassifiesNestedDeadlineAsProbeError checks a deadline
// error returned before the probe's own context expires. The eventual stall
// includes that failed read instead of claiming unanswered polls.
func TestAwaitFDBRestoreClassifiesNestedDeadlineAsProbeError(t *testing.T) {
	advanceClockPerRead(t, testClockStep)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status:   answerDeadlineAtOnce,
	}

	_, _, err := awaitOrHang(t, watch)
	if err == nil {
		t.Fatal("repeated status-probe failures must not pass monitoring")
	}

	if strings.Contains(err.Error(), "could not be observed") {
		t.Fatalf("an immediate nested deadline must not count as an unanswered poll, got: %v", err)
	}

	if !strings.Contains(err.Error(), "made no progress for") ||
		!strings.Contains(
			err.Error(),
			"the last status read failed: read the fdbrestore status: context deadline exceeded",
		) {
		t.Fatalf("the stall must include the probe's error, got: %v", err)
	}
}
