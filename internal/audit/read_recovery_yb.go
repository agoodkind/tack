package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// expiredRead is one candidate that ops_read_expired returns.
type expiredRead struct {
	eventID       uuid.UUID
	generation    uint64
	leaseDeadline time.Time
	template      []byte
	observedAt    time.Time
}

// RecoverExpired saves an incomplete event for each expired read in bucket
// through ops_read_recover. The function compares the generation and lease
// deadline before it saves the event.
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

// expiredReads returns up to limit expired candidates in bucket.
func (l *PoolReadLifecycle) expiredReads(ctx context.Context, bucket uint16, limit int) ([]expiredRead, error) {
	rows, err := l.pool.Query(ctx, readStatement("expired"), int(bucket), limit)
	if err != nil {
		return nil, readLifecycleFailure(ctx, "scan expired audit reads", uuid.Nil, err)
	}
	defer rows.Close()
	var candidates []expiredRead
	for rows.Next() {
		var found expiredRead
		if err := rows.Scan(&found.eventID, &found.generation, &found.leaseDeadline, &found.template, &found.observedAt); err != nil {
			return nil, readLifecycleFailure(ctx, "scan expired audit reads", uuid.Nil, err)
		}
		candidates = append(candidates, found)
	}
	if err := rows.Err(); err != nil {
		return nil, readLifecycleFailure(ctx, "scan expired audit reads", uuid.Nil, err)
	}
	return candidates, nil
}

// recoverOne builds the incomplete event for one candidate and saves it
// through ops_read_recover. It returns false when the candidate changed.
func (l *PoolReadLifecycle) recoverOne(ctx context.Context, expired expiredRead) (bool, error) {
	var template Event
	if err := json.Unmarshal(expired.template, &template); err != nil {
		return false, readLifecycleFailure(ctx, "decode audit read template", expired.eventID, err)
	}
	event, encoded, err := EncodeIncompleteRead(template, expired.observedAt)
	if err != nil {
		return false, readLifecycleFailure(ctx, "recover audit read", expired.eventID, err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return false, readLifecycleFailure(ctx, "encode audit read event", expired.eventID, err)
	}
	var saved bool
	err = l.pool.QueryRow(ctx, readStatement("recover"), expired.eventID, expired.generation,
		expired.leaseDeadline, encoded.TransportDigest, payload).Scan(&saved)
	if err != nil {
		return false, readLifecycleFailure(ctx, "recover audit read", expired.eventID, err)
	}
	return saved, nil
}

// CleanupPublished deletes published receipts in bucket through
// ops_read_cleanup and returns the number it deleted.
func (l *PoolReadLifecycle) CleanupPublished(ctx context.Context, bucket uint16, limit int) (int, error) {
	var deleted int
	if err := l.pool.QueryRow(ctx, readStatement("cleanup"), int(bucket), limit).Scan(&deleted); err != nil {
		return 0, readLifecycleFailure(ctx, "clean up audit read receipts", uuid.Nil, err)
	}
	return deleted, nil
}
