package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// awaitDBPlanOutboxDrain reads the event IDs in public.ops_outbox through
// reader. It then counts every dbPlanProjectionPoll how many of those events
// remain, and returns nil when none remains. The relay removes an event after
// the broker accepts it. It returns an error when waitCtx ends first.
func awaitDBPlanOutboxDrain(waitCtx context.Context, reader *audit.Reader, planID uuid.UUID, wait time.Duration) error {
	eventIDs, err := reader.OperatorOutboxEventIDs(waitCtx)
	if err != nil {
		slog.ErrorContext(waitCtx, "db.plan.outbox_read_failed", slog.String("plan_id", planID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("read the event IDs in public.ops_outbox to close plan %s: %w", planID, err)
	}
	ticker := time.NewTicker(dbPlanProjectionPoll)
	defer ticker.Stop()
	remaining, lastRead := int64(len(eventIDs)), "none"
	for remaining > 0 {
		select {
		case <-waitCtx.Done():
			return dbPlanOutboxTimeout(waitCtx, planID, wait, remaining, len(eventIDs), lastRead)
		case <-ticker.C:
		}
		waiting, readErr := reader.OperatorOutboxWaiting(waitCtx, eventIDs)
		if readErr != nil {
			lastRead = readErr.Error()
			continue
		}
		remaining = waiting
	}
	telemetry.L(waitCtx).InfoContext(waitCtx, "db.plan.outbox_drained",
		slog.String("plan_id", planID.String()), slog.Int("events", len(eventIDs)))
	return nil
}

// dbPlanOutboxTimeout logs and returns the error of an outbox wait that
// passed its bound.
func dbPlanOutboxTimeout(
	ctx context.Context,
	planID uuid.UUID,
	wait time.Duration,
	remaining int64,
	total int,
	lastRead string,
) error {
	err := errors.New("the relay did not send " + strconv.FormatInt(remaining, 10) + " of the " +
		strconv.Itoa(total) + " events in public.ops_outbox within " + wait.String() +
		"; last operator outbox read error: " + lastRead)
	slog.ErrorContext(ctx, "db.plan.outbox_timeout", slog.String("plan_id", planID.String()), slog.String("err", err.Error()))
	return err
}
