package integration

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/ops"
)

// TestAuthTokenIssueAuthenticatesAndIsRecorded checks that an issued bearer
// authenticates as its holder and that revocation makes validation reject it.
// It requires one completed change event per command with the operator as
// actor, and checks that issuance uses the system org.
func TestAuthTokenIssueAuthenticatesAndIsRecorded(t *testing.T) {
	env := SetupTestEnv(t)
	holder := authTokenTestUser(t, env)
	principal := authTokenTestPrincipal()
	tokens := postgres.NewTokenRepo(env.Ops.Pool)

	issue, err := ops.IssueAuthToken(operatorContext(t.Context(), principal), env.Ops.Pool, holder.Email, "  integration ")
	if err != nil {
		t.Fatalf("IssueAuthToken error = %v, want nil", err)
	}
	validated, err := tokens.Validate(env.Ctx, issue.Raw)
	if err != nil || validated.UserID != holder.ID {
		t.Fatalf("Validate(issued) = %+v, %v; want UserID %s and nil error", validated, err, holder.ID)
	}
	created := requireOneChange(t, outboxRowsFor(t, env, audit.VerbAuthTokenCreate, issue.Token.ID), principal)
	if created.Entity.Name != "integration" || created.Context.OrgID != audit.SystemOrgID() {
		t.Fatalf("create event Entity.Name = %q, Context.OrgID = %s; want \"integration\" and audit.SystemOrgID()", created.Entity.Name, created.Context.OrgID)
	}

	revocation, err := ops.RevokeAuthToken(operatorContext(t.Context(), principal), env.Ops.Pool, issue.Token.ID)
	if err != nil || revocation.Holder.ID != holder.ID {
		t.Fatalf("RevokeAuthToken Holder = %+v, error = %v; want ID %s and nil error", revocation.Holder, err, holder.ID)
	}
	if _, err := tokens.Validate(env.Ctx, issue.Raw); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("Validate(revoked) err = %v, want domain.ErrUnauthenticated", err)
	}
	requireOneChange(t, outboxRowsFor(t, env, audit.VerbAuthTokenRevoke, issue.Token.ID), principal)
}

// TestAuthTokenIssueRefusesAnUnknownUser checks that IssueAuthToken refuses to
// issue a token for an email without an existing user.
func TestAuthTokenIssueRefusesAnUnknownUser(t *testing.T) {
	env := SetupTestEnv(t)

	_, err := ops.IssueAuthToken(operatorContext(t.Context(), authTokenTestPrincipal()), env.Ops.Pool, "nobody-"+uuid.NewString()+"@example.invalid", "integration")

	if err == nil {
		t.Fatal("IssueAuthToken error = nil for an email without a user, want an error")
	}
}
