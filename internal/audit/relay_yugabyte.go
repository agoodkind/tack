package audit

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// errRelayStopped signals that AfterProduce stopped the current batch after a
// broker acknowledgment.
var errRelayStopped = errors.New("relay stopped after broker acknowledgment")

// claimingOutbox claims rows and deletes successful publishes in one
// transaction.
type claimingOutbox interface {
	ClaimBatch(ctx context.Context, limit int, publish func(context.Context, OutboxRow) error) (int, error)
}

func (r *Relay) yugabyteLoop(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		r.drainYugabyte(ctx)
		select {
		case <-r.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// drainYugabyte produces one batch and deletes each row only after the broker
// accepts it. A produce failure stops the batch and leaves the rest in place,
// so the next tick retries them; a redelivery is harmless because the consumer
// drops a duplicate event id, while a deletion before acceptance would lose an
// audit event outright.
func (r *Relay) drainYugabyte(ctx context.Context) {
	if claimer, ok := r.yugabyte.(claimingOutbox); ok {
		r.drainClaimed(ctx, claimer)
		return
	}
	batch, err := r.yugabyte.ReadBatch(ctx, r.batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "audit.relay.yugabyte_read_failed", slog.String("err", err.Error()))
		return
	}
	producedCount := 0
	deletedCount := 0
	for _, row := range batch {
		stopped, recordErr := r.produceRow(ctx, row)
		if recordErr != nil {
			slog.ErrorContext(ctx, "audit.relay.yugabyte_produce_failed",
				slog.String("err", recordErr.Error()))
			break
		}
		if stopped {
			break
		}
		producedCount++
		if deleteErr := r.deleteRow(ctx, row); deleteErr != nil {
			slog.ErrorContext(ctx, "audit.relay.yugabyte_delete_failed",
				slog.String("err", deleteErr.Error()))
			break
		}
		deletedCount++
	}
	slog.DebugContext(ctx, "audit.relay.yugabyte_batch",
		slog.Int("read_count", len(batch)),
		slog.Int("produced_count", producedCount),
		slog.Int("deleted_count", deletedCount),
	)
}

// drainClaimed publishes one claimed batch and logs its committed row count. It
// returns errRelayStopped from the publish callback when AfterProduce stops the
// batch and does not log that error as a failure.
func (r *Relay) drainClaimed(ctx context.Context, claimer claimingOutbox) {
	published, err := claimer.ClaimBatch(ctx, r.batchSize, func(ctx context.Context, row OutboxRow) error {
		stopped, err := r.produceRow(ctx, row)
		if err != nil {
			return err
		}
		if stopped {
			return errRelayStopped
		}
		return nil
	})
	if err != nil && !errors.Is(err, errRelayStopped) {
		slog.ErrorContext(ctx, "audit.relay.yugabyte_claim_failed", slog.String("err", err.Error()))
	}
	slog.DebugContext(ctx, "audit.relay.yugabyte_claimed_batch", slog.Int("published_count", published))
}
