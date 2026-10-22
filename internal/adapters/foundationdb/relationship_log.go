package foundationdb

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// logRelationshipFailure writes one error log record with the message name event and the
// attributes err, source_id, and target_id.
func logRelationshipFailure(ctx context.Context, event string, err error, sourceID, targetID uuid.UUID) {
	telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", err.Error()), slog.String("source_id", sourceID.String()), slog.String("target_id", targetID.String()))
}

// logSearchScheduleFailure logs search.work.schedule_failed when a relationship
// write cannot schedule search work. It does not log a failure that the search
// code has already logged.
func logSearchScheduleFailure(ctx context.Context, err error, sourceID, targetID uuid.UUID) {
	if searchFailureWasLogged(err) {
		return
	}
	logRelationshipFailure(ctx, "search.work.schedule_failed", err, sourceID, targetID)
}
