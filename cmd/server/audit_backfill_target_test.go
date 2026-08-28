package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
)

// seedMember creates the given user with one org membership and registers a
// cleanup that removes both.
func seedMember(t *testing.T, admin *pgxpool.Pool, orgID, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	email := "backfill-" + userID.String() + "@example.com"
	if _, err := admin.Exec(ctx,
		`INSERT INTO users (id, email) VALUES ($1, $2)`, userID, email); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO org_members (org_id, user_id) VALUES ($1, $2)`, orgID, userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DELETE FROM org_members WHERE user_id = $1`, userID)
		_, _ = admin.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
}

// TestDeriveBackfillTarget pins the sole-org guard: it accepts the one member
// org alongside the reserved system org's operator rows, and refuses a second
// member org or a foreign customer org in the ledger. The system-org
// acceptance is the TACK-461 review blocker: every deployment's ledger holds
// operator rows under that org, so refusing it refuses the exact deployment
// the command was built for.
func TestDeriveBackfillTarget(t *testing.T) {
	adminDSN := os.Getenv(auditHostTestDSNEnv)
	if adminDSN == "" {
		t.Skipf("set %s to a migrated audit DSN to run", auditHostTestDSNEnv)
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, `DELETE FROM org_members`); err != nil {
		t.Fatalf("reset org_members: %v", err)
	}
	// The actor check reads every nil-org row, so leftovers from other
	// suites on the shared throwaway would leak into this test's verdicts.
	// Run the database suites separately rather than in one parallel go
	// test invocation for the same reason.
	if _, err := admin.Exec(ctx, `DELETE FROM audit.events WHERE org_id = '00000000-0000-0000-0000-000000000000'`); err != nil {
		t.Fatalf("reset nil rows: %v", err)
	}

	// No member org: refused before any ledger reasoning.
	if _, err := deriveBackfillTarget(ctx, admin); err == nil || !strings.Contains(err.Error(), "holds 0 orgs") {
		t.Fatalf("empty org_members err = %v, want the 0-orgs refusal", err)
	}

	memberOrg := uuid.Must(uuid.NewV7())
	actor := uuid.Must(uuid.NewV7())
	seedMember(t, admin, memberOrg, uuid.Must(uuid.NewV7()))
	recordActorEvent(t, admin, adminDSN, memberOrg, actor)
	recordActorEvent(t, admin, adminDSN, audit.SystemOrgID(), actor)

	// The happy path: one member org, ledger holding that org plus the
	// system org's operator rows, derives the member org.
	target, err := deriveBackfillTarget(ctx, admin)
	if err != nil {
		t.Fatalf("derive with system-org rows present: %v", err)
	}
	if target != memberOrg {
		t.Fatalf("target = %s, want %s", target, memberOrg)
	}

	// A foreign customer org in the ledger breaks the sole-org premise.
	foreignOrg := uuid.Must(uuid.NewV7())
	recordActorEvent(t, admin, adminDSN, foreignOrg, actor)
	if _, err := deriveBackfillTarget(ctx, admin); err == nil || !strings.Contains(err.Error(), foreignOrg.String()) {
		t.Fatalf("foreign ledger org err = %v, want a refusal naming %s", err, foreignOrg)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM audit.events WHERE org_id = $1`, foreignOrg); err != nil {
		t.Fatalf("remove foreign row: %v", err)
	}

	// A nil-org row by an actor who is not a member of the target refuses
	// naming the actor: the phantom-org case, where an org deleted from
	// org_members left only nil-stamped history behind.
	stranger := uuid.Must(uuid.NewV7())
	recordActorEvent(t, admin, adminDSN, uuid.Nil, stranger)
	if _, err := deriveBackfillTarget(ctx, admin); err == nil || !strings.Contains(err.Error(), stranger.String()) {
		t.Fatalf("stranger actor err = %v, want a refusal naming %s", err, stranger)
	}
	seedMember(t, admin, memberOrg, stranger)
	if _, err := deriveBackfillTarget(ctx, admin); err != nil {
		t.Fatalf("derive after the stranger joined: %v", err)
	}

	// A second member org is refused by count.
	seedMember(t, admin, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	if _, err := deriveBackfillTarget(ctx, admin); err == nil || !strings.Contains(err.Error(), "holds 2 orgs") {
		t.Fatalf("two member orgs err = %v, want the 2-orgs refusal", err)
	}
}
