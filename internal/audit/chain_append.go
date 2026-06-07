package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// errChainConflict signals that a concurrent writer advanced the same
// (org, shard) chain head between this writer's head read and its head write.
// The caller must roll back the transaction and retry the whole batch.
var errChainConflict = errors.New("audit chain head advanced concurrently")

// chainAppendInput carries the fields appendChainRow writes for one event.
type chainAppendInput struct {
	Event       Event
	EventID     uuid.UUID
	Shard       int16
	PIIRef      uuid.UUID
	ContextJSON []byte
	DeltaJSON   []byte
}

// chainAppendResult reports the sequence and hashes the append produced.
type chainAppendResult struct {
	Seq      int64
	PrevHash []byte
	RowHash  []byte
}

// appendChainRow inserts one event into audit.events and advances its
// (org, shard) head in audit.chain_heads with a compare-and-swap. The head
// write only succeeds when last_seq still equals the value this call read, so
// two writers on the same chain cannot both claim the same seq. The loser sees
// zero rows affected (read-committed) or a serialization failure (repeatable
// read), both of which the caller retries.
//
// It returns errAlreadyProjected when the (event_id, event_time) index rejects
// a redelivered event, and errChainConflict when the head moved underneath it.
func appendChainRow(ctx context.Context, tx pgx.Tx, in chainAppendInput) (chainAppendResult, error) {
	var lastSeq int64
	var lastHash []byte
	headExists := true
	err := tx.QueryRow(ctx, `
		SELECT last_seq, last_hash FROM audit.chain_heads
		WHERE org_id = $1 AND shard = $2
	`, in.Event.Context.OrgID, in.Shard).Scan(&lastSeq, &lastHash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		headExists = false
		lastSeq = 0
		lastHash = []byte{}
	case err != nil:
		slog.ErrorContext(ctx, "audit.chain.head_read_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("chain head read: %w", err)
	}
	if lastHash == nil {
		lastHash = []byte{}
	}
	seq := lastSeq + 1

	rowHash, err := hashRowForEvent(rowHashInput{
		Event: in.Event, EventID: in.EventID, Shard: in.Shard, Seq: seq,
		PIIRef: in.PIIRef, ContextJSON: in.ContextJSON, DeltaJSON: in.DeltaJSON, LastHash: lastHash,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.chain.hash_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("chain hash: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO audit.events (
			org_id, shard, event_time, event_id, seq,
			actor_id, actor_kind, action, entity_kind, entity_id,
			context, delta, pii_ref, prev_hash, row_hash, idempotency_key
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16
		)
		ON CONFLICT (event_id, event_time) DO NOTHING
	`,
		in.Event.Context.OrgID, in.Shard, in.Event.OccurredAt.UTC(), in.EventID, seq,
		in.Event.Actor.ID, actorKindCode(in.Event.Actor.Type), in.Event.Verb,
		in.Event.Entity.Type, in.Event.Entity.ID,
		in.ContextJSON, in.DeltaJSON, piiRefArg(in.PIIRef), lastHash, rowHash,
		in.Event.IdempotencyKey,
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.chain.event_insert_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("event insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return chainAppendResult{}, errAlreadyProjected
	}

	if headExists {
		tag, err = tx.Exec(ctx, `
			UPDATE audit.chain_heads
			SET last_seq = $3, last_hash = $4, updated_at = now()
			WHERE org_id = $1 AND shard = $2 AND last_seq = $5
		`, in.Event.Context.OrgID, in.Shard, seq, rowHash, lastSeq)
	} else {
		tag, err = tx.Exec(ctx, `
			INSERT INTO audit.chain_heads (org_id, shard, last_seq, last_hash, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (org_id, shard) DO NOTHING
		`, in.Event.Context.OrgID, in.Shard, seq, rowHash)
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.chain.head_write_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("chain head write: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return chainAppendResult{}, errChainConflict
	}

	return chainAppendResult{Seq: seq, PrevHash: lastHash, RowHash: rowHash}, nil
}

// isRetryableChainErr reports whether a failed chain append should be retried
// in a fresh transaction. It covers the explicit head-conflict sentinel and the
// Postgres serialization and deadlock codes a concurrent writer can raise.
func isRetryableChainErr(err error) bool {
	if errors.Is(err, errChainConflict) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}
