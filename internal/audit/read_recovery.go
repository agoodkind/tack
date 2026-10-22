package audit

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
)

// IncompleteReadEvent returns the event that recovery saves for an expired
// read. The completion time is the later of completedAt and the start time.
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

// RunReadRecovery recovers expired reads and deletes old published receipts
// in every bucket until ctx ends. It starts at a random bucket, visits each
// bucket once, and then waits ReadRenewInterval. Each application instance
// runs it without a shared lock.
func RunReadRecovery(ctx context.Context, store ReadLifecycle) {
	start := rand.IntN(ReadRecoveryBuckets) //nolint:gosec // The start bucket spreads instances and is not a secret.
	for ctx.Err() == nil {
		for offset := range ReadRecoveryBuckets {
			bucket := uint16((start + offset) % ReadRecoveryBuckets)
			recoverReadBucket(ctx, store, bucket)
		}
		timer := time.NewTimer(ReadRenewInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

// recoverReadBucket runs one recovery and one cleanup pass in bucket and
// recovers a panic in either pass. The store logs each failure.
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

// KeepReadOwned renews token every ReadRenewInterval until ctx ends or the
// returned stop function runs. Stop returns the first renewal error, such as
// ErrReadOwnerLost after recovery replaced the owner.
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

// renewUntilDone renews token on each tick and returns the first error.
// It returns nil when ctx ends.
func renewUntilDone(ctx context.Context, store ReadLifecycle, token ReadToken) error {
	ticker := time.NewTicker(ReadRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := store.RenewRead(ctx, token); err != nil && ctx.Err() == nil {
				return err
			}
		}
	}
}
