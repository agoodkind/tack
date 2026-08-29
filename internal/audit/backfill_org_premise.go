package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
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
// move. The options carry the TACK-462 operator exemptions: an absorbed org
// stops counting as foreign because its rows are about to move too, and an
// acknowledged actor's nil-org rows are attributed by the operator's hand.
func (b *OrgBackfill) DeriveSoleOrg(ctx context.Context, opts OrgBackfillOptions) (uuid.UUID, error) {
	if err := opts.Validate(); err != nil {
		return uuid.Nil, err
	}
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
	absorbed := make(map[uuid.UUID]bool, len(opts.AbsorbOrgs))
	for _, org := range opts.AbsorbOrgs {
		if org == target {
			return uuid.Nil, fmt.Errorf("audit org backfill: --absorb-org %s is the target org itself", org)
		}
		absorbed[org] = true
	}
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
		if org == SystemOrgID() || absorbed[org] {
			continue
		}
		if org != target {
			return uuid.Nil, fmt.Errorf("audit org backfill: ledger names org %s besides the sole member org %s", org, target)
		}
	}
	if err := b.verifyNilActorsBelong(ctx, target, opts.acknowledgedSet()); err != nil {
		return uuid.Nil, err
	}
	return target, nil
}

// verifyNilActorsBelong checks the durable history the ledger itself holds:
// every user actor behind a nil-org row must be a member of the target org
// today, unless the operator acknowledged that actor by hand. An org deleted
// from org_members whose events were all nil-stamped is invisible to the
// membership and ledger-org checks, but its actors are not, so the move
// refuses rather than absorbing a stranger's history.
func (b *OrgBackfill) verifyNilActorsBelong(ctx context.Context, target uuid.UUID, acknowledged map[uuid.UUID]bool) error {
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
	nonMembers, err := collectUUIDs(rows)
	if err != nil {
		return fmt.Errorf("audit org backfill nil-org actors: %w", err)
	}
	strangers := make([]uuid.UUID, 0, len(nonMembers))
	for _, actor := range nonMembers {
		if !acknowledged[actor] {
			strangers = append(strangers, actor)
		}
	}
	if len(strangers) > 0 {
		return fmt.Errorf(
			"audit org backfill: %d nil-org actor(s) are not members of %s (first: %s): their history cannot be attributed to the sole org",
			len(strangers), target, strangers[0])
	}
	return nil
}
