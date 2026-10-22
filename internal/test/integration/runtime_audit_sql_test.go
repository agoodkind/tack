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
	ctx := t.Context()
	principal := sqlAuditPrincipal()
	holder := createSQLAuditUser(t, stack.admin, principal)

	issue, err := ops.IssueAuthToken(ctx, stack.admin, principal, holder.Email, sqlAuditTokenLabel)
	if err != nil {
		t.Fatalf("IssueAuthToken error = %v, want nil", err)
	}
	issueEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, issue.Token.ID), audit.PhaseChange)

	restrictedPool := changeOnlyPool(t, stack.admin)
	if _, err := ops.IssueAuthToken(ctx, restrictedPool, principal, holder.Email, sqlAuditTokenLabel); err == nil {
		t.Fatal("IssueAuthToken error = nil for a login without outbox write access, want an error")
	}
	tokenRows, err := postgres.NewTokenRepo(stack.admin).List(ctx, holder.ID)
	if err != nil || len(tokenRows) != 1 || tokenRows[0].ID != issue.Token.ID {
		t.Fatalf("stored token count = %d, error = %v; want only token %s and nil error", len(tokenRows), err, issue.Token.ID)
	}

	orgID := uuid.Must(uuid.NewV7())
	removal := sqlAuditOperation(audit.VerbOrgMemberRemove, principal, orgID)
	err = postgres.NewOrgMemberRepo(stack.admin).RemoveMember(ctx, removal, orgID, holder.ID)
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
// replaces it with role 20, sets role 20 again, then removes the membership.
// It requires one change event each for the addition, replacement, and removal.
// It requires the repeated role to produce zero events. It compares the ledger
// old and new values and the relationship target with the stored membership
// row. It also requires the user creation event to list /email and
// /display_name in Changed and the ledger row to omit the email and display
// name.
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
			t.Fatalf("AddMember with role %d: got error %v, want nil", step.role, err)
		}
	}
	membership := requireMembership(t, members, orgID, holder.ID)
	if err := members.RemoveMember(ctx, remove, orgID, holder.ID); err != nil {
		t.Fatalf("RemoveMember error = %v, want nil", err)
	}
	memberRows, err := members.ListMembers(ctx, orgID)
	if err != nil || len(memberRows) != 0 {
		t.Fatalf("member count after RemoveMember = %d, error = %v; want 0 and nil error", len(memberRows), err)
	}

	addEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, add.Context.OperationID), audit.PhaseChange)
	replaceEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, replace.Context.OperationID), audit.PhaseChange)
	removeEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, remove.Context.OperationID), audit.PhaseChange)
	userEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, holder.ID), audit.PhaseChange)
	repeatEvents := sqlOutboxEvents(t, stack.admin, repeat.Context.OperationID)
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
	if !slices.Equal(replaceRow.Delta.Changed, []string{sqlAuditMemberChanged}) {
		t.Fatalf("replacement changed fields = %v, want [role]", replaceRow.Delta.Changed)
	}
	removeRow := requireLedgerChange(t, stack, removeEvent)
	requireMemberRecord(t, removeRow.Delta.Before, want)
	if len(removeRow.Delta.After) != 0 {
		t.Fatalf("removal Delta.After = %s, want an empty value", removeRow.Delta.After)
	}
}

// TestRuntimeAuditTokenMetadata attaches a command operation ID to ctx with
// [audit.WithOpID], then issues a token with [ops.IssueAuthToken], validates it
// with [postgres.TokenRepo.Validate], and revokes it with [ops.RevokeAuthToken].
// It requires validation to leave last_used nil without adding an outbox event.
// It checks ledger token metadata against the stored row. It requires both
// ledger change events to omit the raw token and its SHA-256 hash and to use
// the command operation ID as ParentOperationID.
func TestRuntimeAuditTokenMetadata(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	commandID := uuid.Must(uuid.NewV7())
	ctx := audit.WithOpID(t.Context(), commandID)
	principal := sqlAuditPrincipal()
	holder := createSQLAuditUser(t, stack.admin, principal)
	tokens := postgres.NewTokenRepo(stack.admin)

	issue, err := ops.IssueAuthToken(ctx, stack.admin, principal, holder.Email, sqlAuditTokenLabel)
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
	if _, err := ops.RevokeAuthToken(ctx, stack.admin, principal, issue.Token.ID); err != nil {
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
		if row.Context.ParentOperationID != commandID {
			t.Fatalf("event %s has ParentOperationID %s; the test requires commandID %s", row.EventID, row.Context.ParentOperationID, commandID)
		}
	}
	requireTokenRecord(t, issueRow.Delta.After, tokenRow)
	requireTokenRecord(t, revokeRow.Delta.Before, tokenRow)
	if len(revokeRow.Delta.After) != 0 || revokeRow.Action != string(audit.VerbAuthTokenRevoke) {
		t.Fatalf("revocation row Action = %s, Delta.After = %s; want string(audit.VerbAuthTokenRevoke) and empty Delta.After", revokeRow.Action, revokeRow.Delta.After)
	}
}
