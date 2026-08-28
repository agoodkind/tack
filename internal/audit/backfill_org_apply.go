package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OrgBackfillMove reports what a move rewrote.
type OrgBackfillMove struct {
	// TargetOrg is the org every nil-org row now carries.
	TargetOrg uuid.UUID
	// RowsMoved is how many rows the move rewrote.
	RowsMoved int64
	// ShardsTouched is how many chains gained rows.
	ShardsTouched int
}

// moveShardAttempts bounds the per-shard retry loop. A retry only fires when
// a concurrent writer advanced one of the two chain heads mid-transaction.
const moveShardAttempts = 8

// MoveNilOrgRows appends every nil-org row onto the target org's per-shard
// chains. Rows already on the target's chains keep their seq and hashes
// byte-identical, so their notarizations stay valid; each moved row gets the
// next seq on its shard, a prev_hash linking the target's chain head, and a
// recomputed current-version row hash, in the moved rows' original per-shard
// order. The nil org's chain heads are removed as each shard drains. A rerun
// finds no nil rows and moves nothing.
func (b *OrgBackfill) MoveNilOrgRows(ctx context.Context, target uuid.UUID) (OrgBackfillMove, error) {
	move := OrgBackfillMove{TargetOrg: target, RowsMoved: 0, ShardsTouched: 0}
	if b == nil || b.pool == nil {
		return move, fmt.Errorf("audit org backfill not configured")
	}
	if target == uuid.Nil {
		return move, fmt.Errorf("audit org backfill: target org required")
	}
	shards, err := b.nilShards(ctx)
	if err != nil {
		return move, err
	}
	for _, shard := range shards {
		moved, err := b.moveShardWithRetry(ctx, target, shard)
		if err != nil {
			return move, err
		}
		if moved > 0 {
			move.RowsMoved += moved
			move.ShardsTouched++
		}
	}
	return move, nil
}

// nilShards lists the shards holding at least one nil-org row or chain head,
// so a head orphaned by a drained shard is still cleaned up.
func (b *OrgBackfill) nilShards(ctx context.Context) ([]int16, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT shard FROM audit.events WHERE org_id = $1
		UNION
		SELECT shard FROM audit.chain_heads WHERE org_id = $1
		ORDER BY shard
	`, uuid.Nil)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.shard_list_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org backfill shard list: %w", err)
	}
	defer rows.Close()
	var out []int16
	for rows.Next() {
		var shard int16
		if err := rows.Scan(&shard); err != nil {
			slog.ErrorContext(ctx, "audit.backfill.shard_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("audit org backfill shard scan: %w", err)
		}
		out = append(out, shard)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.backfill.shard_rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org backfill shard rows: %w", err)
	}
	return out, nil
}

// moveShardWithRetry runs one shard's move in a fresh transaction per
// attempt, retrying only the conflicts a concurrent chain writer can cause.
func (b *OrgBackfill) moveShardWithRetry(ctx context.Context, target uuid.UUID, shard int16) (int64, error) {
	var lastErr error
	for attempt := range moveShardAttempts {
		moved, err := b.moveShard(ctx, target, shard)
		if err == nil {
			return moved, nil
		}
		lastErr = err
		if !isRetryableChainErr(err) {
			return 0, err
		}
		slog.WarnContext(ctx, "audit.backfill.shard_retry",
			slog.Int("shard", int(shard)), slog.Int("attempt", attempt+1), slog.String("err", err.Error()))
	}
	return 0, fmt.Errorf("audit org backfill shard %d: retries exhausted: %w", shard, lastErr)
}

// moveShard rewrites one shard inside one transaction: read both heads, read
// the nil rows in chain order, append them to the target chain, advance the
// target head with the usual compare-and-swap, and remove the nil head with
// the same guard so a row appended concurrently is never orphaned.
func (b *OrgBackfill) moveShard(ctx context.Context, target uuid.UUID, shard int16) (int64, error) {
	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.begin_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("audit org backfill begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	nilHead, err := readChainHead(ctx, tx, uuid.Nil, shard)
	if err != nil {
		return 0, err
	}
	realHead, err := readChainHead(ctx, tx, target, shard)
	if err != nil {
		return 0, err
	}
	moved, err := rewriteShardRows(ctx, tx, target, shard, realHead)
	if err != nil {
		return 0, err
	}
	if nilHead.Exists {
		if err := deleteNilHead(ctx, tx, shard, nilHead); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.backfill.commit_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("audit org backfill commit: %w", err)
	}
	return moved, nil
}

// deleteNilHead removes the drained nil chain's head, guarded on the last_seq
// this transaction read: a concurrent append to the nil chain makes the guard
// miss, which surfaces as a conflict and reruns the shard.
func deleteNilHead(ctx context.Context, tx pgx.Tx, shard int16, head chainHeadState) error {
	tag, err := tx.Exec(ctx, `
		DELETE FROM audit.chain_heads
		WHERE org_id = $1 AND shard = $2 AND last_seq = $3
	`, uuid.Nil, shard, head.LastSeq)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.head_delete_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit org backfill head delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errChainConflict
	}
	return nil
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
