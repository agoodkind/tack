package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// rewriteShardRows appends one shard's nil-org rows onto the target chain and
// advances the target head. It returns how many rows it rewrote.
func rewriteShardRows(ctx context.Context, tx pgx.Tx, target uuid.UUID, shard int16, realHead chainHeadState) (int64, error) {
	rows, err := nilShardRows(ctx, tx, shard)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	seq := realHead.LastSeq
	prev := realHead.LastHash
	batch := &pgx.Batch{}
	for _, row := range rows {
		seq++
		rowHash, err := rehashMovedRow(row, target, seq, prev)
		if err != nil {
			return 0, err
		}
		batch.Queue(`
			UPDATE audit.events
			SET org_id = $1, seq = $2, prev_hash = $3, row_hash = $4, hash_version = $5
			WHERE org_id = $6 AND shard = $7 AND event_time = $8 AND seq = $9 AND event_id = $10
		`, target, seq, prev, rowHash, auditHashVersionCurrent,
			uuid.Nil, shard, row.EventTime, row.Seq, row.EventID)
		prev = rowHash
	}
	if err := sendMoveBatch(ctx, tx, batch, shard); err != nil {
		return 0, err
	}
	if err := writeChainHead(ctx, tx, target, shard, realHead, seq, prev); err != nil {
		return 0, err
	}
	return int64(len(rows)), nil
}

// sendMoveBatch runs the queued row updates and demands exactly one row per
// update: a miss means a concurrent writer touched the chain, so the shard
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

// nilShardRows reads one shard's nil-org rows in their original chain order.
func nilShardRows(ctx context.Context, tx pgx.Tx, shard int16) ([]Row, error) {
	rows, err := tx.Query(ctx, `
		SELECT org_id, event_time, event_id, seq, shard,
		       actor_id, actor_kind, action, outcome, entity_kind, entity_id,
		       context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
		       idempotency_key
		FROM audit.events
		WHERE org_id = $1 AND shard = $2
		ORDER BY seq
	`, uuid.Nil, shard)
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
