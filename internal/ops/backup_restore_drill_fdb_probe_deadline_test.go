package ops

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// answerWithDeadline is a probe that answers with its deadline's error the
// instant it is asked, which is one side of the race a context-aware exec runs:
// the exec returns the deadline error at the moment the probe's context ends,
// so its answer and the context's done channel are ready together and the
// select may take either. Answering at once fixes which one it takes.
func answerWithDeadline(context.Context) (string, error) {
	return "", fmt.Errorf("read the fdbrestore status: %w", context.DeadlineExceeded)
}

// TestAwaitFDBRestoreCountsAnAnsweredDeadlineAsATimeout is the race this test
// exists for. A probe whose exec ended on the probe deadline returned that
// deadline as its error, and when the select took the answer rather than the
// done channel, the reading counted as a probe that answered with a failure:
// tolerated for the whole stall window instead of the three polls a probe that
// did not answer is given. Here every status probe answers with the deadline,
// and the wait must end as blind after three polls, not as a stall.
func TestAwaitFDBRestoreCountsAnAnsweredDeadlineAsATimeout(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	watch := fdbRestoreWatch{
		Finished: func(context.Context) (int, bool, error) { return 0, false, nil },
		Status:   answerWithDeadline,
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
// The first two exit probes below answer with the deadline and the restore then
// finishes cleanly.
func TestAwaitFDBRestoreToleratesAnExitProbeThatAnsweredWithItsDeadline(t *testing.T) {
	advanceClockPerRead(t, time.Second)
	var exitReads atomic.Int32
	watch := fdbRestoreWatch{
		Finished: func(ctx context.Context) (int, bool, error) {
			if exitReads.Add(1) <= fdbDrillProbeTimeoutTolerance-1 {
				_, err := answerWithDeadline(ctx)
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
