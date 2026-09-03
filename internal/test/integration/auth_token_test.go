package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/ops"
)

// refusingOutbox is a ledger that cannot be written, which is the case an
// issue must not survive: a token nobody can account for.
type refusingOutbox struct{}

func (refusingOutbox) WriteOutbox(context.Context, audit.Event) error {
	return errors.New("ledger unreachable")
}

// outcomeRefusingOutbox accepts the intent row and refuses the outcome row,
// which is the window where a token exists that the ledger has not confirmed.
type outcomeRefusingOutbox struct {
	inner audit.OutboxWriter
}

func (o outcomeRefusingOutbox) WriteOutbox(ctx context.Context, event audit.Event) error {
	if event.Outcome == audit.OutcomeOK {
		return errors.New("ledger unreachable after the write")
	}
	return o.inner.WriteOutbox(ctx, event)
}

func authTokenTestUser(t *testing.T, env *TestEnv) *user.User {
	t.Helper()
	now := time.Now().UTC()
	created, err := postgres.NewUserRepo(env.Ops.Pool).Create(env.Ctx, &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: "token-" + uuid.NewString() + "@example.invalid",
		DisplayName: "Token Holder", AvatarURL: nil, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create the token holder: %v", err)
	}
	return created
}

func authTokenTestPrincipal() audit.OperatorPrincipal {
	return audit.OperatorPrincipal{
		ID: uuid.Must(uuid.NewV7()), Email: "operator@example.invalid", Name: "Operator", Source: "test",
	}
}

// outboxRowsFor reads every outbox row for one token and verb straight from
// the table by the event's own fields, so rows other tests left behind cannot
// hide the ones this test wrote. It removes them after the test.
func outboxRowsFor(t *testing.T, env *TestEnv, verb audit.Verb, tokenID uuid.UUID) map[audit.Outcome]audit.Event {
	t.Helper()
	rows, err := env.Ops.Pool.Query(env.Ctx, `
		SELECT event_id, event FROM public.ops_outbox
		 WHERE event->>'verb' = $1 AND event->'entity'->>'id' = $2`,
		string(verb), tokenID.String())
	if err != nil {
		t.Fatalf("read the outbox for token %s: %v", tokenID, err)
	}
	defer rows.Close()
	found := make(map[audit.Outcome]audit.Event)
	for rows.Next() {
		var eventID uuid.UUID
		var raw []byte
		if err := rows.Scan(&eventID, &raw); err != nil {
			t.Fatalf("scan an outbox row: %v", err)
		}
		var event audit.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode an outbox row: %v", err)
		}
		found[event.Outcome] = event
		t.Cleanup(func() {
			_, _ = env.Ops.Pool.Exec(context.Background(), `DELETE FROM public.ops_outbox WHERE event_id = $1`, eventID)
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox rows: %v", err)
	}
	return found
}

func requireIntentAndOutcome(t *testing.T, rows map[audit.Outcome]audit.Event, principal audit.OperatorPrincipal, what string) {
	t.Helper()
	pending, hasPending := rows[audit.OutcomePending]
	confirmed, hasOK := rows[audit.OutcomeOK]
	if !hasPending || !hasOK {
		t.Fatalf("%s rows = %d, want a pending and an ok row", what, len(rows))
	}
	if pending.Actor.ID != principal.ID || confirmed.Actor.ID != principal.ID {
		t.Fatalf("%s rows name actors %s and %s, want operator %s", what, pending.Actor.ID, confirmed.Actor.ID, principal.ID)
	}
}

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
	created := outboxRowsFor(t, env, audit.VerbAuthTokenCreate, issue.Token.ID)
	requireIntentAndOutcome(t, created, principal, "create")
	confirmed := created[audit.OutcomeOK]
	if confirmed.Entity.Identifier != holder.Email || confirmed.Entity.Name != "integration" {
		t.Fatalf("create row entity = %q/%q, want %s/integration", confirmed.Entity.Identifier, confirmed.Entity.Name, holder.Email)
	}
	// A user in no org gets the nil org, the same answer the bearer middleware
	// stamps on that user's auth.token_used rows (TACK-461), so the issue and
	// the uses of one token are found by the same query.
	if confirmed.Context.OrgID != uuid.Nil {
		t.Fatalf("create row org = %s, want the nil org for a user with no membership", confirmed.Context.OrgID)
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

// TestAuthTokenRevokeRetriesAfterAFailedAttempt pins that a revocation whose
// first attempt died after its intent row can be run again: the retry records
// its own rows instead of colliding with the first attempt's pending row in
// the outbox, which would refuse it until the relay drained.
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

	// First attempt: the intent lands and the outcome is refused, so the
	// token is gone but unconfirmed. Second attempt against a gone token must
	// fail on the lookup, not on a duplicate outbox row.
	_, firstErr := ops.RevokeAuthToken(env.Ctx, env.Ops.Pool, outcomeRefusingOutbox{inner: realOutbox}, principal, issue.Token.ID, time.Now())
	_, secondErr := ops.RevokeAuthToken(env.Ctx, env.Ops.Pool, realOutbox, principal, issue.Token.ID, time.Now())

	if firstErr == nil {
		t.Fatal("a revocation whose confirming row was refused must report it")
	}
	if secondErr == nil || !strings.Contains(secondErr.Error(), "no token") {
		t.Fatalf("second attempt err = %v, want the token reported as gone, not an outbox conflict", secondErr)
	}
	rows := outboxRowsFor(t, env, audit.VerbAuthTokenRevoke, issue.Token.ID)
	if _, hasPending := rows[audit.OutcomePending]; !hasPending {
		t.Fatal("the first attempt's pending row must stand as the record of the revocation")
	}
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
	pendingRows, err := env.Ops.Pool.Query(env.Ctx, `
		SELECT event_id FROM public.ops_outbox
		 WHERE event->>'verb' = $1 AND event->'entity'->>'identifier' = $2 AND event->>'outcome' = $3`,
		string(audit.VerbAuthTokenCreate), holder.Email, string(audit.OutcomePending))
	if err != nil {
		t.Fatalf("read pending rows: %v", err)
	}
	defer pendingRows.Close()
	pendingCount := 0
	for pendingRows.Next() {
		var eventID uuid.UUID
		if err := pendingRows.Scan(&eventID); err != nil {
			t.Fatalf("scan pending row: %v", err)
		}
		pendingCount++
		t.Cleanup(func() {
			_, _ = env.Ops.Pool.Exec(context.Background(), `DELETE FROM public.ops_outbox WHERE event_id = $1`, eventID)
		})
	}
	if pendingCount != 1 {
		t.Fatalf("pending rows for %s = %d, want exactly the one the withdrawn issue left", holder.Email, pendingCount)
	}
}

// TestAuthTokenIssueRefusesAnUnknownUser pins that the command issues only to
// users that exist: it never creates one on the way.
func TestAuthTokenIssueRefusesAnUnknownUser(t *testing.T) {
	env := SetupTestEnv(t)
	outbox := audit.NewPoolOutbox(env.Ops.Pool)

	_, err := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, outbox, authTokenTestPrincipal(), "nobody-"+uuid.NewString()+"@example.invalid", "integration", time.Now())

	if err == nil {
		t.Fatal("issuing to a user that does not exist must fail")
	}
}
