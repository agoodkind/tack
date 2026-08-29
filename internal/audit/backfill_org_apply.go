package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OrgBackfillMove reports what a move rewrote.
type OrgBackfillMove struct {
	// SourceOrg is the org the moved rows carried before the move.
	SourceOrg uuid.UUID
	// TargetOrg is the org every moved row now carries.
	TargetOrg uuid.UUID
	// RowsMoved is how many rows the move rewrote.
	RowsMoved int64
	// ShardsTouched is how many chains gained rows.
	ShardsTouched int
	// Passes is how many full shard sweeps ran before the ledger held no
	// source-org row or head.
	Passes int
}

// ShardGuard runs inside every chunk transaction after the chunk's rows are
// read and before any of them move. The caller uses it to re-assert the
// premise that made the move safe against the very rows about to be
// rewritten (for the org backfill, that org_members still holds exactly the
// target org and that every actor in the chunk belongs to it), so a premise
// broken mid-run aborts before the broken chunk commits.
type ShardGuard func(ctx context.Context, tx pgx.Tx, chunk []Row) error

// moveShardAttempts bounds the per-chunk retry loop. A retry only fires when
// a concurrent writer advanced one of the two chain heads mid-transaction.
const moveShardAttempts = 8

// movePasses bounds the full-sweep loop. Source rows found on a later pass
// were appended during the run; with the auth-stamp fix deployed first that
// does not happen, so exhausting the bound means a live writer on the source
// org and the move fails loudly instead of reporting success over a growing
// remainder.
const movePasses = 3

// moveChunkRows caps how many rows one transaction rewrites, so the process
// memory and transaction size stay bounded by the chunk, not by the largest
// historical chain. A variable rather than a constant so the integration
// test can drive multi-chunk shards with a small corpus.
var moveChunkRows = 500

// MoveOrgRows appends every row of the source org (the nil org, or a
// TACK-462 absorbed org) onto the target org's per-shard chains. Rows already
// on the target's chains keep their seq and hashes byte-identical, so their
// notarizations stay valid; each moved row gets the next seq on its shard, a
// prev_hash linking the target's chain head, and a recomputed current-version
// row hash, in the moved rows' original per-shard order. Shards drain in
// bounded chunks, the source org's chain head is removed only by the pass
// that observes its shard empty, and full sweeps repeat until no source row
// or head remains. A rerun finds nothing and moves nothing.
func (b *OrgBackfill) MoveOrgRows(ctx context.Context, source, target uuid.UUID, guard ShardGuard) (OrgBackfillMove, error) {
	move := OrgBackfillMove{SourceOrg: source, TargetOrg: target, RowsMoved: 0, ShardsTouched: 0, Passes: 0}
	if b == nil || b.pool == nil {
		return move, errors.New("audit org backfill not configured")
	}
	if target == uuid.Nil {
		return move, errors.New("audit org backfill: target org required")
	}
	if source == target {
		return move, errors.New("audit org backfill: source and target org are the same")
	}
	touched := map[int16]bool{}
	for pass := range movePasses {
		shards, err := b.orgShards(ctx, source)
		if err != nil {
			return move, err
		}
		move.Passes = pass + 1
		if len(shards) == 0 {
			move.ShardsTouched = len(touched)
			return move, nil
		}
		for _, shard := range shards {
			moved, err := b.drainShard(ctx, source, target, shard, guard)
			if err != nil {
				return move, err
			}
			if moved > 0 {
				move.RowsMoved += moved
				touched[shard] = true
			}
		}
	}
	move.ShardsTouched = len(touched)
	plan, err := b.PlanOrgMove(ctx, source)
	if err != nil {
		return move, err
	}
	if plan.Rows > 0 {
		return move, fmt.Errorf(
			"audit org backfill: %d rows of org %s remain after %d passes: a live writer is still recording that org",
			plan.Rows, source, movePasses)
	}
	return move, nil
}

// orgShards lists the shards holding at least one source-org row or chain
// head, so a head orphaned by a drained shard is still cleaned up.
func (b *OrgBackfill) orgShards(ctx context.Context, source uuid.UUID) ([]int16, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT shard FROM audit.events WHERE org_id = $1
		UNION
		SELECT shard FROM audit.chain_heads WHERE org_id = $1
		ORDER BY shard
	`, source)
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

// drainShard moves one shard chunk by chunk until a chunk observes it empty
// and removes the source head. Each chunk is one transaction, so a crash
// leaves a consistent prefix moved and a rerun continues from the remainder.
func (b *OrgBackfill) drainShard(ctx context.Context, source, target uuid.UUID, shard int16, guard ShardGuard) (int64, error) {
	var total int64
	// Progress bound: a chunk either moves at least one row or finishes the
	// shard, so the shard cannot need more chunks than rows plus one.
	for {
		moved, done, err := b.moveChunkWithRetry(ctx, source, target, shard, guard)
		if err != nil {
			return total, err
		}
		total += moved
		if done {
			return total, nil
		}
	}
}

// moveChunkWithRetry runs one chunk in a fresh transaction per attempt,
// retrying only the conflicts a concurrent chain writer can cause.
func (b *OrgBackfill) moveChunkWithRetry(ctx context.Context, source, target uuid.UUID, shard int16, guard ShardGuard) (int64, bool, error) {
	var lastErr error
	for attempt := range moveShardAttempts {
		moved, done, err := b.moveChunk(ctx, source, target, shard, guard)
		if err == nil {
			return moved, done, nil
		}
		lastErr = err
		if !isRetryableChainErr(err) {
			return 0, false, err
		}
		slog.WarnContext(ctx, "audit.backfill.chunk_retry",
			slog.Int("shard", int(shard)), slog.Int("attempt", attempt+1), slog.String("err", err.Error()))
	}
	return 0, false, fmt.Errorf("audit org backfill shard %d: retries exhausted: %w", shard, lastErr)
}
