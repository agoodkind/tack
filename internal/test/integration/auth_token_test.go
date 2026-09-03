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
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/ops"
)

// refusingOutbox is a ledger that cannot be written, which is the case the
// issue must not survive: a token nobody can account for.
type refusingOutbox struct{}

func (refusingOutbox) WriteOutbox(context.Context, audit.Event) error {
	return errors.New("ledger unreachable")
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

// outboxEventFor finds the row the outbox holds for one token and verb, and
// removes it after the test so the shared outbox does not accumulate.
func outboxEventFor(t *testing.T, env *TestEnv, outbox *audit.PoolOutbox, verb audit.Verb, tokenID uuid.UUID) audit.Event {
	t.Helper()
	rows, err := outbox.ReadBatch(env.Ctx, 1000)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	for _, row := range rows {
		if row.Event.Verb == string(verb) && row.Event.Entity.ID == tokenID {
			eventID := row.EventID
			t.Cleanup(func() { _ = outbox.Delete(context.Background(), eventID) })
			return row.Event
		}
	}
	t.Fatalf("no %s row in the outbox for token %s among %d rows", verb, tokenID, len(rows))
	return audit.Event{}
}

// TestAuthTokenIssueAuthenticatesAndIsRecorded is TACK-472's contract end to
// end: the issued value authenticates as the user through the same repository
// the bearer middleware uses, the issue is in the ledger naming the operator
// and the token, and after revocation the value is refused and the revocation
// is in the ledger too.
func TestAuthTokenIssueAuthenticatesAndIsRecorded(t *testing.T) {
	env := SetupTestEnv(t)
	holder := authTokenTestUser(t, env)
	outbox := audit.NewPoolOutbox(env.Ops.Pool)
	t.Cleanup(outbox.Close)
	principal := authTokenTestPrincipal()
	tokens := postgres.NewTokenRepo(env.Ops.Pool)

	issue, err := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, outbox, principal, holder.Email, "integration", time.Now())
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
	created := outboxEventFor(t, env, outbox, audit.VerbAuthTokenCreate, issue.Token.ID)
	if created.Actor.ID != principal.ID || created.Entity.Identifier != holder.Email || created.Entity.Name != "integration" {
		t.Fatalf("create row = actor %s entity %q/%q, want operator %s for %s/integration",
			created.Actor.ID, created.Entity.Identifier, created.Entity.Name, principal.ID, holder.Email)
	}
	// A user in no org gets the system org, the same answer auth events give
	// (TACK-461), rather than a nil org that no tenant query can find.
	if created.Context.OrgID != audit.SystemOrgID() {
		t.Fatalf("create row org = %s, want the system org for a user with no membership", created.Context.OrgID)
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
	revoked := outboxEventFor(t, env, outbox, audit.VerbAuthTokenRevoke, issue.Token.ID)
	if revoked.Actor.ID != principal.ID {
		t.Fatalf("revoke row actor = %s, want operator %s", revoked.Actor.ID, principal.ID)
	}
}

// TestAuthTokenIssueIsWithdrawnWhenItCannotBeRecorded pins the rule the whole
// epic rests on: a credential with no ledger row must not exist. When the
// ledger refuses, the token row is removed again and the user holds nothing.
func TestAuthTokenIssueIsWithdrawnWhenItCannotBeRecorded(t *testing.T) {
	env := SetupTestEnv(t)
	holder := authTokenTestUser(t, env)

	_, err := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, refusingOutbox{}, authTokenTestPrincipal(), holder.Email, "integration", time.Now())

	if err == nil {
		t.Fatal("an issue the ledger refused must fail")
	}
	remaining, listErr := postgres.NewTokenRepo(env.Ops.Pool).List(env.Ctx, holder.ID)
	if listErr != nil {
		t.Fatalf("list tokens: %v", listErr)
	}
	if len(remaining) != 0 {
		t.Fatalf("user holds %d token(s) after a refused issue, want none", len(remaining))
	}
}

// TestAuthTokenIssueRefusesAnUnknownUser pins that the command issues only to
// users that exist: it never creates one on the way.
func TestAuthTokenIssueRefusesAnUnknownUser(t *testing.T) {
	env := SetupTestEnv(t)
	outbox := audit.NewPoolOutbox(env.Ops.Pool)
	t.Cleanup(outbox.Close)

	_, err := ops.IssueAuthToken(env.Ctx, env.Ops.Pool, outbox, authTokenTestPrincipal(), "nobody-"+uuid.NewString()+"@example.invalid", "integration", time.Now())

	if err == nil {
		t.Fatal("issuing to a user that does not exist must fail")
	}
}
