// The ledger node wait limits inactivity during startup. A growing ledger can
// require longer write-ahead-log replays. The original TACK-488 report recorded
// a long, quiet replay that exceeded a fixed production startup budget. Like
// the FoundationDB restore wait, this wait permits continued startup while the
// cluster counters show progress.

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
	// ledgerNodeWaitDefaultStall limits the interval without observed progress.
	// It does not limit total startup time.
	ledgerNodeWaitDefaultStall = 5 * time.Minute
	// ledgerNodeWaitDefaultPoll is how often the wait reads the node.
	ledgerNodeWaitDefaultPoll = 10 * time.Second
	// The master reports progress with these decreasing counters.
	ledgerCounterDeadNodes       = "dead_nodes"
	ledgerCounterUnderReplicated = "under_replicated_tablets"
)

// ledgerNodeHealth records the container runtime's node status.
type ledgerNodeHealth struct {
	status  string
	running bool
}

// ledgerClusterCounts records the masters' counts of unavailable nodes and
// tablets with too few replicas. Registration and replica recovery can reduce
// these counts. The wait treats a decrease as startup progress.
type ledgerClusterCounts struct {
	deadNodes       int
	underReplicated int
}

// ledgerNodeWatch supplies node health and cluster counts for each poll.
// Functions let tests exercise the loop without a Docker daemon or a cluster.
type ledgerNodeWatch struct {
	Health  func(ctx context.Context) (ledgerNodeHealth, error)
	Cluster func(ctx context.Context) (ledgerClusterCounts, error)
}

// ledgerNodeProgress records each counter's lowest observed value. A count
// that rises and falls must fall below that value to count as further progress.
type ledgerNodeProgress struct {
	lowWater map[string]int
}

func newLedgerNodeProgress() *ledgerNodeProgress {
	return &ledgerNodeProgress{lowWater: map[string]int{}}
}

// observe records one reading and reports whether either counter sets a new
// minimum. The first readable cluster response also counts as startup progress.
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

// summary prints the minimum counter values in a stable order. It reports
// when the masters never supplied a readable response.
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

// awaitLedgerNode waits for a healthy container and returns an error if the
// container exits or progress stalls. stallWindow limits time since the last
// observed progress. A container that is not running ends the wait immediately.
// Health-read errors and context cancellation also end the wait.
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

// ledgerNodeStallError reports the observed health and minimum counters.
// It includes the last cluster-read error when that read failed.
func ledgerNodeStallError(stalled time.Duration, health string, progress *ledgerNodeProgress, lastProbeErr string) error {
	message := fmt.Sprintf("the ledger node made no progress for %s (health %s): %s",
		stalled.Round(time.Second), health, progress.summary())
	if lastProbeErr != "" {
		message += "; the last cluster read failed: " + lastProbeErr
	}
	return errors.New(message)
}
