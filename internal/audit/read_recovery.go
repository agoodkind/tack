package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// IncompleteReadEvent returns an incomplete event for a read with an expired lease.
// It uses the later of completedAt and the read start time as the completion time.
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

// RunReadRecovery recovers reads with expired leases and deletes old published
// receipts in every bucket until ctx signals cancellation. It starts at a random
// bucket, visits each bucket once, and then waits ReadRenewInterval.
// Each application instance runs recovery without a shared lock.
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

// recoverReadBucket runs recovery and cleanup once for bucket and recovers
// panics from either call. The store logs each failure.
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

// KeepReadOwned renews the read lease every ReadRenewInterval until ctx signals
// cancellation or the returned stop function runs. The stop function returns the
// first renewal error, including ErrReadOwnerLost after recovery increments the generation.
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

// renewUntilDone renews the read lease on each tick and returns the first error.
// It returns nil when ctx signals cancellation.
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
