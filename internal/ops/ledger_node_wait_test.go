package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests use a stall window that differs from the default to detect a
// hardcoded limit. Each clock read advances simulated time by one minute;
// hundreds of polls represent hours without sleeping for that duration.
const (
	ledgerTestStall     = 7 * time.Minute
	ledgerTestClockStep = time.Minute
	ledgerTestPoll      = 10 * time.Second
)

// scriptedLedgerNode keeps the node starting until healthyAfter polls.
// It supplies cluster counts in order and repeats the last reading.
type scriptedLedgerNode struct {
	healthyAfter int
	counts       []ledgerClusterCounts
	clusterErr   error
	running      bool
	polls        int
}

func (s *scriptedLedgerNode) watch() ledgerNodeWatch {
	return ledgerNodeWatch{
		Health: func(context.Context) (ledgerNodeHealth, error) {
			if s.polls >= s.healthyAfter {
				return ledgerNodeHealth{status: ledgerNodeHealthy, running: true}, nil
			}
			return ledgerNodeHealth{status: "starting", running: s.running}, nil
		},
		Cluster: func(context.Context) (ledgerClusterCounts, error) {
			counts := s.counts[min(s.polls, len(s.counts)-1)]
			s.polls++
			if s.clusterErr != nil {
				return ledgerClusterCounts{}, s.clusterErr
			}
			return counts, nil
		},
	}
}

// recordLedgerWaits replaces the package pause with a counter. Scripted runs
// do not sleep.
func recordLedgerWaits(t *testing.T) *int {
	t.Helper()
	pauses := 0
	waitFunc = func(ctx context.Context, _ time.Duration) bool {
		pauses++
		return ctx.Err() == nil
	}
	t.Cleanup(func() { waitFunc = waitUntil })
	return &pauses
}

// fallingCounts supplies under-replicated tablet counts from total down to
// zero, one count per poll.
func fallingCounts(total int) []ledgerClusterCounts {
	counts := make([]ledgerClusterCounts, 0, total+1)
	for remaining := total; remaining >= 0; remaining-- {
		counts = append(counts, ledgerClusterCounts{deadNodes: 0, underReplicated: remaining})
	}
	return counts
}

// TestAwaitLedgerNodeLetsASlowReplayRunPastAnyFixedBudget checks the
// TACK-488 regression: an earlier deployment budget could fail a node during
// startup despite progress. The script records an initial tablet count and
// supplies a smaller count on each later poll before reporting healthy.
//
// The test checks these results:
//   - The wait succeeds while the node keeps making progress.
//   - The returned simulated duration is at least three hours.
//   - The wait pauses 240 times, once for each cluster reading.
func TestAwaitLedgerNodeLetsASlowReplayRunPastAnyFixedBudget(t *testing.T) {
	const polls = 240
	advanceClockPerRead(t, ledgerTestClockStep)
	pauses := recordLedgerWaits(t)
	node := &scriptedLedgerNode{healthyAfter: polls, counts: fallingCounts(polls), running: true}

	took, err := awaitLedgerNode(context.Background(), node.watch(), ledgerTestStall, ledgerTestPoll)
	if err != nil {
		t.Fatalf("a node that keeps bringing tablets back must be waited for: %v", err)
	}
	if took < 3*time.Hour {
		t.Fatalf("took = %s, want a wait that outlived any fixed budget", took)
	}
	if *pauses != polls {
		t.Fatalf("pauses = %d, want one per poll before the healthy read", *pauses)
	}
}

// TestAwaitLedgerNodeFailsANodeThatStoppedMoving checks that fixed counter
// values trigger a stall error. It checks the health and minimum values in the
// error and limits the poll count after progress stops.
func TestAwaitLedgerNodeFailsANodeThatStoppedMoving(t *testing.T) {
	advanceClockPerRead(t, ledgerTestClockStep)
	recordLedgerWaits(t)
	frozen := []ledgerClusterCounts{{deadNodes: 1, underReplicated: 40}, {deadNodes: 0, underReplicated: 12}}
	node := &scriptedLedgerNode{healthyAfter: 1000, counts: frozen, running: true}

	_, err := awaitLedgerNode(context.Background(), node.watch(), ledgerTestStall, ledgerTestPoll)
	if err == nil {
		t.Fatal("a node whose counts stopped falling must be reported as stalled")
	}
	message := err.Error()
	for _, want := range []string{"no progress for", "dead_nodes=0", "under_replicated_tablets=12", "health starting"} {
		if !strings.Contains(message, want) {
			t.Errorf("stall error %q lacks %q", message, want)
		}
	}
	if node.polls > 12 {
		t.Fatalf("polls = %d, want the wait to stop soon after the window, not run on", node.polls)
	}
}

// TestAwaitLedgerNodeNamesMastersItCouldNotReach checks that the stall error
// includes the last cluster-read failure and reports that the masters never
// supplied a readable response.
func TestAwaitLedgerNodeNamesMastersItCouldNotReach(t *testing.T) {
	advanceClockPerRead(t, ledgerTestClockStep)
	recordLedgerWaits(t)
	node := &scriptedLedgerNode{
		healthyAfter: 1000, counts: fallingCounts(1), running: true,
		clusterErr: errors.New("get http://[::1]:7000/api/v1/health-check: connection refused"),
	}

	_, err := awaitLedgerNode(context.Background(), node.watch(), ledgerTestStall, ledgerTestPoll)
	if err == nil {
		t.Fatal("a node with unreachable masters must not wait forever")
	}
	if !strings.Contains(err.Error(), "the last cluster read failed: get http://[::1]:7000") {
		t.Fatalf("stall error %q must name the last read failure", err)
	}
	if !strings.Contains(err.Error(), "no cluster reading was ever readable") {
		t.Fatalf("stall error %q must say no reading arrived", err)
	}
}

// TestAwaitLedgerNodeEndsWhenTheContainerExits checks that a stopped
// container ends the wait before the stall window and before any cluster read.
func TestAwaitLedgerNodeEndsWhenTheContainerExits(t *testing.T) {
	advanceClockPerRead(t, ledgerTestClockStep)
	recordLedgerWaits(t)
	node := &scriptedLedgerNode{healthyAfter: 1000, counts: fallingCounts(50), running: false}

	_, err := awaitLedgerNode(context.Background(), node.watch(), ledgerTestStall, ledgerTestPoll)
	if err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("err = %v, want the exited container named", err)
	}
	if node.polls != 0 {
		t.Fatalf("polls = %d, want the wait to end before reading the cluster", node.polls)
	}
}

func TestLedgerNodeWaitWindowsRefuseZeroAndGarbage(t *testing.T) {
	for _, in := range []ledgerNodeWaitInput{
		{Stall: "0s", Poll: "10s"},
		{Stall: "5m", Poll: "-1s"},
		{Stall: "soon", Poll: "10s"},
	} {
		if _, _, err := ledgerNodeWaitWindows(in); err == nil {
			t.Errorf("windows %+v must be refused", in)
		}
	}
	stall, poll, err := ledgerNodeWaitWindows(ledgerNodeWaitInput{Stall: "5m", Poll: "10s"})
	if err != nil || stall != 5*time.Minute || poll != 10*time.Second {
		t.Fatalf("windows = %s %s %v, want 5m 10s", stall, poll, err)
	}
}
