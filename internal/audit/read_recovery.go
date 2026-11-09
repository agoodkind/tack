package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// IncompleteReadEvent returns an incomplete event for a read with an expired
// lease.
//
// The event uses the later of completedAt and the read's start time as its
// completion time. Recovery clears read evidence and Delta and records the
// expired-owner error.
//
//   - A completion time before the start time selects the start time.
//   - A later completion time uses completedAt in UTC.
func IncompleteReadEvent(template Event, completedAt time.Time) Event {
	event := template
	event.Context.CompletedAt = completedAt.UTC()
	if event.Context.CompletedAt.Before(template.Context.StartedAt) {
		event.Context.CompletedAt = template.Context.StartedAt
	}
	event.Context.Read = nil
	event.Delta = nil
	event.Outcome = OutcomeIncomplete
	event.Error = &EventError{
		Code:    readRecoveryErrorCode,
		Message: "the read owner stopped renewing before it saved a final event",
	}
	return event
}

// EncodeIncompleteRead returns the encoded recovery event and its digest.
//
// The function builds the incomplete event, validates it, and encodes it
// without returned targets. A validation or encoding failure returns an error.
//
//	event, encoded, err := EncodeIncompleteRead(template, observedAt)
//	digest := encoded.TransportDigest
func EncodeIncompleteRead(template Event, completedAt time.Time) (Event, EncodedEvent, error) {
	event := IncompleteReadEvent(template, completedAt)
	if err := ValidateEvent(event); err != nil {
		return Event{}, EncodedEvent{}, fmt.Errorf("recover audit read %s: %s", template.EventID, err.Error())
	}
	encoded, err := EncodeEvent(event, nil)
	if err != nil {
		return Event{}, EncodedEvent{}, err
	}
	return event, encoded, nil
}

// RunReadRecovery recovers expired read leases and deletes old published
// receipts in every bucket until ctx signals cancellation.
//
// Each application instance runs recovery without a shared lock. The loop
// chooses a random starting bucket and waits ReadRenewInterval after each
// complete scan.
//
//  1. The scan visits every bucket once from the chosen starting bucket.
//  2. Each bucket attempts recovery before receipt cleanup.
func RunReadRecovery(ctx context.Context, store ReadLifecycle) {
	start := ReadBucket(uuid.New())
	for ctx.Err() == nil {
		for offset := range uint16(ReadRecoveryBuckets) {
			recoverReadBucket(ctx, store, (start+offset)%ReadRecoveryBuckets)
		}
		timer := time.NewTimer(ReadRenewInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// recoverReadBucket attempts recovery followed by cleanup for bucket. It
// recovers a panic from either call, but a recovery panic prevents cleanup in
// that invocation. The store logs failures.
func recoverReadBucket(ctx context.Context, store ReadLifecycle, bucket uint16) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(ctx, "audit.read_recovery.panicked",
				slog.Int("bucket", int(bucket)), slog.String("err", fmt.Sprint(recovered)))
		}
	}()
	recovered, recoverErr := store.RecoverExpired(ctx, bucket, ReadRecoveryScanLimit)
	if recoverErr == nil && recovered > 0 {
		slog.InfoContext(ctx, "audit.read_recovery.recovered", slog.Int("bucket", int(bucket)), slog.Int("count", recovered))
	}
	deleted, cleanupErr := store.CleanupPublished(ctx, bucket, ReadCleanupBatch)
	if cleanupErr == nil && deleted > 0 {
		slog.DebugContext(ctx, "audit.read_receipt.cleaned", slog.Int("bucket", int(bucket)), slog.Int("count", deleted))
	}
}

// KeepReadOwned renews the read lease every ReadRenewInterval until ctx
// signals cancellation or the returned stop function runs.
//
// The stop function cancels renewal and waits for its result. It returns the
// first renewal error, including ErrReadOwnerLost after recovery increments
// the generation.
//
//	stopRenewal := KeepReadOwned(ctx, store, token)
//	renewalError := stopRenewal()
func KeepReadOwned(ctx context.Context, store ReadLifecycle, token ReadToken) func() error {
	renewContext, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "audit.read_renewal.panicked", slog.String("err", fmt.Sprint(recovered)))
				result <- fmt.Errorf("renew audit read %s: renewal stopped unexpectedly", token.EventID)
			}
		}()
		result <- renewUntilDone(renewContext, store, token)
	}()
	return func() error {
		cancel()
		return <-result
	}
}

// renewUntilDone renews the read lease on each tick and returns the first
// error. It returns nil when ctx signals cancellation.
func renewUntilDone(ctx context.Context, store ReadLifecycle, token ReadToken) error {
	ticker := time.NewTicker(ReadRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := store.RenewRead(ctx, token); err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "audit.read_renewal.failed",
					slog.String("event_id", token.EventID.String()), slog.String("err", err.Error()))
				return fmt.Errorf("renew audit read %s: %w", token.EventID, err)
			}
		}
	}
}
