package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SoleOrgGuard re-asserts the sole-org premise inside every chunk
// transaction, against the very rows about to move: org_members must still
// hold exactly the target org, and every user actor in the chunk must be a
// member of it or explicitly acknowledged. A membership created mid-run, or a
// late nil-org row from a nonmember, aborts before its chunk commits instead
// of silently attributing history to the wrong org.
func SoleOrgGuard(target uuid.UUID, acknowledged []uuid.UUID) ShardGuard {
	acked := make(map[uuid.UUID]bool, len(acknowledged))
	for _, actor := range acknowledged {
		acked[actor] = true
	}
	return func(ctx context.Context, tx pgx.Tx, chunk []Row) error {
		if err := verifyMembershipPremise(ctx, tx, target); err != nil {
			return err
		}
		return verifyChunkActors(ctx, tx, target, chunk, acked)
	}
}

// AbsorbedOrgGuard protects an absorbed org's move: the membership premise
// must still hold, but the chunk actors are not re-checked because the
// operator attributed the named org's rows wholesale, actors included.
func AbsorbedOrgGuard(target uuid.UUID) ShardGuard {
	return func(ctx context.Context, tx pgx.Tx, _ []Row) error {
		return verifyMembershipPremise(ctx, tx, target)
	}
}

// verifyMembershipPremise demands org_members still holds exactly the target,
// read in the transaction that is about to move rows.
func verifyMembershipPremise(ctx context.Context, tx pgx.Tx, target uuid.UUID) error {
	rows, err := tx.Query(ctx, memberOrgsQuery)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.guard_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit org backfill premise guard: %w", err)
	}
	orgs, err := collectUUIDs(rows)
	if err != nil {
		return fmt.Errorf("audit org backfill premise guard: %w", err)
	}
	if len(orgs) != 1 || orgs[0] != target {
		return fmt.Errorf("audit org backfill: sole-org premise broke mid-run: org_members holds %d orgs", len(orgs))
	}
	return nil
}

// verifyChunkActors demands that every user actor behind the chunk's rows is
// a member of the target org or acknowledged, read in the same transaction
// that moves them.
func verifyChunkActors(ctx context.Context, tx pgx.Tx, target uuid.UUID, chunk []Row, acknowledged map[uuid.UUID]bool) error {
	seen := map[uuid.UUID]bool{}
	actors := make([]uuid.UUID, 0, len(chunk))
	for _, row := range chunk {
		if row.ActorKind != 1 || row.ActorID == uuid.Nil || seen[row.ActorID] || acknowledged[row.ActorID] {
			continue
		}
		seen[row.ActorID] = true
		actors = append(actors, row.ActorID)
	}
	if len(actors) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT user_id FROM org_members
		WHERE org_id = $1 AND user_id = ANY($2)
	`, target, actors)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.chunk_actors_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit org backfill chunk actors: %w", err)
	}
	members, err := collectUUIDs(rows)
	if err != nil {
		return fmt.Errorf("audit org backfill chunk actors: %w", err)
	}
	isMember := map[uuid.UUID]bool{}
	for _, id := range members {
		isMember[id] = true
	}
	for _, id := range actors {
		if !isMember[id] {
			return fmt.Errorf("audit org backfill: chunk actor %s is not a member of %s: refusing to attribute their history", id, target)
		}
	}
	return nil
}

// collectUUIDs drains an already-opened single-UUID-column query result, so
// pool-backed and transaction-backed queries share one path.
func collectUUIDs(rows pgx.Rows) ([]uuid.UUID, error) {
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			slog.Error("audit.backfill.org_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("audit org scan: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		slog.Error("audit.backfill.org_rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit org rows: %w", err)
	}
	return out, nil
}
