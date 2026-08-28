package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
)

// seedPremiseMember creates the given user with one org membership and
// registers a cleanup that removes both.
func seedPremiseMember(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	email := "premise-" + userID.String() + "@example.com"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email) VALUES ($1, $2)`, userID, email); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO org_members (org_id, user_id) VALUES ($1, $2)`, orgID, userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM org_members WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
}

// premiseEvent builds a chain input with a controlled actor, the way
// chainTestEvent does with a random one.
func premiseEvent(t *testing.T, orgID, actorID uuid.UUID, shard int16) chainAppendInput {
	t.Helper()
	ev := Event{
		EventID:    uuid.Must(uuid.NewV7()),
		Verb:       string(VerbNodeRead),
		Actor:      Actor{Type: ActorUser, ID: actorID},
		Entity:     Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context:    EventContext{OrgID: orgID, Source: SourceMCP},
		Outcome:    OutcomeOK,
		OccurredAt: clock.Now().UTC(),
	}
	contextJSON, err := json.Marshal(ev.Context)
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	return chainAppendInput{
		Event: ev, EventID: ev.EventID, Shard: shard,
		PIIRef: uuid.Nil, ContextJSON: contextJSON, DeltaJSON: []byte("null"),
	}
}

// purgeOrgChain removes one org's events and chain heads.
func purgeOrgChain(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM audit.events WHERE org_id = $1`, orgID)
	_, _ = pool.Exec(ctx, `DELETE FROM audit.chain_heads WHERE org_id = $1`, orgID)
}

// TestDeriveSoleOrg pins the sole-org premise: it accepts the one member org
// alongside the reserved system org's operator rows, refuses a foreign
// customer org in the ledger, refuses a nil-org actor who is no member (the
// phantom-org case), and refuses zero or two member orgs. The shared
// throwaway holds cross-suite state, so run the database suites separately
// rather than in one parallel go test invocation.
func TestDeriveSoleOrg(t *testing.T) {
	pool, orgID := chainTestPool(t)
	ctx := context.Background()
	purgeNilOrg(t, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM org_members`); err != nil {
		t.Fatalf("reset org_members: %v", err)
	}
	backfill := &OrgBackfill{pool: pool}

	// No member org: refused before any ledger reasoning.
	if _, err := backfill.DeriveSoleOrg(ctx); err == nil || !strings.Contains(err.Error(), "holds 0 orgs") {
		t.Fatalf("empty org_members err = %v, want the 0-orgs refusal", err)
	}

	actor := uuid.Must(uuid.NewV7())
	seedPremiseMember(t, pool, orgID, uuid.Must(uuid.NewV7()))
	if err := appendWithRetry(ctx, pool, premiseEvent(t, orgID, actor, 31)); err != nil {
		t.Fatalf("seed member-org row: %v", err)
	}
	if err := appendWithRetry(ctx, pool, premiseEvent(t, SystemOrgID(), actor, 31)); err != nil {
		t.Fatalf("seed system-org row: %v", err)
	}
	t.Cleanup(func() { purgeOrgChain(t, pool, SystemOrgID()) })

	// The happy path: one member org, ledger holding that org plus the
	// system org's operator rows, derives the member org.
	target, err := backfill.DeriveSoleOrg(ctx)
	if err != nil {
		t.Fatalf("derive with system-org rows present: %v", err)
	}
	if target != orgID {
		t.Fatalf("target = %s, want %s", target, orgID)
	}

	// A foreign customer org in the ledger breaks the sole-org premise.
	foreignOrg := uuid.Must(uuid.NewV7())
	if err := appendWithRetry(ctx, pool, premiseEvent(t, foreignOrg, actor, 32)); err != nil {
		t.Fatalf("seed foreign-org row: %v", err)
	}
	if _, err := backfill.DeriveSoleOrg(ctx); err == nil || !strings.Contains(err.Error(), foreignOrg.String()) {
		t.Fatalf("foreign ledger org err = %v, want a refusal naming %s", err, foreignOrg)
	}
	purgeOrgChain(t, pool, foreignOrg)

	// A nil-org row by an actor who is not a member refuses naming the
	// actor: the phantom-org case, an org deleted from org_members that
	// left only nil-stamped history behind.
	stranger := uuid.Must(uuid.NewV7())
	if err := appendWithRetry(ctx, pool, premiseEvent(t, uuid.Nil, stranger, 33)); err != nil {
		t.Fatalf("seed stranger nil row: %v", err)
	}
	t.Cleanup(func() { purgeNilOrg(t, pool) })
	if _, err := backfill.DeriveSoleOrg(ctx); err == nil || !strings.Contains(err.Error(), stranger.String()) {
		t.Fatalf("stranger actor err = %v, want a refusal naming %s", err, stranger)
	}
	seedPremiseMember(t, pool, orgID, stranger)
	if _, err := backfill.DeriveSoleOrg(ctx); err != nil {
		t.Fatalf("derive after the stranger joined: %v", err)
	}

	// A second member org is refused by count.
	seedPremiseMember(t, pool, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	if _, err := backfill.DeriveSoleOrg(ctx); err == nil || !strings.Contains(err.Error(), "holds 2 orgs") {
		t.Fatalf("two member orgs err = %v, want the 2-orgs refusal", err)
	}
}
