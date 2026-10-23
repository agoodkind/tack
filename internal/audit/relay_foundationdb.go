package audit

import (
	"context"
	"log/slog"
	"time"
)

// relayLeaseReleaseTimeout limits the FoundationDB lease release attempt during
// Close.
const relayLeaseReleaseTimeout = 5 * time.Second

// relayLease provides methods to acquire, renew, and release the FoundationDB
// relay lease.
type relayLease interface {
	HoldRelayLease(ctx context.Context) (bool, error)
	ReleaseRelayLease(ctx context.Context) error
}

// foundationDBLoop acquires or renews the relay lease before draining each
// batch. It restarts reads from the beginning of the outbox whenever the relay
// gains the lease.
func (r *Relay) foundationDBLoop(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	var mark []byte
	holding := false
	for {
		held := r.holdLease(ctx, holding)
		if held && !holding {
			mark = nil
		}
		holding = held
		if held {
			mark = r.drainFoundationDB(ctx, mark)
		}
		select {
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// holdLease acquires or renews the FoundationDB relay lease and logs ownership
// changes. It permits draining when the outbox does not implement relayLease.
func (r *Relay) holdLease(ctx context.Context, holding bool) bool {
	lease, ok := r.foundationDB.(relayLease)
	if !ok {
		return true
	}
	held, err := lease.HoldRelayLease(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.lease_hold_failed", slog.String("err", err.Error()))
		held = false
	}
	switch {
	case held && !holding:
		slog.InfoContext(ctx, "audit.relay.lease_acquired")
	case !held && holding:
		slog.InfoContext(ctx, "audit.relay.lease_lost")
	}
	return held
}

// releaseLease attempts to release the FoundationDB relay lease within
// relayLeaseReleaseTimeout and logs any failure.
func (r *Relay) releaseLease() {
	lease, ok := r.foundationDB.(relayLease)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), relayLeaseReleaseTimeout)
	defer cancel()
	if err := lease.ReleaseRelayLease(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.relay.lease_release_failed", slog.String("err", err.Error()))
	}
}

func (r *Relay) drainFoundationDB(ctx context.Context, mark []byte) []byte {
	batch, err := r.foundationDB.ReadOutboxFrom(ctx, mark, r.batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.foundationdb_read_failed", slog.String("err", err.Error()))
		return mark
	}
	producedCount := 0
	stopped := false
	var lastAccepted []byte
	var reads []PublishedRead
	for _, entry := range batch {
		eventID, err := r.produceEntry(ctx, entry)
		if err != nil {
			slog.ErrorContext(ctx, "audit.relay.foundationdb_produce_failed", slog.String("err", err.Error()))
			break
		}
		producedCount++
		if r.stopAfterProduce(ctx, eventID) {
			stopped = true
			break
		}
		lastAccepted = append(lastAccepted[:0], entry.Mark...)
		reads = append(reads, recordPublishedReads(ctx, entry.Event)...)
	}
	clearedCount := 0
	if len(lastAccepted) > 0 && !stopped {
		if err := r.clearThrough(ctx, lastAccepted, reads); err != nil {
			slog.ErrorContext(ctx, "audit.relay.foundationdb_clear_failed", slog.String("err", err.Error()))
		} else {
			clearedCount = producedCount
			mark = lastAccepted
		}
	}
	slog.DebugContext(ctx, "audit.relay.foundationdb_batch",
		slog.Int("read_count", len(batch)),
		slog.Int("produced_count", producedCount),
		slog.Int("cleared_count", clearedCount),
	)
	return mark
}
