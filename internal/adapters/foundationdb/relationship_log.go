package foundationdb

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// logRelationshipFailure logs one failed relationship write as event.
func logRelationshipFailure(ctx context.Context, event string, err error, sourceID, targetID uuid.UUID) {
	telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", err.Error()), slog.String("source_id", sourceID.String()), slog.String("target_id", targetID.String()))
}

// logSearchScheduleFailure logs search.work.schedule_failed for a relationship
// write that failed to schedule search work. It logs nothing when the search
// code already logged the failure.
func logSearchScheduleFailure(ctx context.Context, err error, sourceID, targetID uuid.UUID) {
	if searchFailureWasLogged(err) {
		return
	}
	logRelationshipFailure(ctx, "search.work.schedule_failed", err, sourceID, targetID)
}
