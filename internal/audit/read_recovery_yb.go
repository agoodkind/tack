package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// expiredRead contains a read that ops_read_expired selected after its lease expired.
type expiredRead struct {
	eventID       uuid.UUID
	generation    uint64
	leaseDeadline time.Time
	template      []byte
	observedAt    time.Time
}

// RecoverExpired uses ops_read_recover to save an incomplete event for each
// read with an expired lease in bucket. The database function checks the generation
// and lease deadline, then increments the generation and saves the event in one transaction.
func (l *PoolReadLifecycle) RecoverExpired(ctx context.Context, bucket uint16, limit int) (int, error) {
	candidates, err := l.expiredReads(ctx, bucket, limit)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, expired := range candidates {
		saved, err := l.recoverOne(ctx, expired)
		if err != nil {
			return recovered, err
		}
		if saved {
			recovered++
		}
	}
	return recovered, nil
}

// expiredReads returns up to limit reads with expired leases in bucket.
func (l *PoolReadLifecycle) expiredReads(ctx context.Context, bucket uint16, limit int) ([]expiredRead, error) {
	var candidates []expiredRead
	err := l.onCheckedConnection(ctx, func(connection *pgx.Conn) error {
		rows, err := connection.Query(ctx, readStatement("expired"), int(bucket), limit)
		if err != nil {
			return statementFailure(ctx, "expired", err)
		}
		defer rows.Close()
		for rows.Next() {
			var found expiredRead
			if err := rows.Scan(&found.eventID, &found.generation, &found.leaseDeadline, &found.template, &found.observedAt); err != nil {
				return statementFailure(ctx, "expired", err)
			}
			candidates = append(candidates, found)
		}
		if err := rows.Err(); err != nil {
			return statementFailure(ctx, "expired", err)
		}
		return nil
	})
	if err != nil {
		return nil, readLifecycleFailure(ctx, "scan expired audit reads", uuid.Nil, err)
	}
	return candidates, nil
}

// recoverOne creates and saves an incomplete event through ops_read_recover.
// It returns false if the candidate changed before recovery.
func (l *PoolReadLifecycle) recoverOne(ctx context.Context, expired expiredRead) (bool, error) {
	var template Event
	if err := json.Unmarshal(expired.template, &template); err != nil {
		return false, readLifecycleFailure(ctx, "decode audit read template", expired.eventID, err)
	}
	_, encoded, err := EncodeIncompleteRead(template, expired.observedAt)
	if err != nil {
		return false, readLifecycleFailure(ctx, "recover audit read", expired.eventID, err)
	}
	header, parts, err := splitOutboxRecords(encoded)
	if err != nil {
		return false, readLifecycleFailure(ctx, "encode audit read event", expired.eventID, err)
	}
	var saved bool
	err = l.onCheckedConnection(ctx, func(connection *pgx.Conn) error {
		return connection.QueryRow(ctx, readStatement("recover"), expired.eventID, expired.generation,
			expired.leaseDeadline, encoded.TransportDigest, header, parts).Scan(&saved)
	})
	if err != nil {
		return false, readLifecycleFailure(ctx, "recover audit read", expired.eventID, err)
	}
	return saved, nil
}

// CleanupPublished uses ops_read_cleanup to delete up to limit receipts in bucket
// after ReadReceiptRetention has elapsed since publication.
// It returns the number of receipts it deletes.
func (l *PoolReadLifecycle) CleanupPublished(ctx context.Context, bucket uint16, limit int) (int, error) {
	var deleted int
	err := l.onCheckedConnection(ctx, func(connection *pgx.Conn) error {
		return connection.QueryRow(ctx, readStatement("cleanup"), int(bucket), limit).Scan(&deleted)
	})
	if err != nil {
		return 0, readLifecycleFailure(ctx, "clean up audit read receipts", uuid.Nil, err)
	}
	return deleted, nil
}
