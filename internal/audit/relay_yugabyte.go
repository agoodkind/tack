package audit

import (
	"context"
	"errors"
	"log/slog"
)

// errRelayStopped signals that AfterProduce stopped the current batch after a
// broker acknowledgment.
var errRelayStopped = errors.New("relay stopped after broker acknowledgment")

// claimingOutbox claims rows and deletes successful publishes in one
// transaction.
type claimingOutbox interface {
	ClaimBatch(ctx context.Context, limit int, publish func(context.Context, OutboxRow) error) (int, error)
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
