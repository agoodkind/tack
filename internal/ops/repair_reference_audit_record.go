package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// recordReferenceRepair writes one ledger event per rename, per counter seed,
// and per reference key the run applied. Event identity is derived from the
// fact itself, so a second run over unchanged state adds nothing.
func recordReferenceRepair(
	ctx context.Context,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	report RepairReferenceReport,
	occurredAt time.Time,
) error {
	idempotent, ok := outbox.(audit.IdempotentOutboxWriter)
	if !ok {
		err := fmt.Errorf("audit outbox does not support idempotent repair writes")
		telemetry.L(ctx).ErrorContext(
			ctx,
			"repair.reference_uniqueness.outbox_invalid",
			slog.String("err", err.Error()),
		)

		return err
	}

	events, err := referenceRepairEvents(ctx, principal, report, occurredAt)
	if err != nil {
		return err
	}

	for _, event := range events {
		if _, writeErr := idempotent.WriteOutboxIfAbsent(ctx, event); writeErr != nil {
			wrapped := fmt.Errorf("record repaired %s: %w", event.Entity.Identifier, writeErr)
			telemetry.L(ctx).ErrorContext(
				ctx,
				"repair.reference_uniqueness.record_failed",
				slog.String("event_id", event.EventID.String()),
				slog.String("err", wrapped.Error()),
			)

			return wrapped
		}
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"repair.reference_uniqueness.recorded",
		slog.Int("events", len(events)),
	)

	return nil
}
