package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The window is deliberately not the deployed one, so the wait is exercised
// against a window it does not hardcode. The fake clock moves one minute per
// read, so a run of a few hundred polls covers hours without sleeping.
const (
	ledgerTestStall     = 7 * time.Minute
	ledgerTestClockStep = time.Minute
	ledgerTestPoll      = 10 * time.Second
)

// scriptedLedgerNode drives one awaitLedgerNode run: the node stays starting
// for healthyAfter polls, and the cluster counts are handed out in order with
// the last one repeating.
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

// recordLedgerWaits swaps the package pause for one that only counts, so the
// scripted runs take no real time.
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

// fallingCounts renders a node bringing its tablets back one at a time from
// total down to zero, one count per poll.
func fallingCounts(total int) []ledgerClusterCounts {
	counts := make([]ledgerClusterCounts, 0, total+1)
	for remaining := total; remaining >= 0; remaining-- {
		counts = append(counts, ledgerClusterCounts{deadNodes: 0, underReplicated: remaining})
	}
	return counts
}

// TestAwaitLedgerNodeLetsASlowReplayRunPastAnyFixedBudget is the defect this
// file exists for. The node below takes four hours of wall clock to come
// healthy and the wait lets it, because every poll shows one more tablet
// back. Under the fixed budget the deploy used to carry, it would have been
// declared failed while it was working (TACK-488).
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

// TestAwaitLedgerNodeFailsANodeThatStoppedMoving proves the other half: a
// node whose counts froze is reported as stalled after the window, with the
// last marks in the message, and the wait does not run on.
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

// TestAwaitLedgerNodeNamesMastersItCouldNotReach keeps a wedged node apart
// from masters the wait never reached: the stall message carries the last
// read failure, and the marks say no reading ever arrived.
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

// TestAwaitLedgerNodeEndsWhenTheContainerExits proves a container that died
// ends the wait at once rather than after the stall window.
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
