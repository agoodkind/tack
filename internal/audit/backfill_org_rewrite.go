package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// moveChunk rewrites up to moveChunkRows of one shard inside one transaction:
// run the caller's premise guard, read both heads, read the next chunk of the
// source org's rows in chain order, append them to the target chain, and
// advance the target head with the usual compare-and-swap. The chunk that
// observes the shard empty removes the source head under the same guard, so a
// row appended concurrently is never orphaned, and reports the shard done.
func (b *OrgBackfill) moveChunk(ctx context.Context, source, target uuid.UUID, shard int16, guard ShardGuard) (int64, bool, error) {
	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.begin_failed", slog.String("err", err.Error()))
		return 0, false, fmt.Errorf("audit org backfill begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sourceHead, err := readChainHead(ctx, tx, source, shard)
	if err != nil {
		return 0, false, err
	}
	realHead, err := readChainHead(ctx, tx, target, shard)
	if err != nil {
		return 0, false, err
	}
	rows, err := sourceShardRows(ctx, tx, source, shard)
	if err != nil {
		return 0, false, err
	}
	if guard != nil {
		if err := guard(ctx, tx, rows); err != nil {
			return 0, false, err
		}
	}
	done := len(rows) == 0
	if done {
		if sourceHead.Exists {
			if err := deleteSourceHead(ctx, tx, source, shard, sourceHead); err != nil {
				return 0, false, err
			}
		}
	} else if err := rewriteShardRows(ctx, tx, source, target, shard, realHead, rows); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.backfill.commit_failed", slog.String("err", err.Error()))
		return 0, false, fmt.Errorf("audit org backfill commit: %w", err)
	}
	return int64(len(rows)), done, nil
}

// deleteSourceHead removes the drained source chain's head, guarded on the
// last_seq this transaction read: a concurrent append to the source chain
// makes the guard miss, which surfaces as a conflict and reruns the chunk.
func deleteSourceHead(ctx context.Context, tx pgx.Tx, source uuid.UUID, shard int16, head chainHeadState) error {
	tag, err := tx.Exec(ctx, `
		DELETE FROM audit.chain_heads
		WHERE org_id = $1 AND shard = $2 AND last_seq = $3
	`, source, shard, head.LastSeq)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.head_delete_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit org backfill head delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errChainConflict
	}
	return nil
}

// rewriteShardRows appends one chunk of the source org's rows onto the target
// chain and advances the target head.
func rewriteShardRows(ctx context.Context, tx pgx.Tx, source, target uuid.UUID, shard int16, realHead chainHeadState, rows []Row) error {
	seq := realHead.LastSeq
	prev := realHead.LastHash
	batch := &pgx.Batch{}
	for _, row := range rows {
		seq++
		rowHash, err := rehashMovedRow(row, target, seq, prev)
		if err != nil {
			return err
		}
		batch.Queue(`
			UPDATE audit.events
			SET org_id = $1, seq = $2, prev_hash = $3, row_hash = $4, hash_version = $5
			WHERE org_id = $6 AND shard = $7 AND event_time = $8 AND seq = $9 AND event_id = $10
		`, target, seq, prev, rowHash, auditHashVersionCurrent,
			source, shard, row.EventTime, row.Seq, row.EventID)
		prev = rowHash
	}
	if err := sendMoveBatch(ctx, tx, batch, shard); err != nil {
		return err
	}
	return writeChainHead(ctx, tx, target, shard, realHead, seq, prev)
}

// rehashMovedRow computes the moved row's current-version hash at its new
// chain position. The stored context and delta columns are untouched history:
// the hash canonicalizes them by parsed value, exactly as verification does,
// so the recomputed hash verifies from the read-back row in one try.
func rehashMovedRow(row Row, target uuid.UUID, seq int64, prev []byte) ([]byte, error) {
	row.OrgID = target
	piiRef := uuid.Nil
	if row.PIIRef != nil {
		piiRef = *row.PIIRef
	}
	contextJSON, err := json.Marshal(row.Context)
	if err != nil {
		slog.Error("audit.backfill.context_encode_failed",
			slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org backfill row %s context: %w", row.EventID, err)
	}
	deltaJSON, err := json.Marshal(row.Delta)
	if err != nil {
		slog.Error("audit.backfill.delta_encode_failed",
			slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org backfill row %s delta: %w", row.EventID, err)
	}
	return hashRowForEvent(rowHashInput{
		Event:   exportEvent(row),
		EventID: row.EventID, Shard: row.Shard, Seq: seq,
		PIIRef: piiRef, ContextJSON: contextJSON, DeltaJSON: deltaJSON,
		LastHash: prev, Version: auditHashVersionCurrent,
	})
}

// sendMoveBatch runs the queued row updates and demands exactly one row per
// update: a miss means a concurrent writer touched the chain, so the chunk
// reruns rather than committing a partial move.
func sendMoveBatch(ctx context.Context, tx pgx.Tx, batch *pgx.Batch, shard int16) error {
	results := tx.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()
	for i := range batch.Len() {
		tag, err := results.Exec()
		if err != nil {
			slog.ErrorContext(ctx, "audit.backfill.row_update_failed",
				slog.Int("shard", int(shard)), slog.Int("index", i), slog.String("err", err.Error()))
			return fmt.Errorf("audit org backfill shard %d row update: %w", shard, err)
		}
		if tag.RowsAffected() != 1 {
			return errChainConflict
		}
	}
	return nil
}

// sourceShardRows reads the next chunk of one shard's source-org rows in
// their original chain order.
func sourceShardRows(ctx context.Context, tx pgx.Tx, source uuid.UUID, shard int16) ([]Row, error) {
	rows, err := tx.Query(ctx, `
		SELECT org_id, event_time, event_id, seq, shard,
		       actor_id, actor_kind, action, outcome, entity_kind, entity_id,
		       context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
		       idempotency_key
		FROM audit.events
		WHERE org_id = $1 AND shard = $2
		ORDER BY seq
		LIMIT $3
	`, source, shard, moveChunkRows)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.row_select_failed",
			slog.Int("shard", int(shard)), slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org backfill shard %d select: %w", shard, err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		row, err := readAuditRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.backfill.row_rows_failed",
			slog.Int("shard", int(shard)), slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org backfill shard %d rows: %w", shard, err)
	}
	return out, nil
}
