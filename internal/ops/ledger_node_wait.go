// ledger_node_wait.go limits startup inactivity rather than total startup
// time (TACK-488). A large write-ahead log can require a long replay. The wait
// continues while the node makes progress, as the FoundationDB restore wait does.

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
	// ledgerNodeWaitDefaultStall limits time without progress, not total work.
	ledgerNodeWaitDefaultStall = 5 * time.Minute
	// ledgerNodeWaitDefaultPoll specifies the interval between node reads.
	ledgerNodeWaitDefaultPoll = 10 * time.Second
	// These keys identify the master's counters that decrease during recovery.
	ledgerCounterDeadNodes       = "dead_nodes"
	ledgerCounterUnderReplicated = "under_replicated_tablets"
)

// ledgerNodeHealth records the node's container status.
type ledgerNodeHealth struct {
	status  string
	running bool
}

// ledgerClusterCounts records unavailable nodes and tablets missing replicas.
// A decrease in either count indicates recovery progress.
type ledgerClusterCounts struct {
	deadNodes       int
	underReplicated int
}

// ledgerNodeWatch supplies container health and cluster counts for each poll.
// Function fields permit testing the wait without Docker or a cluster.
type ledgerNodeWatch struct {
	Health  func(ctx context.Context) (ledgerNodeHealth, error)
	Cluster func(ctx context.Context) (ledgerClusterCounts, error)
}

// ledgerNodeProgress records each counter's lowest observed value. Comparing
// against the minimum prevents repeated increases and decreases from extending
// the wait indefinitely.
type ledgerNodeProgress struct {
	lowWater map[string]int
}

func newLedgerNodeProgress() *ledgerNodeProgress {
	return &ledgerNodeProgress{lowWater: map[string]int{}}
}

// observe reports progress on the first successful cluster read or when a
// counter decreases below its recorded minimum.
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

// summary renders recorded minima in a stable order. It reports when the
// masters did not provide a readable cluster result.
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

// awaitLedgerNode waits until the container reports healthy, exits, or stops
// making progress. stallWindow limits time since the last observed progress.
// A stopped container ends the wait immediately.
func awaitLedgerNode(
	ctx context.Context,
	watch ledgerNodeWatch,
	stallWindow time.Duration,
	pollInterval time.Duration,
) (time.Duration, error) {
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
			return 0, fmt.Errorf(
				"the ledger node container is not running (health %s): %s",
				health.status,
				progress.summary(),
			)
		}

		counts, probeErr := watch.Cluster(ctx)
		switch {
		case probeErr != nil:
			lastProbeErr = probeErr.Error()
		case progress.observe(counts):
			lastMoved = opsNow()
			lastProbeErr = ""
			slog.DebugContext(
				ctx,
				"ops.ledger.node_wait.progress",
				slog.String("health", health.status),
				slog.String("marks", progress.summary()),
			)
		default:
			lastProbeErr = ""
		}

		if stalled := opsNow().Sub(lastMoved); stalled >= stallWindow {
			return 0, ledgerNodeStallError(stalled, health.status, progress, lastProbeErr)
		}

		if !opsWait(ctx, pollInterval) {
			slog.ErrorContext(
				ctx,
				"ops.ledger.node_wait.cancelled",
				slog.String("err", ctx.Err().Error()),
				slog.String("marks", progress.summary()),
			)

			return 0, fmt.Errorf("waiting for the ledger node: %w", ctx.Err())
		}
	}
}

// ledgerNodeStallError includes observed progress and the last cluster read
// failure to distinguish stalled recovery from unavailable masters.
func ledgerNodeStallError(
	stalled time.Duration,
	health string,
	progress *ledgerNodeProgress,
	lastProbeErr string,
) error {
	message := fmt.Sprintf(
		"the ledger node made no progress for %s (health %s): %s",
		stalled.Round(time.Second),
		health,
		progress.summary(),
	)
	if lastProbeErr != "" {
		message += "; the last cluster read failed: " + lastProbeErr
	}

	return errors.New(message)
}
