package service

import (
	"context"
	"log/slog"
	"time"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// modelRepairUncountTimeout bounds the record write after the check's
// context ends.
const modelRepairUncountTimeout = 5 * time.Second

// deployInterrupted handles a check context that ended after the deploy
// request was sent. The ML task can still finish. The attempt does not count
// toward the cap, and its claim time keeps the spacing. The next check from
// any process reads the model state and active tasks before another deploy.
// The record write uses its own bounded context.
func (r *SearchModelRepair) deployInterrupted(ctx context.Context, claim searchdomain.ModelRepairClaim, taskID string) error {
	record := claim.Record
	telemetry.L(ctx).InfoContext(ctx, "search.model_repair.deploy_interrupted", slog.String("model_id", record.ModelID),
		slog.String("task_id", taskID), slog.Int("attempt", record.Attempts))
	writeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelRepairUncountTimeout)
	defer cancel()
	uncounted, err := r.ports.Records.UncountModelRepair(writeContext, claim)
	if err != nil {
		return modelRepairFailure(writeContext, "uncount interrupted repair of model "+record.ModelID, err)
	}
	if !uncounted {
		telemetry.L(ctx).InfoContext(ctx, "search.model_repair.interrupt_stale", slog.String("model_id", record.ModelID),
			slog.String("task_id", taskID), slog.Int("attempt", record.Attempts))
	}
	return nil
}
