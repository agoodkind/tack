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
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/ops"
)

// TestRuntimeAuditSQLAtomicChange checks that ops.IssueAuthToken records one
// change event. A second ops.IssueAuthToken through a login that cannot write
// the operator outbox must fail and leave the holder's users, api_tokens, and
// org_members rows and the outbox unchanged. The test uses the real relay to
// compare the ledger change with the stored token. It checks that removing a
// missing membership returns domain.ErrNotFound and records one failed result
// with not_found and without a delta.
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
	issued, err := postgres.NewTokenRepo(stack.admin).GetByID(ctx, issue.Token.ID)
	if err != nil {
		t.Fatalf("TokenRepo.GetByID(%s) error = %v, want nil", issue.Token.ID, err)
	}

	holderIDs := []uuid.UUID{holder.ID}
	before := sqlRows(t, stack.admin, holderIDs)
	failedCtx, failedID := sqlOperatorContext(t.Context(), principal)
	_, err = ops.IssueAuthToken(failedCtx, changeOnlyPool(t, stack.admin), holder.Email, sqlAuditTokenLabel)
	requireAuditWriteFailure(t, "IssueAuthToken", err)
	requireSQLUnchanged(t, stack.admin, holderIDs, before, failedID)

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
	requireTokenRecord(t, issueRow.Delta.After, issued)
	failureRow := requireLedgerChange(t, stack, failureEvent)
	if failureRow.Outcome != audit.OutcomeFailed || failureRow.Error == nil ||
		failureRow.Error.Code != sqlAuditFailureCode || failureRow.Delta != nil {
		t.Fatalf("failure row Outcome = %s, Error = %+v, Delta = %+v; want audit.OutcomeFailed, Error.Code sqlAuditFailureCode, and nil Delta", failureRow.Outcome, failureRow.Error, failureRow.Delta)
	}
}

// TestRuntimeAuditMembershipRoleReplacement runs datagen.BootstrapIdentities,
// which creates the first actor and adds its membership with role 20. It sets
// the role to 15 with OrgMemberRepo.AddMember because no Tack caller sets
// role 15. A second datagen.BootstrapIdentities run for the same seed replaces
// role 15 with role 20, and a third run repeats role 20. The test then removes
// the membership with OrgMemberRepo.RemoveMember because the datagen callers
// of RemoveMember run only inside the search revocation checks. It requires
// one membership change event for each addition, replacement, and removal,
// and requires the repeated role to produce zero membership events. It
// compares ledger values and the relationship target with the stored
// membership. It requires the replacement event to use org.member_role_set
// and list /role in Changed. It also checks the user creation event's Changed
// fields, protected references, and omission of the raw email and display
// name.
func TestRuntimeAuditMembershipRoleReplacement(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	principal := sqlAuditPrincipal()
	members := postgres.NewOrgMemberRepo(stack.admin)
	dsn := stack.admin.Config().ConnString()
	seed := clock.Now().UnixNano()

	add := newMemberStep(t, principal, sqlAuditAdminRole)
	identities := bootstrapSQLAuditIdentities(t, add.ctx, dsn, seed)
	orgID, holderID := identities.Workspaces[0].OrgID, identities.Workspaces[0].Actors[0].UserID
	demote := newMemberStep(t, principal, sqlAuditMemberRole)
	member := &org.Member{ID: uuid.Nil, OrgID: orgID, UserID: holderID, Role: demote.role, CreatedAt: time.Time{}}
	if err := members.AddMember(demote.ctx, member); err != nil {
		t.Fatalf("AddMember with role %d: got error %v, want nil", demote.role, err)
	}
	replace := newMemberStep(t, principal, sqlAuditAdminRole)
	bootstrapSQLAuditIdentities(t, replace.ctx, dsn, seed)
	repeat := newMemberStep(t, principal, sqlAuditAdminRole)
	bootstrapSQLAuditIdentities(t, repeat.ctx, dsn, seed)
	membership := requireMembership(t, members, orgID, holderID)
	removeCtx, remove := sqlAuditContext(t, principal)
	if err := members.RemoveMember(removeCtx, orgID, holderID); err != nil {
		t.Fatalf("RemoveMember error = %v, want nil", err)
	}
	requireNoMembership(t, members, orgID, holderID)

	addEvent := onlyEvent(t, memberEvents(t, stack.admin, add.operation, holderID), audit.PhaseChange)
	replaceEvent := onlyEvent(t, memberEvents(t, stack.admin, replace.operation, holderID), audit.PhaseChange)
	removeEvent := onlyEvent(t, memberEvents(t, stack.admin, remove, holderID), audit.PhaseChange)
	userEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, holderID), audit.PhaseChange)
	if repeatEvents := memberEvents(t, stack.admin, repeat.operation, holderID); len(repeatEvents) != 0 {
		t.Fatalf("membership events for an unchanged role = %d, want 0", len(repeatEvents))
	}
	holder, err := postgres.NewUserRepo(stack.admin).GetByID(t.Context(), holderID)
	if err != nil {
		t.Fatalf("UserRepo.GetByID(%s) error = %v, want nil", holderID, err)
	}
	startOutboxRelay(t, stack)

	requireUserChange(t, stack, userEvent, holder)
	want := memberRecord{ID: membership.ID, OrgID: orgID, UserID: holderID, Role: sqlAuditAdminRole, CreatedAt: membership.CreatedAt}
	addRow := requireLedgerChange(t, stack, addEvent)
	requireMemberTarget(t, addRow, want)
	requireMemberRecord(t, addRow.Delta.After, want)
	replaceRow := requireLedgerChange(t, stack, replaceEvent)
	want.Role = sqlAuditMemberRole
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
