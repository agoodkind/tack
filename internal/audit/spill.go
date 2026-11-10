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

// SpillTimeout sets the deadline for one spill. Its original budget allowed
// twice the FoundationDB transaction limit for one retry. The deadline bounds
// the request's wait for a stuck cluster.
const SpillTimeout = 10 * time.Second

// OutboxAppender durably stores an event that the primary recorder could not
// record. The FoundationDB operator outbox implements this interface in
// production, and the audit-consumer's relay publishes its events to Kafka.
type OutboxAppender interface {
	Append(ctx context.Context, event json.RawMessage) error
}

// SpillRecorder records through Primary and appends an event to Spill when
// Primary fails. The relay can publish stored events after the outage.
//
// The original TACK-335 incident account reports that the producer dropped
// broker-refused events on 2026-07-06. Eight issue creations lacked audit
// rows. TACK-336 introduced FoundationDB spill storage for refused events
// and relay delivery after broker recovery. Product writes also use
// FoundationDB.
//
// TACK-320 established logging per outage. The first successful spill logs
// the primary failure at Error. Later spills log at Debug with a running
// count. The first primary success logs the total at Info. This produces one
// Error and one Info line for an outage, including an outage lasting an hour.
// Each failed spill logs at Error because neither recorder stored the event.
type SpillRecorder struct {
	Primary  Recorder
	Spill    OutboxAppender
	inOutage atomic.Bool
	spilled  atomic.Int64
}

// Record calls Primary.Record and appends the event to Spill on failure.
//
// A successful spill returns nil because the outbox stored the event
// durably. Returning the primary error after a successful spill would make
// both the MCP wrapper and canonical recorder log a failure for a stored
// event. The spill result avoids that repeated failure logging.
//
//   - A primary success does not append to Spill.
//   - A failed spill returns an error.
func (s *SpillRecorder) Record(ctx context.Context, ev Event) error {
	primaryErr := s.Primary.Record(ctx, ev)
	if primaryErr == nil {
		s.noteRecovered(ctx)
		return nil
	}

	return s.spill(ctx, ev, primaryErr)
}

// errBrokerRefused represents a per-event refusal from a batch produce result
// that does not supply its own error.
var errBrokerRefused = errors.New("broker refused the produce")

// spill appends an event after primary rejection and reports outage state.
// A successful append returns nil because the outbox stored the event durably.
func (s *SpillRecorder) spill(ctx context.Context, ev Event, primaryErr error) error {
	payload, marshalErr := MarshalEvent(ev)
	if marshalErr != nil {
		telemetry.IncAuditDropped(ev.Verb, "spill_marshal")
		return errors.Join(primaryErr, marshalErr)
	}

	// The spill uses its own deadline and does not inherit request cancellation.
	// An expired request can cause the primary failure after the product write
	// commits; reusing that cancellation would also fail the spill. The context
	// preserves request values for trace and request IDs in logs.
	spillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SpillTimeout)
	defer cancel()
	if spillErr := s.Spill.Append(spillCtx, payload); spillErr != nil {
		telemetry.IncAuditDropped(ev.Verb, "spill")
		slog.ErrorContext(
			ctx,
			"audit.spill_failed",
			slog.String("verb", ev.Verb),
			slog.String("event_id", ev.EventID.String()),
			slog.String("primary_err", primaryErr.Error()),
			slog.String("err", spillErr.Error()),
		)

		return fmt.Errorf(
			"audit spill after primary failure: %w",
			errors.Join(primaryErr, spillErr),
		)
	}

	telemetry.IncAuditSpilled(ev.Verb)
	count := s.spilled.Add(1)
	if s.inOutage.CompareAndSwap(false, true) {
		slog.ErrorContext(
			ctx,
			"audit.outage_began",
			slog.String("verb", ev.Verb),
			slog.String("event_id", ev.EventID.String()),
			slog.String("err", primaryErr.Error()),
		)

		return nil
	}

	slog.DebugContext(
		ctx,
		"audit.spilled",
		slog.String("verb", ev.Verb),
		slog.String("event_id", ev.EventID.String()),
		slog.Int64("spilled_this_outage", count),
	)

	return nil
}

// noteRecovered ends an outage on the first primary success and logs the
// number of events that spilled during that outage.
func (s *SpillRecorder) noteRecovered(ctx context.Context) {
	if !s.inOutage.CompareAndSwap(true, false) {
		return
	}

	slog.InfoContext(ctx, "audit.outage_ended", slog.Int64("spilled", s.spilled.Swap(0)))
}

// Close closes Primary when it supports closing.
//
// The method returns a wrapped error when Primary.Close returns an error.
//
//   - A primary with a Close method that does not return an error also closes.
//   - A primary without a Close method does not require closing.
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
