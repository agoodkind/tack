// ledger_node_wait.go decides when a restarted ledger node has stopped coming
// up, not when it has taken too long. A node that replays a large write-ahead
// log is quiet for as long as the log is long, so a fixed budget for the whole
// start is a wall a growing ledger eventually hits, and it hit one on
// production (TACK-488). Only inactivity is bounded here, the way the
// FoundationDB restore wait bounds it: a node that keeps making progress
// waits for as long as it needs.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// ledgerNodeWaitDefaultStall is how long the node may show no progress at
	// all before the wait calls it stalled. It bounds inactivity and never
	// total work.
	ledgerNodeWaitDefaultStall = 5 * time.Minute
	// ledgerNodeWaitDefaultPoll is how often the wait reads the node.
	ledgerNodeWaitDefaultPoll = 10 * time.Second
	// Names of the falling counters, as the master reports them.
	ledgerCounterDeadNodes       = "dead_nodes"
	ledgerCounterUnderReplicated = "under_replicated_tablets"
)

// ledgerNodeHealth is what the container runtime says about the node.
type ledgerNodeHealth struct {
	status  string
	running bool
}

// ledgerClusterCounts is what the master quorum says about the cluster: how
// many nodes it cannot reach and how many tablets are short a replica. Both
// fall as a restarted node registers and brings its tablets back, so a fall
// in either is the evidence the node is still moving.
type ledgerClusterCounts struct {
	deadNodes       int
	underReplicated int
}

// ledgerNodeWatch is what the wait reads on each poll. Both are functions so
// the loop is exercised in tests without a Docker daemon or a cluster.
type ledgerNodeWatch struct {
	Health  func(ctx context.Context) (ledgerNodeHealth, error)
	Cluster func(ctx context.Context) (ledgerClusterCounts, error)
}

// ledgerNodeProgress remembers the lowest value each counter has reached.
// Marks, rather than the last reading, are what stop a count that moves both
// ways from looking like progress forever.
type ledgerNodeProgress struct {
	lowWater map[string]int
}

func newLedgerNodeProgress() *ledgerNodeProgress {
	return &ledgerNodeProgress{lowWater: map[string]int{}}
}

// observe records one reading and reports whether any counter fell past its
// mark. The first reading counts as movement, because the cluster answering
// at all is progress for a node that was just restarted.
func (p *ledgerNodeProgress) observe(counts ledgerClusterCounts) bool {
	moved := p.observeFall(ledgerCounterDeadNodes, counts.deadNodes)
	if p.observeFall(ledgerCounterUnderReplicated, counts.underReplicated) {
		moved = true
	}
	return moved
}

func (p *ledgerNodeProgress) observeFall(name string, value int) bool {
	previous, seen := p.lowWater[name]
	if seen && value >= previous {
		return false
	}
	p.lowWater[name] = value
	return true
}

// summary renders every mark in a stable order, and says so when no reading
// ever arrived.
func (p *ledgerNodeProgress) summary() string {
	if len(p.lowWater) == 0 {
		return "no cluster reading was ever readable from the masters"
	}
	parts := make([]string, 0, len(p.lowWater))
	for _, name := range slices.Sorted(maps.Keys(p.lowWater)) {
		parts = append(parts, name+"="+strconv.Itoa(p.lowWater[name]))
	}
	return strings.Join(parts, " ")
}

// awaitLedgerNode blocks until the node's container reports healthy, exits,
// or stops making progress. stallWindow bounds only the time since the last
// observed movement. A container that is not running ends the wait at once:
// nothing will make it healthy.
func awaitLedgerNode(ctx context.Context, watch ledgerNodeWatch, stallWindow, pollInterval time.Duration) (time.Duration, error) {
	progress := newLedgerNodeProgress()
	started := opsNow()
	lastMoved := started
	var lastProbeErr string
	for {
		health, err := watch.Health(ctx)
		if err != nil {
			return 0, err
		}
		if health.status == ledgerNodeHealthy {
			return opsNow().Sub(started), nil
		}
		if !health.running {
			return 0, fmt.Errorf("the ledger node container is not running (health %s): %s", health.status, progress.summary())
		}
		counts, probeErr := watch.Cluster(ctx)
		switch {
		case probeErr != nil:
			lastProbeErr = probeErr.Error()
		case progress.observe(counts):
			lastMoved = opsNow()
			lastProbeErr = ""
			slog.DebugContext(ctx, "ops.ledger.node_wait.progress",
				slog.String("health", health.status), slog.String("marks", progress.summary()))
		default:
			lastProbeErr = ""
		}
		if stalled := opsNow().Sub(lastMoved); stalled >= stallWindow {
			return 0, ledgerNodeStallError(stalled, health.status, progress, lastProbeErr)
		}
		if !opsWait(ctx, pollInterval) {
			slog.ErrorContext(ctx, "ops.ledger.node_wait.cancelled",
				slog.String("err", ctx.Err().Error()), slog.String("marks", progress.summary()))
			return 0, fmt.Errorf("waiting for the ledger node: %w", ctx.Err())
		}
	}
}

// ledgerNodeStallError names what the wait saw before it gave up, so a wedged
// node reads differently from masters the wait could not reach.
func ledgerNodeStallError(stalled time.Duration, health string, progress *ledgerNodeProgress, lastProbeErr string) error {
	message := fmt.Sprintf("the ledger node made no progress for %s (health %s): %s",
		stalled.Round(time.Second), health, progress.summary())
	if lastProbeErr != "" {
		message += "; the last cluster read failed: " + lastProbeErr
	}
	return errors.New(message)
}
