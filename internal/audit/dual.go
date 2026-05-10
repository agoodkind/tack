package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

// DualRecorder writes every event to two underlying Recorders. Used during
// the Wave 1 migration window so the new Kafka producer path runs in
// parallel with the existing WAL path; either side can fail independently
// and the operator sees both outcomes.
//
// Order is fixed: Primary first, then Secondary. Both recorders are
// invoked even when Primary fails, because the migration requires that
// neither side mask the other's loss. The error returned to the caller
// joins both outcomes via [errors.Join], so the caller can branch on
// [errors.Is] for either side.
type DualRecorder struct {
	Primary   Recorder
	Secondary Recorder
}

// NewDualRecorder validates that both recorders are non-nil.
func NewDualRecorder(primary, secondary Recorder) (*DualRecorder, error) {
	if primary == nil {
		return nil, errors.New("audit dual: primary recorder required")
	}
	if secondary == nil {
		return nil, errors.New("audit dual: secondary recorder required")
	}
	return &DualRecorder{Primary: primary, Secondary: secondary}, nil
}

// Record forwards the event to Primary then Secondary. Both calls are
// always made, even when Primary returns an error, so a Kafka outage
// cannot hide a WAL outage and vice versa. Context cancellation is
// honored by each underlying recorder; the wrapper does not pre-empt.
func (d *DualRecorder) Record(ctx context.Context, ev Event) error {
	primaryErr := d.Primary.Record(ctx, ev)
	primaryDone := monoStart()
	if primaryErr != nil {
		telemetry.IncAuditDualWrite("primary", "error")
	} else {
		telemetry.IncAuditDualWrite("primary", "ok")
	}

	secondaryErr := d.Secondary.Record(ctx, ev)
	secondaryDone := monoStart()
	if secondaryErr != nil {
		telemetry.IncAuditDualWrite("secondary", "error")
	} else {
		telemetry.IncAuditDualWrite("secondary", "ok")
	}

	if primaryErr == nil && secondaryErr == nil {
		skewSeconds := secondaryDone.Sub(primaryDone).Seconds()
		if skewSeconds < 0 {
			skewSeconds = -skewSeconds
		}
		telemetry.ObserveAuditDualWriteSkew(skewSeconds)
	}

	switch {
	case primaryErr != nil && secondaryErr != nil:
		joined := errors.Join(primaryErr, secondaryErr)
		slog.ErrorContext(ctx, "audit.dual.both_failed",
			slog.String("verb", ev.Verb),
			slog.String("err", joined.Error()),
		)
		return fmt.Errorf("audit dual: both sides failed: %w", joined)
	case primaryErr != nil:
		slog.WarnContext(ctx, "dual.write.divergence",
			slog.String("event_id", eventIDForLog(ev)),
			slog.String("successful_path", "secondary"),
			slog.String("failed_path", "primary"),
			slog.String("err", primaryErr.Error()),
			slog.String("verb", ev.Verb),
		)
		return fmt.Errorf("audit dual primary: %w", primaryErr)
	case secondaryErr != nil:
		slog.WarnContext(ctx, "dual.write.divergence",
			slog.String("event_id", eventIDForLog(ev)),
			slog.String("successful_path", "primary"),
			slog.String("failed_path", "secondary"),
			slog.String("err", secondaryErr.Error()),
			slog.String("verb", ev.Verb),
		)
		return fmt.Errorf("audit dual secondary: %w", secondaryErr)
	default:
		return nil
	}
}

// Close shuts both underlying recorders down when they implement Close.
// Errors from both sides are joined.
func (d *DualRecorder) Close() error {
	var errs []error
	if c, ok := d.Primary.(interface{ Close() error }); ok {
		if err := c.Close(); err != nil {
			slog.Error("audit.dual.primary_close_failed", slog.String("err", err.Error()))
			errs = append(errs, fmt.Errorf("audit dual primary close: %w", err))
		}
	} else if c, ok := d.Primary.(interface{ Close() }); ok {
		c.Close()
	}
	if c, ok := d.Secondary.(interface{ Close() error }); ok {
		if err := c.Close(); err != nil {
			slog.Error("audit.dual.secondary_close_failed", slog.String("err", err.Error()))
			errs = append(errs, fmt.Errorf("audit dual secondary close: %w", err))
		}
	} else if c, ok := d.Secondary.(interface{ Close() }); ok {
		c.Close()
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}
