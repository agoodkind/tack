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

// SpillTimeout limits one spill to twice the FoundationDB transaction
// limit. A slow cluster gets one retry within that limit. A stuck cluster
// cannot keep the request open.
const SpillTimeout = 10 * time.Second

// OutboxAppender durably stores events the primary recorder refuses. The
// FoundationDB operator outbox implements it in production. The audit-consumer's
// relay publishes the stored events to Kafka.
type OutboxAppender interface {
	Append(ctx context.Context, event json.RawMessage) error
}

// SpillRecorder records through Primary and appends events to Spill when
// Primary fails. The outbox retains the events during a broker outage, and
// the relay publishes them after the broker recovers (TACK-336).
//
// The first spill logs at Error with the primary failure. Later spills in the
// outage log at Debug with a running count. The first primary success logs at
// Info with the total (TACK-320). Each failed spill logs at Error because
// neither recorder stored the event.
type SpillRecorder struct {
	Primary  Recorder
	Spill    OutboxAppender
	inOutage atomic.Bool
	spilled  atomic.Int64
}

// Record records through Primary and appends to the outbox on failure.
// A successful append returns nil because the outbox durably stores the event.
func (s *SpillRecorder) Record(ctx context.Context, ev Event) error {
	primaryErr := s.Primary.Record(ctx, ev)
	if primaryErr == nil {
		s.noteRecovered(ctx)
		return nil
	}

	return s.spill(ctx, ev, primaryErr)
}

// errBrokerRefused reports a batch produce refusal without a specific error.
var errBrokerRefused = errors.New("broker refused the produce")

// spill appends one event the primary refused to the outbox, logging once per
// outage. A successful spill returns nil because the outbox durably stores it.
func (s *SpillRecorder) spill(ctx context.Context, ev Event, primaryErr error) error {
	payload, marshalErr := MarshalEvent(ev)
	if marshalErr != nil {
		telemetry.IncAuditDropped(ev.Verb, "spill_marshal")
		return errors.Join(primaryErr, marshalErr)
	}

	// The primary can fail after request cancellation and a committed product
	// write. The spill uses its own deadline without the request's cancellation.
	// It retains context values for trace and request identifiers in logs.
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

// noteRecovered logs the first primary success after an outage with the count
// of events awaiting publication by the relay.
func (s *SpillRecorder) noteRecovered(ctx context.Context) {
	if !s.inOutage.CompareAndSwap(true, false) {
		return
	}

	slog.InfoContext(ctx, "audit.outage_ended", slog.Int64("spilled", s.spilled.Swap(0)))
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
