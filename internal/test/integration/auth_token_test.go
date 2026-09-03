package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/ops"
)

// TestAuthTokenIssueAuthenticatesAndIsRecorded is TACK-472's contract end to
// end: the issued value authenticates as the user through the same repository
// the bearer middleware uses, the issue is in the ledger as an intent and an
// outcome naming the operator and the token, and after revocation the value
// is refused and the revocation is in the ledger the same way.
func TestAuthTokenIssueAuthenticatesAndIsRecorded(t *testing.T) {
	env := SetupTestEnv(t)
	holder := authTokenTestUser(t, env)
	outbox := audit.NewPoolOutbox(env.Ops.Pool)
	principal := authTokenTestPrincipal()
	tokens := postgres.NewTokenRepo(env.Ops.Pool)

	issue, err := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, outbox, principal, holder.Email, "  integration ", time.Now())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	t.Cleanup(func() { _ = tokens.Delete(context.Background(), issue.Token.ID) })

	validated, err := tokens.Validate(env.Ctx, issue.Raw)
	if err != nil {
		t.Fatalf("the issued value must authenticate: %v", err)
	}
	if validated.UserID != holder.ID {
		t.Fatalf("issued value authenticates as %s, want %s", validated.UserID, holder.ID)
	}
	confirmed := requireIntentAndOutcome(t, outboxRowsFor(t, env, audit.VerbAuthTokenCreate, issue.Token.ID), principal, "create")
	if confirmed.Entity.Identifier != holder.Email || confirmed.Entity.Name != "integration" {
		t.Fatalf("create row entity = %q/%q, want %s/integration", confirmed.Entity.Identifier, confirmed.Entity.Name, holder.Email)
	}
	// A user in no org is stamped with the system org, which the ledger
	// reader can be asked for, rather than the nil org it refuses.
	if confirmed.Context.OrgID != audit.SystemOrgID() {
		t.Fatalf("create row org = %s, want the system org for a user with no membership", confirmed.Context.OrgID)
	}

	revocation, err := ops.RevokeAuthToken(env.Ctx, env.Ops.Pool, outbox, principal, issue.Token.ID, time.Now())
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revocation.Holder.ID != holder.ID {
		t.Fatalf("revocation names holder %s, want %s", revocation.Holder.ID, holder.ID)
	}
	if _, err := tokens.Validate(env.Ctx, issue.Raw); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("after revocation Validate err = %v, want ErrUnauthenticated", err)
	}
	requireIntentAndOutcome(t, outboxRowsFor(t, env, audit.VerbAuthTokenRevoke, issue.Token.ID), principal, "revoke")
}

// TestAuthTokenIssueRefusedByTheLedgerIssuesNothing pins the rule the whole
// epic rests on: a credential with no ledger row must not exist. A ledger that
// refuses the intent issues nothing; a ledger that refuses the outcome leaves
// the pending row and withdraws the token. Either way the user holds nothing.
func TestAuthTokenIssueRefusedByTheLedgerIssuesNothing(t *testing.T) {
	env := SetupTestEnv(t)
	holder := authTokenTestUser(t, env)
	principal := authTokenTestPrincipal()
	realOutbox := audit.NewPoolOutbox(env.Ops.Pool)

	_, intentErr := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, refusingOutbox{}, principal, holder.Email, "integration", time.Now())
	_, outcomeErr := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, outcomeRefusingOutbox{inner: realOutbox}, principal, holder.Email, "integration", time.Now())

	if intentErr == nil || outcomeErr == nil {
		t.Fatalf("errs = %v / %v, want both issues to fail", intentErr, outcomeErr)
	}
	remaining, listErr := postgres.NewTokenRepo(env.Ops.Pool).List(env.Ctx, holder.ID)
	if listErr != nil {
		t.Fatalf("list tokens: %v", listErr)
	}
	if len(remaining) != 0 {
		t.Fatalf("user holds %d token(s) after refused issues, want none", len(remaining))
	}
	if pending := pendingRowsForHolder(t, env, holder.Email); pending != 1 {
		t.Fatalf("pending rows for %s = %d, want exactly the one the withdrawn issue left", holder.Email, pending)
	}
}

// TestAuthTokenRevokeRetriesAfterAFailedAttempt pins that a revocation whose
// first attempt died between its intent row and its delete can be run again.
// The retry records its own intent and outcome instead of colliding with the
// first attempt's rows in the outbox, which under one key per token would
// refuse it until the relay drained; the ledger then holds both attempts,
// the first answered by an error row and the second by an ok row.
func TestAuthTokenRevokeRetriesAfterAFailedAttempt(t *testing.T) {
	env := SetupTestEnv(t)
	holder := authTokenTestUser(t, env)
	principal := authTokenTestPrincipal()
	realOutbox := audit.NewPoolOutbox(env.Ops.Pool)
	tokens := postgres.NewTokenRepo(env.Ops.Pool)
	issue, err := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, realOutbox, principal, holder.Email, "integration", time.Now())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	t.Cleanup(func() { _ = tokens.Delete(context.Background(), issue.Token.ID) })
	outboxRowsFor(t, env, audit.VerbAuthTokenCreate, issue.Token.ID)

	dyingCtx, cancel := context.WithCancel(env.Ctx)
	defer cancel()
	_, firstErr := ops.RevokeAuthToken(dyingCtx, env.Ops.Pool, cancelAfterIntentOutbox{inner: realOutbox, cancel: cancel}, principal, issue.Token.ID, time.Now())
	stillThere, lookupErr := tokens.GetByID(env.Ctx, issue.Token.ID)
	_, secondErr := ops.RevokeAuthToken(env.Ctx, env.Ops.Pool, realOutbox, principal, issue.Token.ID, time.Now())

	if firstErr == nil {
		t.Fatal("a revocation whose delete was cut off must report it")
	}
	if lookupErr != nil || stillThere == nil {
		t.Fatalf("the cut-off attempt must leave the token in place: %v", lookupErr)
	}
	if secondErr != nil {
		t.Fatalf("the retry must revoke, not collide with the first attempt's rows: %v", secondErr)
	}
	if _, err := tokens.Validate(env.Ctx, issue.Raw); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("after the retry Validate err = %v, want ErrUnauthenticated", err)
	}
	counts := countOutcomes(outboxRowsFor(t, env, audit.VerbAuthTokenRevoke, issue.Token.ID))
	if counts[audit.OutcomePending] != 2 || counts[audit.OutcomeError] != 1 || counts[audit.OutcomeOK] != 1 {
		t.Fatalf("revoke rows by outcome = %v, want two pending, one error, one ok", counts)
	}
}

// TestAuthTokenIssueRefusesAnUnknownUser pins that the command issues only to
// users that exist: it never creates one on the way.
func TestAuthTokenIssueRefusesAnUnknownUser(t *testing.T) {
	env := SetupTestEnv(t)
	migrateLedgerSchema(t, env)
	outbox := audit.NewPoolOutbox(env.Ops.Pool)

	_, err := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, outbox, authTokenTestPrincipal(), "nobody-"+uuid.NewString()+"@example.invalid", "integration", time.Now())

	if err == nil {
		t.Fatal("issuing to a user that does not exist must fail")
	}
}
