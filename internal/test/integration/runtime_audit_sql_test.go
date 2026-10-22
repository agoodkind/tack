package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/ops"
)

const (
	sqlAuditMemberRole    = 15
	sqlAuditAdminRole     = 20
	sqlAuditTokenLabel    = "runtime audit"
	sqlAuditTokenURI      = "tack:api_token/"
	sqlAuditFailureCode   = "not_found"
	sqlAuditMemberChanged = "role"
)

type memberStep struct {
	operation audit.Event
	role      int
}

// TestRuntimeAuditSQLAtomicChange checks that ops.IssueAuthToken records one
// change event and rolls back token creation when outbox writes fail. It uses
// the real relay to compare the ledger change with the stored token and checks
// that removing a missing membership returns domain.ErrNotFound and records
// one failed result with not_found and without a delta.
func TestRuntimeAuditSQLAtomicChange(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	ctx := t.Context()
	principal := sqlAuditPrincipal()
	holder := createSQLAuditUser(t, stack.admin, principal)

	issue, err := ops.IssueAuthToken(ctx, stack.admin, principal, holder.Email, sqlAuditTokenLabel)
	if err != nil {
		t.Fatalf("IssueAuthToken: %v", err)
	}
	issueEvent := onlyEvent(t, outboxEvents(t, stack.admin, issue.Token.ID), audit.PhaseChange)

	restrictedPool := changeOnlyPool(t, stack.admin)
	if _, err := ops.IssueAuthToken(ctx, restrictedPool, principal, holder.Email, sqlAuditTokenLabel); err == nil {
		t.Fatal("IssueAuthToken returned a nil error for a login without outbox write access")
	}
	tokenRows, err := postgres.NewTokenRepo(stack.admin).List(ctx, holder.ID)
	if err != nil || len(tokenRows) != 1 || tokenRows[0].ID != issue.Token.ID {
		t.Fatalf("stored tokens = %d, %v; want only token %s", len(tokenRows), err, issue.Token.ID)
	}

	orgID := uuid.Must(uuid.NewV7())
	removal := sqlAuditOperation(audit.VerbOrgMemberRemove, principal, orgID)
	err = postgres.NewOrgMemberRepo(stack.admin).RemoveMember(ctx, removal, orgID, holder.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RemoveMember of a missing membership: err = %v, want domain.ErrNotFound", err)
	}
	failureEvent := onlyEvent(t, outboxEvents(t, stack.admin, removal.Context.OperationID), audit.PhaseResult)

	startOutboxRelay(t, stack)
	issueRow := requireLedgerChange(t, stack, issueEvent)
	if issueRow.Delta == nil || len(issueRow.Delta.Before) != 0 {
		t.Fatalf("token creation delta = %+v, want an empty old value", issueRow.Delta)
	}
	requireTokenRecord(t, issueRow.Delta.After, tokenRows[0])
	failureRow := requireLedgerChange(t, stack, failureEvent)
	if failureRow.Outcome != audit.OutcomeFailed || failureRow.Error == nil ||
		failureRow.Error.Code != sqlAuditFailureCode || failureRow.Delta != nil {
		t.Fatalf("failure row = outcome %s, error %+v, delta %+v", failureRow.Outcome, failureRow.Error, failureRow.Delta)
	}
}

// TestRuntimeAuditMembershipRoleReplacement checks one change event for each
// membership addition, role replacement, and removal, and does not allow an
// event for an unchanged role. It compares ledger values and the relationship
// target with the stored membership.
func TestRuntimeAuditMembershipRoleReplacement(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	ctx := t.Context()
	principal := sqlAuditPrincipal()
	holder := createSQLAuditUser(t, stack.admin, principal)
	members := postgres.NewOrgMemberRepo(stack.admin)
	orgID := uuid.Must(uuid.NewV7())

	add := sqlAuditOperation(audit.VerbOrgMemberAdd, principal, orgID)
	replace := sqlAuditOperation(audit.VerbOrgMemberRoleSet, principal, orgID)
	repeat := sqlAuditOperation(audit.VerbOrgMemberRoleSet, principal, orgID)
	remove := sqlAuditOperation(audit.VerbOrgMemberRemove, principal, orgID)
	steps := []memberStep{{add, sqlAuditMemberRole}, {replace, sqlAuditAdminRole}, {repeat, sqlAuditAdminRole}}
	for _, step := range steps {
		member := &org.Member{ID: uuid.Nil, OrgID: orgID, UserID: holder.ID, Role: step.role, CreatedAt: time.Time{}}
		if err := members.AddMember(ctx, step.operation, member); err != nil {
			t.Fatalf("AddMember with role %d: %v", step.role, err)
		}
	}
	membership := requireMembership(t, members, orgID, holder.ID)
	if err := members.RemoveMember(ctx, remove, orgID, holder.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	memberRows, err := members.ListMembers(ctx, orgID)
	if err != nil || len(memberRows) != 0 {
		t.Fatalf("members after RemoveMember = %d, %v; want 0", len(memberRows), err)
	}

	addEvent := onlyEvent(t, outboxEvents(t, stack.admin, add.Context.OperationID), audit.PhaseChange)
	replaceEvent := onlyEvent(t, outboxEvents(t, stack.admin, replace.Context.OperationID), audit.PhaseChange)
	removeEvent := onlyEvent(t, outboxEvents(t, stack.admin, remove.Context.OperationID), audit.PhaseChange)
	repeatEvents := outboxEvents(t, stack.admin, repeat.Context.OperationID)
	if len(repeatEvents) != 0 {
		t.Fatalf("events for an unchanged role = %d, want 0", len(repeatEvents))
	}
	startOutboxRelay(t, stack)

	want := memberRecord{ID: membership.ID, OrgID: orgID, UserID: holder.ID, Role: sqlAuditMemberRole, CreatedAt: membership.CreatedAt}
	addRow := requireLedgerChange(t, stack, addEvent)
	requireMemberTarget(t, addRow, want)
	requireMemberRecord(t, addRow.Delta.After, want)
	replaceRow := requireLedgerChange(t, stack, replaceEvent)
	requireMemberRecord(t, replaceRow.Delta.Before, want)
	want.Role = sqlAuditAdminRole
	requireMemberRecord(t, replaceRow.Delta.After, want)
	if !slices.Equal(replaceRow.Delta.Changed, []string{sqlAuditMemberChanged}) {
		t.Fatalf("replacement changed fields = %v, want [role]", replaceRow.Delta.Changed)
	}
	removeRow := requireLedgerChange(t, stack, removeEvent)
	requireMemberRecord(t, removeRow.Delta.Before, want)
	if len(removeRow.Delta.After) != 0 {
		t.Fatalf("removal new value = %s, want an empty value", removeRow.Delta.After)
	}
}

// TestRuntimeAuditTokenMetadata checks token issuance and revocation through
// operator commands. It requires validation to leave last_used nil without
// adding an outbox event. It checks ledger token metadata against the stored
// row and requires both ledger change events to omit the raw token and its
// SHA-256 hash.
func TestRuntimeAuditTokenMetadata(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	ctx := t.Context()
	principal := sqlAuditPrincipal()
	holder := createSQLAuditUser(t, stack.admin, principal)
	tokens := postgres.NewTokenRepo(stack.admin)

	issue, err := ops.IssueAuthToken(ctx, stack.admin, principal, holder.Email, sqlAuditTokenLabel)
	if err != nil {
		t.Fatalf("IssueAuthToken: %v", err)
	}
	issueEvent := onlyEvent(t, outboxEvents(t, stack.admin, issue.Token.ID), audit.PhaseChange)
	if _, err := tokens.Validate(ctx, issue.Raw); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tokenRow, err := tokens.GetByID(ctx, issue.Token.ID)
	if err != nil || tokenRow.LastUsed != nil {
		t.Fatalf("token after Validate = %+v, %v; want a nil last use", tokenRow, err)
	}
	onlyEvent(t, outboxEvents(t, stack.admin, issue.Token.ID), audit.PhaseChange)
	if _, err := ops.RevokeAuthToken(ctx, stack.admin, principal, issue.Token.ID); err != nil {
		t.Fatalf("RevokeAuthToken: %v", err)
	}
	tokenEvents := outboxEvents(t, stack.admin, issue.Token.ID)
	if len(tokenEvents) != 2 {
		t.Fatalf("token events = %d, want 2", len(tokenEvents))
	}
	startOutboxRelay(t, stack)

	digest := sha256.Sum256([]byte(issue.Raw))
	secrets := []string{issue.Raw, hex.EncodeToString(digest[:])}
	issueRow := requireLedgerChange(t, stack, issueEvent)
	revokeRow := requireLedgerChange(t, stack, tokenEvents[1])
	for _, row := range []audit.Row{issueRow, revokeRow} {
		requireTokenTarget(t, row, issue.Token.ID)
		requireNoSecrets(t, row, secrets)
	}
	requireTokenRecord(t, issueRow.Delta.After, tokenRow)
	requireTokenRecord(t, revokeRow.Delta.Before, tokenRow)
	if len(revokeRow.Delta.After) != 0 || revokeRow.Action != string(audit.VerbAuthTokenRevoke) {
		t.Fatalf("revocation row = action %s, new value %s", revokeRow.Action, revokeRow.Delta.After)
	}
}
