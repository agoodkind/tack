package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// spillTimeout bounds one spill. It is the FoundationDB transaction limit
// doubled, so a slow cluster gets one retry inside the budget and a stuck one
// cannot hold the request open.
const spillTimeout = 10 * time.Second

// OutboxAppender is a durable landing zone for an event the primary recorder
// could not take. The FoundationDB operator outbox is the production
// implementation; the audit-consumer's relay drains it to Kafka.
type OutboxAppender interface {
	Append(ctx context.Context, event json.RawMessage) error
}

// SpillRecorder records through Primary and, when Primary fails, appends the
// event to Spill so it survives the outage and lands afterwards.
//
// The 2026-07-06 gap is why this exists: the producer dropped every event its
// broker refused, and eight issue creations reached the store with no ledger
// row (TACK-335). With a spill in place the same outage leaves the events in
// FoundationDB, which the product write already reached, and the relay
// delivers them once the broker returns (TACK-336).
//
// Logging is per outage rather than per event (TACK-320). The first spill logs
// at Error with the failure that caused it; later spills in the same outage
// log at Debug with a running count; the first success afterwards logs at Info
// with the total. A broker down for an hour therefore costs two lines an
// operator reads, not one per write. A spill that itself fails is a real loss
// and logs at Error every time, since there is no longer anywhere to put the
// event.
type SpillRecorder struct {
	Primary  Recorder
	Spill    OutboxAppender
	inOutage atomic.Bool
	spilled  atomic.Int64
}

// Record delivers through Primary, and spills on failure.
//
// A successful spill returns nil: the event is durable and the caller has
// nothing further to do for it. Returning the primary error would make the
// MCP wrapper and the canonical recorder each log a failure for an event that
// was not lost, which is the flood this replaces.
func (s *SpillRecorder) Record(ctx context.Context, ev Event) error {
	primaryErr := s.Primary.Record(ctx, ev)
	if primaryErr == nil {
		s.noteRecovered(ctx)
		return nil
	}
	payload, marshalErr := MarshalEvent(ev)
	if marshalErr != nil {
		telemetry.IncAuditDropped(ev.Verb, "spill_marshal")
		return errors.Join(primaryErr, marshalErr)
	}
	// The spill runs detached from the request's cancellation with a deadline
	// of its own. The primary may have failed precisely because the request
	// context expired, and a spill that inherits that expiry fails the same
	// way, after the product write it describes has already committed. The
	// request's values stay attached so the trace and request ids still
	// reach the logs.
	spillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spillTimeout)
	defer cancel()
	if spillErr := s.Spill.Append(spillCtx, payload); spillErr != nil {
		telemetry.IncAuditDropped(ev.Verb, "spill")
		slog.ErrorContext(ctx, "audit.spill_failed",
			slog.String("verb", ev.Verb),
			slog.String("event_id", ev.EventID.String()),
			slog.String("primary_err", primaryErr.Error()),
			slog.String("err", spillErr.Error()))
		return fmt.Errorf("audit spill after primary failure: %w", errors.Join(primaryErr, spillErr))
	}
	telemetry.IncAuditSpilled(ev.Verb)
	count := s.spilled.Add(1)
	if s.inOutage.CompareAndSwap(false, true) {
		slog.ErrorContext(ctx, "audit.outage_began",
			slog.String("verb", ev.Verb),
			slog.String("event_id", ev.EventID.String()),
			slog.String("err", primaryErr.Error()))
		return nil
	}
	slog.DebugContext(ctx, "audit.spilled",
		slog.String("verb", ev.Verb),
		slog.String("event_id", ev.EventID.String()),
		slog.Int64("spilled_this_outage", count))
	return nil
}

// noteRecovered closes an outage on the first primary success after it, and
// says how many events the relay now owes the ledger.
func (s *SpillRecorder) noteRecovered(ctx context.Context) {
	if !s.inOutage.CompareAndSwap(true, false) {
		return
	}
	slog.InfoContext(ctx, "audit.outage_ended",
		slog.Int64("spilled", s.spilled.Swap(0)))
}

// Close closes Primary when it supports closing.
func (s *SpillRecorder) Close() error {
	switch recorder := s.Primary.(type) {
	case interface{ Close() error }:
		if err := recorder.Close(); err != nil {
			slog.Error("audit.spill_primary_close_failed", slog.String("err", err.Error()))
			return fmt.Errorf("close spill primary: %w", err)
		}
	case interface{ Close() }:
		recorder.Close()
	}
	return nil
}
