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

// TestRuntimeAuditSQLAtomicChange checks that ops.IssueAuthToken records one
// change event and rolls back token creation when outbox writes fail. It uses
// the real relay to compare the ledger change with the stored token and checks
// that removing a missing membership returns domain.ErrNotFound and records
// one failed result with not_found and without a delta.
func TestRuntimeAuditSQLAtomicChange(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	principal := sqlAuditPrincipal()
	ctx, _ := sqlOperatorContext(t.Context(), principal)
	holder := createSQLAuditUser(t, stack.admin, principal)

	issue, err := ops.IssueAuthToken(ctx, stack.admin, holder.Email, sqlAuditTokenLabel)
	if err != nil {
		t.Fatalf("IssueAuthToken error = %v, want nil", err)
	}
	issueEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, issue.Token.ID), audit.PhaseChange)

	restrictedPool := changeOnlyPool(t, stack.admin)
	if _, err := ops.IssueAuthToken(ctx, restrictedPool, holder.Email, sqlAuditTokenLabel); err == nil {
		t.Fatal("IssueAuthToken error = nil for a login without outbox write access, want an error")
	}
	tokenRows, err := postgres.NewTokenRepo(stack.admin).List(ctx, holder.ID)
	if err != nil || len(tokenRows) != 1 || tokenRows[0].ID != issue.Token.ID {
		t.Fatalf("stored token count = %d, error = %v; want only token %s and nil error", len(tokenRows), err, issue.Token.ID)
	}

	orgID := uuid.Must(uuid.NewV7())
	removalCtx, removal := sqlAuditContext(t, principal)
	err = postgres.NewOrgMemberRepo(stack.admin).RemoveMember(removalCtx, orgID, holder.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RemoveMember of a missing membership: err = %v, want domain.ErrNotFound", err)
	}
	failureEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, removal.Context.OperationID), audit.PhaseResult)

	startOutboxRelay(t, stack)
	issueRow := requireLedgerChange(t, stack, issueEvent)
	if issueRow.Delta == nil || len(issueRow.Delta.Before) != 0 {
		t.Fatalf("token creation Delta = %+v, want non-nil Delta with empty Before", issueRow.Delta)
	}
	requireTokenRecord(t, issueRow.Delta.After, tokenRows[0])
	failureRow := requireLedgerChange(t, stack, failureEvent)
	if failureRow.Outcome != audit.OutcomeFailed || failureRow.Error == nil ||
		failureRow.Error.Code != sqlAuditFailureCode || failureRow.Delta != nil {
		t.Fatalf("failure row Outcome = %s, Error = %+v, Delta = %+v; want audit.OutcomeFailed, Error.Code sqlAuditFailureCode, and nil Delta", failureRow.Outcome, failureRow.Error, failureRow.Delta)
	}
}

// TestRuntimeAuditMembershipRoleReplacement adds a membership with role 15,
// replaces it with role 20, repeats role 20, then removes the membership.
// It requires one change event for each addition, replacement, and removal,
// and requires the repeated role to produce zero events. It compares ledger
// values and the relationship target with the stored membership. It requires
// the replacement event to use org.member_role_set and list /role in Changed.
// It also checks the user creation event's Changed fields, protected
// references, and omission of the raw email and display name.
func TestRuntimeAuditMembershipRoleReplacement(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	ctx := t.Context()
	principal := sqlAuditPrincipal()
	holder := createSQLAuditUser(t, stack.admin, principal)
	members := postgres.NewOrgMemberRepo(stack.admin)
	orgID := uuid.Must(uuid.NewV7())

	add := newMemberStep(t, principal, sqlAuditMemberRole)
	replace := newMemberStep(t, principal, sqlAuditAdminRole)
	repeat := newMemberStep(t, principal, sqlAuditAdminRole)
	removeCtx, remove := sqlAuditContext(t, principal)
	for _, step := range []memberStep{add, replace, repeat} {
		member := &org.Member{ID: uuid.Nil, OrgID: orgID, UserID: holder.ID, Role: step.role, CreatedAt: time.Time{}}
		if err := members.AddMember(step.ctx, member); err != nil {
			t.Fatalf("AddMember with role %d: got error %v, want nil", step.role, err)
		}
	}
	membership := requireMembership(t, members, orgID, holder.ID)
	if err := members.RemoveMember(removeCtx, orgID, holder.ID); err != nil {
		t.Fatalf("RemoveMember error = %v, want nil", err)
	}
	memberRows, err := members.ListMembers(ctx, orgID)
	if err != nil || len(memberRows) != 0 {
		t.Fatalf("member count after RemoveMember = %d, error = %v; want 0 and nil error", len(memberRows), err)
	}

	addEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, add.operation.Context.OperationID), audit.PhaseChange)
	replaceEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, replace.operation.Context.OperationID), audit.PhaseChange)
	removeEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, remove.Context.OperationID), audit.PhaseChange)
	userEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, holder.ID), audit.PhaseChange)
	repeatEvents := sqlOutboxEvents(t, stack.admin, repeat.operation.Context.OperationID)
	if len(repeatEvents) != 0 {
		t.Fatalf("events for an unchanged role = %d, want 0", len(repeatEvents))
	}
	startOutboxRelay(t, stack)

	requireUserChange(t, stack, userEvent, holder)
	want := memberRecord{ID: membership.ID, OrgID: orgID, UserID: holder.ID, Role: sqlAuditMemberRole, CreatedAt: membership.CreatedAt}
	addRow := requireLedgerChange(t, stack, addEvent)
	requireMemberTarget(t, addRow, want)
	requireMemberRecord(t, addRow.Delta.After, want)
	replaceRow := requireLedgerChange(t, stack, replaceEvent)
	requireMemberRecord(t, replaceRow.Delta.Before, want)
	want.Role = sqlAuditAdminRole
	requireMemberRecord(t, replaceRow.Delta.After, want)
	if !slices.Equal(replaceRow.Delta.Changed, []string{sqlAuditMemberChanged}) || replaceRow.Action != string(audit.VerbOrgMemberRoleSet) {
		t.Fatalf("replacement Delta.Changed = %v, Action = %s; want Delta.Changed = [/role] and Action = string(audit.VerbOrgMemberRoleSet)", replaceRow.Delta.Changed, replaceRow.Action)
	}
	removeRow := requireLedgerChange(t, stack, removeEvent)
	requireMemberRecord(t, removeRow.Delta.Before, want)
	if len(removeRow.Delta.After) != 0 {
		t.Fatalf("removal Delta.After = %s, want an empty value", removeRow.Delta.After)
	}
}

// TestRuntimeAuditTokenMetadata issues, validates, and revokes a token using
// ctx with an operator principal and command operation ID. It requires
// validation to leave last_used nil without adding an outbox event. It
// compares ledger token metadata with the stored row and requires both change
// events to omit the raw token and its SHA-256 hash, use the command ID as
// OperationID, and identify the principal as the actor.
func TestRuntimeAuditTokenMetadata(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	principal := sqlAuditPrincipal()
	ctx, commandID := sqlOperatorContext(t.Context(), principal)
	holder := createSQLAuditUser(t, stack.admin, principal)
	tokens := postgres.NewTokenRepo(stack.admin)

	issue, err := ops.IssueAuthToken(ctx, stack.admin, holder.Email, sqlAuditTokenLabel)
	if err != nil {
		t.Fatalf("IssueAuthToken error = %v, want nil", err)
	}
	issueEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, issue.Token.ID), audit.PhaseChange)
	if _, err := tokens.Validate(ctx, issue.Raw); err != nil {
		t.Fatalf("Validate error = %v, want nil", err)
	}
	tokenRow, err := tokens.GetByID(ctx, issue.Token.ID)
	if err != nil || tokenRow.LastUsed != nil {
		t.Fatalf("token after Validate = %+v, %v; want nil LastUsed and nil error", tokenRow, err)
	}
	onlyEvent(t, sqlOutboxEvents(t, stack.admin, issue.Token.ID), audit.PhaseChange)
	if _, err := ops.RevokeAuthToken(ctx, stack.admin, issue.Token.ID); err != nil {
		t.Fatalf("RevokeAuthToken error = %v, want nil", err)
	}
	tokenEvents := sqlOutboxEvents(t, stack.admin, issue.Token.ID)
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
		if row.Context.OperationID != commandID || row.ActorID != principal.ID {
			t.Fatalf("event %s OperationID = %s, ActorID = %s; want commandID = %s and principal.ID = %s", row.EventID, row.Context.OperationID, row.ActorID, commandID, principal.ID)
		}
	}
	requireTokenRecord(t, issueRow.Delta.After, tokenRow)
	requireTokenRecord(t, revokeRow.Delta.Before, tokenRow)
	if len(revokeRow.Delta.After) != 0 || revokeRow.Action != string(audit.VerbAuthTokenRevoke) {
		t.Fatalf("revocation row Action = %s, Delta.After = %s; want string(audit.VerbAuthTokenRevoke) and empty Delta.After", revokeRow.Action, revokeRow.Delta.After)
	}
}
