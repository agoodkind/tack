package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A non-default stall window checks that the wait uses its argument. The test
// clock advances one minute per read to simulate hours without sleeping.
const (
	ledgerTestStall     = 7 * time.Minute
	ledgerTestClockStep = time.Minute
	ledgerTestPoll      = 10 * time.Second
)

// scriptedLedgerNode reports starting until healthyAfter polls. It returns
// counts in order and repeats the last count after exhausting the list.
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

// recordLedgerWaits counts requested pauses without sleeping.
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

// fallingCounts decreases under-replicated tablets from total to zero,
// one tablet per poll.
func fallingCounts(total int) []ledgerClusterCounts {
	counts := make([]ledgerClusterCounts, 0, total+1)
	for remaining := total; remaining >= 0; remaining-- {
		counts = append(counts, ledgerClusterCounts{deadNodes: 0, underReplicated: remaining})
	}

	return counts
}

// TestLedgerNodeWaitContinuesDuringLongRecovery checks that recovery
// can continue for four simulated hours while each poll indicates progress
// (TACK-488). A fixed startup deadline would reject that recovery.
func TestLedgerNodeWaitContinuesDuringLongRecovery(t *testing.T) {
	const polls = 240
	advanceClockPerRead(t, ledgerTestClockStep)
	pauses := recordLedgerWaits(t)
	node := &scriptedLedgerNode{healthyAfter: polls, counts: fallingCounts(polls), running: true}

	took, err := awaitLedgerNode(
		context.Background(),
		node.watch(),
		ledgerTestStall,
		ledgerTestPoll,
	)
	if err != nil {
		t.Fatalf("the wait must continue while tablets recover: %v", err)
	}

	if took < 3*time.Hour {
		t.Fatalf("took = %s, want a wait longer than three hours", took)
	}

	if *pauses != polls {
		t.Fatalf("pauses = %d, want one per poll before the healthy read", *pauses)
	}
}

// TestLedgerNodeWaitReportsStalledRecovery checks that unchanged counts
// trigger a stall error with the last recorded minima after the stall window.
func TestLedgerNodeWaitReportsStalledRecovery(t *testing.T) {
	advanceClockPerRead(t, ledgerTestClockStep)
	recordLedgerWaits(t)
	frozen := []ledgerClusterCounts{
		{deadNodes: 1, underReplicated: 40},
		{deadNodes: 0, underReplicated: 12},
	}
	node := &scriptedLedgerNode{healthyAfter: 1000, counts: frozen, running: true}

	_, err := awaitLedgerNode(context.Background(), node.watch(), ledgerTestStall, ledgerTestPoll)
	if err == nil {
		t.Fatal("the wait must report a stall when counts stop decreasing")
	}

	message := err.Error()

	for _, want := range []string{
		"did not make progress for",
		"dead_nodes=0",
		"under_replicated_tablets=12",
		"health starting",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("stall error %q lacks %q", message, want)
		}
	}

	if node.polls > 12 {
		t.Fatalf("polls = %d, want the wait to stop soon after the stall window", node.polls)
	}
}

// TestLedgerNodeWaitReportsMasterReadFailures checks that a stall error
// includes the last master read failure and the absence of readable counts.
func TestLedgerNodeWaitReportsMasterReadFailures(t *testing.T) {
	advanceClockPerRead(t, ledgerTestClockStep)
	recordLedgerWaits(t)
	node := &scriptedLedgerNode{
		healthyAfter: 1000,
		counts:       fallingCounts(1),
		running:      true,
		clusterErr:   errors.New("get http://[::1]:7000/api/v1/health-check: connection refused"),
	}

	_, err := awaitLedgerNode(context.Background(), node.watch(), ledgerTestStall, ledgerTestPoll)
	if err == nil {
		t.Fatal("a node with unavailable masters must not wait forever")
	}

	if !strings.Contains(err.Error(), "the last cluster read failed: get http://[::1]:7000") {
		t.Fatalf("stall error %q must include the last read failure", err)
	}

	if !strings.Contains(err.Error(), "no cluster reading was ever readable") {
		t.Fatalf("stall error %q must report the absence of a readable cluster result", err)
	}
}

// TestAwaitLedgerNodeEndsWhenTheContainerExits checks that a stopped container
// ends the wait before the stall window.
func TestAwaitLedgerNodeEndsWhenTheContainerExits(t *testing.T) {
	advanceClockPerRead(t, ledgerTestClockStep)
	recordLedgerWaits(t)
	node := &scriptedLedgerNode{healthyAfter: 1000, counts: fallingCounts(50), running: false}

	_, err := awaitLedgerNode(context.Background(), node.watch(), ledgerTestStall, ledgerTestPoll)
	if err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("err = %v, want the stopped container identified", err)
	}

	if node.polls != 0 {
		t.Fatalf("polls = %d, want the wait to end before reading the cluster", node.polls)
	}
}

func TestLedgerNodeWaitWindowsRejectInvalidDurations(t *testing.T) {
	for _, in := range []ledgerNodeWaitInput{
		{Stall: "0s", Poll: "10s"},
		{Stall: "5m", Poll: "-1s"},
		{Stall: "soon", Poll: "10s"},
	} {
		if _, _, err := ledgerNodeWaitWindows(in); err == nil {
			t.Errorf("windows %+v must be refused", in)
		}
	}

	stall, poll, err := ledgerNodeWaitWindows(ledgerNodeWaitInput{
		Stall: "5m",
		Poll:  "10s",
	})
	if err != nil || stall != 5*time.Minute || poll != 10*time.Second {
		t.Fatalf("windows = %s %s %v, want 5m 10s", stall, poll, err)
	}
}
