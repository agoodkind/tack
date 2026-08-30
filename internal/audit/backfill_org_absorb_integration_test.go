package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestOrgBackfillOptionsValidate pins the exemptions that can never be
// reviewed: the nil org and actor name nothing, the system org never moves,
// and naming the target itself is a contradiction.
func TestOrgBackfillOptionsValidate(t *testing.T) {
	if err := (OrgBackfillOptions{AbsorbOrgs: []uuid.UUID{uuid.Nil}, AcknowledgedActors: nil}).Validate(); err == nil {
		t.Fatal("nil absorb org must refuse")
	}
	if err := (OrgBackfillOptions{AbsorbOrgs: []uuid.UUID{SystemOrgID()}, AcknowledgedActors: nil}).Validate(); err == nil {
		t.Fatal("system absorb org must refuse")
	}
	if err := (OrgBackfillOptions{AbsorbOrgs: nil, AcknowledgedActors: []uuid.UUID{uuid.Nil}}).Validate(); err == nil {
		t.Fatal("nil acknowledged actor must refuse")
	}
	ok := OrgBackfillOptions{
		AbsorbOrgs:         []uuid.UUID{uuid.Must(uuid.NewV7())},
		AcknowledgedActors: []uuid.UUID{uuid.Must(uuid.NewV7())},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid options refused: %v", err)
	}
}

// TestDeriveSoleOrgWithExemptions pins the TACK-462 premise widening: an
// absorbed org stops counting as foreign, absorbing the target itself
// refuses, and an acknowledged actor's nil rows pass the membership check
// while an unacknowledged stranger still refuses.
func TestDeriveSoleOrgWithExemptions(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	purgeNilOrg(t, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM org_members`); err != nil {
		t.Fatalf("reset org_members: %v", err)
	}
	seedPremiseMember(t, pool, orgID, uuid.Must(uuid.NewV7()))
	backfill := &OrgBackfill{pool: pool}

	phantom := uuid.Must(uuid.NewV7())
	if err := appendWithRetry(ctx, pool, premiseEvent(t, phantom, uuid.Must(uuid.NewV7()), 41)); err != nil {
		t.Fatalf("seed phantom row: %v", err)
	}
	t.Cleanup(func() { purgeOrgChain(t, pool, phantom) })

	// Unabsorbed, the phantom org refuses; absorbed, it derives.
	if _, err := backfill.DeriveSoleOrg(ctx, OrgBackfillOptions{AbsorbOrgs: nil, AcknowledgedActors: nil}); err == nil || !strings.Contains(err.Error(), phantom.String()) {
		t.Fatalf("unabsorbed phantom err = %v, want a refusal naming %s", err, phantom)
	}
	target, err := backfill.DeriveSoleOrg(ctx, OrgBackfillOptions{AbsorbOrgs: []uuid.UUID{phantom}, AcknowledgedActors: nil})
	if err != nil {
		t.Fatalf("derive with absorbed phantom: %v", err)
	}
	if target != orgID {
		t.Fatalf("target = %s, want %s", target, orgID)
	}

	// Absorbing the target itself is a contradiction.
	if _, err := backfill.DeriveSoleOrg(ctx, OrgBackfillOptions{AbsorbOrgs: []uuid.UUID{orgID}, AcknowledgedActors: nil}); err == nil || !strings.Contains(err.Error(), "target org itself") {
		t.Fatalf("absorb-target err = %v, want the target refusal", err)
	}

	// An acknowledged stranger passes; an unacknowledged one still refuses.
	stranger := uuid.Must(uuid.NewV7())
	if err := appendWithRetry(ctx, pool, premiseEvent(t, uuid.Nil, stranger, 42)); err != nil {
		t.Fatalf("seed stranger nil row: %v", err)
	}
	t.Cleanup(func() { purgeNilOrg(t, pool) })
	opts := OrgBackfillOptions{AbsorbOrgs: []uuid.UUID{phantom}, AcknowledgedActors: nil}
	if _, err := backfill.DeriveSoleOrg(ctx, opts); err == nil || !strings.Contains(err.Error(), stranger.String()) {
		t.Fatalf("unacknowledged stranger err = %v, want a refusal naming %s", err, stranger)
	}
	opts.AcknowledgedActors = []uuid.UUID{stranger}
	if _, err := backfill.DeriveSoleOrg(ctx, opts); err != nil {
		t.Fatalf("derive with acknowledged stranger: %v", err)
	}
}

// TestNilActorRowsMoveUnderTheSoleOrgPremise pins why the chunk guard skips a
// user-kind row whose actor id is the all-zero UUID. Such a row names no
// actor, so no membership lookup can accept or refuse it; its only attribution
// basis is the sole-org premise the command already proved. Requiring the nil
// actor to be acknowledged instead would be unreachable by construction,
// because Validate refuses uuid.Nil as an acknowledgement, so those rows could
// never move at all.
func TestNilActorRowsMoveUnderTheSoleOrgPremise(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	purgeNilOrg(t, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM org_members`); err != nil {
		t.Fatalf("reset org_members: %v", err)
	}
	seedPremiseMember(t, pool, orgID, uuid.Must(uuid.NewV7()))
	t.Cleanup(func() { purgeNilOrg(t, pool) })
	backfill := &OrgBackfill{pool: pool}

	anonymous := premiseEvent(t, uuid.Nil, uuid.Nil, 44)
	if err := appendWithRetry(ctx, pool, anonymous); err != nil {
		t.Fatalf("seed nil-actor row: %v", err)
	}
	// The premise holds with no exemptions: an unnamed actor is not a
	// stranger, so the derivation does not refuse on it.
	if _, err := backfill.DeriveSoleOrg(ctx, OrgBackfillOptions{AbsorbOrgs: nil, AcknowledgedActors: nil}); err != nil {
		t.Fatalf("derive with a nil-actor nil-org row present: %v", err)
	}
	move, err := backfill.MoveOrgRows(ctx, uuid.Nil, orgID, SoleOrgGuard(orgID, nil))
	if err != nil {
		t.Fatalf("guarded move of the nil-actor row: %v", err)
	}
	if move.RowsMoved != 1 {
		t.Fatalf("moved %d rows, want the 1 nil-actor row", move.RowsMoved)
	}
	assertMovedRowsVerify(t, pool, orgID, []chainAppendInput{anonymous})

	// The skip is scoped to the unnamed actor: a named nonmember in the same
	// position still refuses, so the guard never became permissive.
	stranger := uuid.Must(uuid.NewV7())
	if err := appendWithRetry(ctx, pool, premiseEvent(t, uuid.Nil, stranger, 44)); err != nil {
		t.Fatalf("seed named stranger row: %v", err)
	}
	if _, err := backfill.MoveOrgRows(ctx, uuid.Nil, orgID, SoleOrgGuard(orgID, nil)); err == nil || !strings.Contains(err.Error(), stranger.String()) {
		t.Fatalf("named stranger err = %v, want a refusal naming %s", err, stranger)
	}
}

// TestMoveAbsorbedAndAcknowledgedRows pins the TACK-462 move: an absorbed
// org's rows land on the target chain with verifiable hashes and its heads
// vanish, an acknowledged stranger's nil rows move under SoleOrgGuard, and a
// rerun of both moves nothing.
func TestMoveAbsorbedAndAcknowledgedRows(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	purgeNilOrg(t, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM org_members`); err != nil {
		t.Fatalf("reset org_members: %v", err)
	}
	seedPremiseMember(t, pool, orgID, uuid.Must(uuid.NewV7()))
	backfill := &OrgBackfill{pool: pool}
	const shard int16 = 43

	phantom := uuid.Must(uuid.NewV7())
	stranger := uuid.Must(uuid.NewV7())
	phantomInputs := []chainAppendInput{
		premiseEvent(t, phantom, uuid.Must(uuid.NewV7()), shard),
		premiseEvent(t, phantom, uuid.Must(uuid.NewV7()), shard),
	}
	strangerInput := premiseEvent(t, uuid.Nil, stranger, shard)
	for _, in := range append(phantomInputs, strangerInput) {
		if err := appendWithRetry(ctx, pool, in); err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}
	t.Cleanup(func() { purgeOrgChain(t, pool, phantom) })
	t.Cleanup(func() { purgeNilOrg(t, pool) })

	nilMove, err := backfill.MoveOrgRows(ctx, uuid.Nil, orgID, SoleOrgGuard(orgID, []uuid.UUID{stranger}))
	if err != nil {
		t.Fatalf("acknowledged nil move: %v", err)
	}
	if nilMove.RowsMoved != 1 {
		t.Fatalf("acknowledged nil move moved %d rows, want 1", nilMove.RowsMoved)
	}
	absorbMove, err := backfill.MoveOrgRows(ctx, phantom, orgID, AbsorbedOrgGuard(orgID))
	if err != nil {
		t.Fatalf("absorb move: %v", err)
	}
	if absorbMove.RowsMoved != 2 {
		t.Fatalf("absorb move moved %d rows, want 2", absorbMove.RowsMoved)
	}

	if n := countRows(t, pool, `SELECT count(*) FROM audit.events WHERE org_id = $1`, phantom); n != 0 {
		t.Fatalf("phantom rows remaining = %d, want 0", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit.chain_heads WHERE org_id = $1`, phantom); n != 0 {
		t.Fatalf("phantom heads remaining = %d, want 0", n)
	}
	merged := readChain(t, pool, orgID, shard)
	if len(merged) != 3 {
		t.Fatalf("merged chain rows = %d, want 3", len(merged))
	}
	assertChainLinks(t, pool, orgID, shard, merged)
	assertMovedRowsVerify(t, pool, orgID, append(phantomInputs, strangerInput))

	againNil, err := backfill.MoveOrgRows(ctx, uuid.Nil, orgID, SoleOrgGuard(orgID, []uuid.UUID{stranger}))
	if err != nil {
		t.Fatalf("nil rerun: %v", err)
	}
	againAbsorb, err := backfill.MoveOrgRows(ctx, phantom, orgID, AbsorbedOrgGuard(orgID))
	if err != nil {
		t.Fatalf("absorb rerun: %v", err)
	}
	if againNil.RowsMoved != 0 || againAbsorb.RowsMoved != 0 {
		t.Fatalf("rerun moved %d + %d rows, want 0 + 0", againNil.RowsMoved, againAbsorb.RowsMoved)
	}
}
