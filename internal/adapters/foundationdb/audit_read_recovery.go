package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// RecoverExpired saves an incomplete event and receipt for each expired
// pending read in bucket, up to limit reads. Each read uses its own
// transaction, which compares the generation and indexed deadline and
// increments the generation before it saves the event. Recovery never reruns
// the read.
func (s *AuditReadStore) RecoverExpired(ctx context.Context, bucket uint16, limit int) (int, error) {
	if bucket >= audit.ReadRecoveryBuckets {
		return 0, auditReadFailure(ctx, "recover audit reads", uuid.Nil, fmt.Errorf("bucket %d is out of range", bucket))
	}
	var candidates []fdb.KeyValue
	err := transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		due := auditReadDueRange(keyAuditReadExpiry, bucket, s.clock.Now())
		keyValues, err := tr.Snapshot().GetRange(due, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
		if err != nil {
			return auditReadStepFailure("read audit read expiry keys", err)
		}
		candidates = keyValues
		return nil
	})
	if err != nil {
		return 0, auditReadFailure(ctx, "scan expired audit reads", uuid.Nil, err)
	}
	recovered := 0
	for _, candidate := range candidates {
		deadline, eventID, err := unpackAuditReadTimeKey(candidate.Key)
		if err != nil {
			return recovered, auditReadFailure(ctx, "decode an audit read expiry key", uuid.Nil, err)
		}
		saved, err := s.recoverOne(ctx, bucket, deadline, eventID)
		if err != nil {
			return recovered, auditReadFailure(ctx, "recover audit read", eventID, err)
		}
		if saved {
			recovered++
		}
	}
	return recovered, nil
}

// recoverOne recovers one candidate. It clears an expiry key that no longer
// matches its pending read and skips a lease that a renewal extended.
func (s *AuditReadStore) recoverOne(ctx context.Context, bucket uint16, deadline int64, eventID uuid.UUID) (bool, error) {
	saved := false
	err := transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		saved = false
		pending, found, err := readAuditRecord[auditReadPending](tr, auditReadPendingKey(eventID))
		if err != nil {
			return err
		}
		if !found || pending.LeaseDeadline.UnixNano() != deadline {
			tr.Clear(auditReadExpiryKey(bucket, time.Unix(0, deadline), eventID))
			return nil
		}
		now := s.clock.Now()
		if pending.LeaseDeadline.After(now) {
			return nil
		}
		var template audit.Event
		if err := json.Unmarshal(pending.Template, &template); err != nil {
			return auditReadStepFailure("decode the pending template", err)
		}
		event, encoded, err := audit.EncodeIncompleteRead(template, now)
		if err != nil {
			return auditReadStepFailure("encode the incomplete event", err)
		}
		if err := s.outbox.writeEncoded(tr, encoded); err != nil {
			return err
		}
		clearPending(tr, eventID, pending)
		saved = true
		return writeAuditRecord(tr, auditReadReceiptKey(eventID), auditReadReceipt{
			Owner: pending.Owner, Generation: pending.Generation + 1, Digest: encoded.TransportDigest,
			Outcome: event.Outcome,
		})
	})
	return saved, err
}

// CleanupPublished deletes up to limit receipts in bucket that the relay
// published at least audit.ReadReceiptRetention ago. It deletes only
// published receipts.
func (s *AuditReadStore) CleanupPublished(ctx context.Context, bucket uint16, limit int) (int, error) {
	if bucket >= audit.ReadRecoveryBuckets {
		return 0, auditReadFailure(ctx, "clean up audit read receipts", uuid.Nil, fmt.Errorf("bucket %d is out of range", bucket))
	}
	deleted := 0
	err := transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		deleted = 0
		due := auditReadDueRange(keyAuditReadCleanup, bucket, s.clock.Now().Add(-audit.ReadReceiptRetention))
		entries, err := tr.GetRange(due, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
		if err != nil {
			return auditReadStepFailure("read audit read cleanup keys", err)
		}
		for _, entry := range entries {
			published, eventID, err := unpackAuditReadTimeKey(entry.Key)
			if err != nil {
				return err
			}
			receipt, found, err := readAuditRecord[auditReadReceipt](tr, auditReadReceiptKey(eventID))
			if err != nil {
				return err
			}
			if found && !receipt.PublishedAt.IsZero() && receipt.PublishedAt.UnixNano() == published {
				tr.Clear(auditReadReceiptKey(eventID))
				deleted++
			}
			tr.Clear(entry.Key)
		}
		return nil
	})
	if err != nil {
		return 0, auditReadFailure(ctx, "clean up audit read receipts", uuid.Nil, err)
	}
	return deleted, nil
}

// unpackAuditReadTimeKey returns the time and event ID of an expiry or
// cleanup key.
func unpackAuditReadTimeKey(key fdb.Key) (int64, uuid.UUID, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return 0, uuid.Nil, auditReadStepFailure("unpack audit read key", err)
	}
	if len(values) != 4 {
		return 0, uuid.Nil, fmt.Errorf("audit read key has %d tuple values, want 4", len(values))
	}
	instant, isInstant := values[2].(int64)
	text, isText := values[3].(string)
	if !isInstant || !isText {
		return 0, uuid.Nil, errors.New("audit read key does not end with a time and an event ID")
	}
	eventID, err := uuid.Parse(text)
	if err != nil {
		return 0, uuid.Nil, auditReadStepFailure("parse audit read key event ID", err)
	}
	return instant, eventID, nil
}

// transactAuditRead runs apply and commits it, retrying each retryable
// FoundationDB error. Each attempt limits its timeout to
// auditReadTransactionTimeout and the time left before the context deadline.
// apply errors that are not FoundationDB errors end the loop unchanged.
func transactAuditRead(ctx context.Context, db fdb.Database, apply func(fdb.Transaction) error) error {
	transaction, err := db.CreateTransaction()
	if err != nil {
		slog.ErrorContext(ctx, "audit.read_lifecycle.transaction_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create audit read transaction: %w", err)
	}
	defer transaction.Cancel()
	for {
		if err := ctx.Err(); err != nil {
			slog.ErrorContext(ctx, "audit.read_lifecycle.canceled", slog.String("err", err.Error()))
			return fmt.Errorf("audit read transaction: %w", err)
		}
		timeout := auditReadTransactionTimeout
		if deadline, exists := ctx.Deadline(); exists {
			timeout = max(time.Millisecond, min(timeout, deadline.Sub(clock.Now())))
		}
		if err := transaction.Options().SetTimeout(max(int64(timeout/time.Millisecond), 1)); err != nil {
			slog.ErrorContext(ctx, "audit.read_lifecycle.timeout_failed", slog.String("err", err.Error()))
			return fmt.Errorf("set audit read transaction timeout: %w", err)
		}
		err := apply(transaction)
		if err == nil {
			if commitErr := transaction.Commit().Get(); commitErr != nil {
				err = auditReadStepFailure("commit audit read transaction", commitErr)
			}
		}
		if err == nil {
			return nil
		}
		var databaseError fdb.Error
		if !errors.As(err, &databaseError) {
			return err
		}
		if retryErr := transaction.OnError(databaseError).Get(); retryErr != nil {
			slog.ErrorContext(ctx, "audit.read_lifecycle.retry_failed", slog.String("err", retryErr.Error()))
			return fmt.Errorf("retry audit read transaction: %w", retryErr)
		}
	}
}
