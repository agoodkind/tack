package ops

import (
	"context"
	"errors"
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
// the broker accepts it. The first failed read returns a *dbPlanReadError,
// and the end of waitCtx returns the timeout error. It logs neither error;
// the caller logs the error it returns to the command.
func awaitDBPlanOutboxDrain(waitCtx context.Context, reader *audit.Reader, planID uuid.UUID, wait time.Duration) error {
	eventIDs, err := reader.OperatorOutboxEventIDs(waitCtx)
	if err != nil {
		return dbPlanWaitReadFailed(waitCtx, errors.New("public.ops_outbox was not read within "+wait.String()),
			"read the event IDs in public.ops_outbox to close plan "+planID.String(), err)
	}
	ticker := time.NewTicker(dbPlanProjectionPoll)
	defer ticker.Stop()
	remaining := int64(len(eventIDs))
	for remaining > 0 {
		select {
		case <-waitCtx.Done():
			return dbPlanOutboxTimeout(wait, remaining, len(eventIDs))
		case <-ticker.C:
		}
		waiting, readErr := reader.OperatorOutboxWaiting(waitCtx, eventIDs)
		if readErr != nil {
			return dbPlanWaitReadFailed(waitCtx, dbPlanOutboxTimeout(wait, remaining, len(eventIDs)),
				"count the waiting events in public.ops_outbox to close plan "+planID.String(), readErr)
		}
		remaining = waiting
	}
	telemetry.L(waitCtx).InfoContext(waitCtx, "db.plan.outbox_drained",
		slog.String("plan_id", planID.String()), slog.Int("events", len(eventIDs)))
	return nil
}

// dbPlanOutboxTimeout returns the error of an outbox wait that passed its
// bound.
func dbPlanOutboxTimeout(wait time.Duration, remaining int64, total int) error {
	return errors.New("the relay did not send " + strconv.FormatInt(remaining, 10) + " of the " +
		strconv.Itoa(total) + " events in public.ops_outbox within " + wait.String())
}
