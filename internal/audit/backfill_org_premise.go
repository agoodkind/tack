package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// memberOrgsQuery and ledgerOrgsQuery are the two premise reads; the guard
// and the derivation share them verbatim.
const (
	memberOrgsQuery = `SELECT DISTINCT org_id FROM org_members`
	ledgerOrgsQuery = `SELECT DISTINCT org_id FROM audit.events WHERE org_id <> '00000000-0000-0000-0000-000000000000'`
)

// DeriveSoleOrg resolves the deployment's sole org and refuses when the
// sole-org premise does not hold: the mapping "every nil row belongs to this
// org" is only provable when no other org has ever existed here. Operator
// commands record under the reserved system org, never a customer org and
// never nil, so system-org rows neither weaken the premise nor belong to the
// move.
func (b *OrgBackfill) DeriveSoleOrg(ctx context.Context) (uuid.UUID, error) {
	memberRows, err := b.pool.Query(ctx, memberOrgsQuery)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.member_orgs_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("audit org backfill member orgs: %w", err)
	}
	memberOrgs, err := collectUUIDs(memberRows)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit org backfill member orgs: %w", err)
	}
	if len(memberOrgs) != 1 {
		return uuid.Nil, fmt.Errorf("audit org backfill: org_members holds %d orgs, need exactly 1", len(memberOrgs))
	}
	target := memberOrgs[0]
	ledgerRows, err := b.pool.Query(ctx, ledgerOrgsQuery)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.ledger_orgs_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("audit org backfill ledger orgs: %w", err)
	}
	ledgerOrgs, err := collectUUIDs(ledgerRows)
	if err != nil {
		return uuid.Nil, fmt.Errorf("audit org backfill ledger orgs: %w", err)
	}
	for _, org := range ledgerOrgs {
		if org == SystemOrgID() {
			continue
		}
		if org != target {
			return uuid.Nil, fmt.Errorf("audit org backfill: ledger names org %s besides the sole member org %s", org, target)
		}
	}
	if err := b.verifyNilActorsBelong(ctx, target); err != nil {
		return uuid.Nil, err
	}
	return target, nil
}

// verifyNilActorsBelong checks the durable history the ledger itself holds:
// every user actor behind a nil-org row must be a member of the target org
// today. An org deleted from org_members whose events were all nil-stamped
// is invisible to the membership and ledger-org checks, but its actors are
// not, so the move refuses rather than absorbing a stranger's history.
func (b *OrgBackfill) verifyNilActorsBelong(ctx context.Context, target uuid.UUID) error {
	rows, err := b.pool.Query(ctx, `
		SELECT DISTINCT e.actor_id FROM audit.events e
		WHERE e.org_id = '00000000-0000-0000-0000-000000000000'
		  AND e.actor_kind = 1
		  AND e.actor_id <> '00000000-0000-0000-0000-000000000000'
		  AND NOT EXISTS (
		      SELECT 1 FROM org_members m
		      WHERE m.user_id = e.actor_id AND m.org_id = $1
		  )`, target)
	if err != nil {
		slog.ErrorContext(ctx, "audit.backfill.nil_actors_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit org backfill nil-org actors: %w", err)
	}
	strangers, err := collectUUIDs(rows)
	if err != nil {
		return fmt.Errorf("audit org backfill nil-org actors: %w", err)
	}
	if len(strangers) > 0 {
		return fmt.Errorf(
			"audit org backfill: %d nil-org actor(s) are not members of %s (first: %s): their history cannot be attributed to the sole org",
			len(strangers), target, strangers[0])
	}
	return nil
}

// SoleOrgGuard re-asserts the sole-org premise inside every chunk
// transaction, so a membership created mid-run aborts the move before the
// next chunk commits instead of silently attributing new history to the
// wrong org.
func SoleOrgGuard(target uuid.UUID) ShardGuard {
	return func(ctx context.Context, tx pgx.Tx) error {
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
