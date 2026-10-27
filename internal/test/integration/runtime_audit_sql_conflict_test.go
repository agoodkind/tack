package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/org"
)

const (
	// conflictBlockerRole is the role that the conflicting transaction stores.
	conflictBlockerRole = 15
	// conflictMemberRole is the role that AddMember stores.
	conflictMemberRole = 20
	// conflictWaitDeadline limits the wait for AddMember to block on the
	// conflicting transaction.
	conflictWaitDeadline = 30 * time.Second
	// conflictPollInterval is the pause between checks of pg_stat_activity.
	conflictPollInterval = 50 * time.Millisecond
)

// TestRuntimeAuditSQLMembershipConflictRetry: AddMember's SELECT ... FOR UPDATE
// cannot see the uncommitted membership. Its insert waits for the open transaction.
// After that transaction commits, YugabyteDB aborts the waiting insert with
// SQLSTATE 40001 because the committed row is newer than AddMember's transaction snapshot.
// YugabyteDB retries only a transaction's first statement itself; AddMember's
// insert is the second statement.
func TestRuntimeAuditSQLMembershipConflictRetry(t *testing.T) {
	admin := ledgerAdminPool(t)
	principal := sqlAuditPrincipal()
	holder := createSQLAuditUser(t, admin, principal)
	orgID := uuid.Must(uuid.NewV7())

	blocker, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the conflicting transaction: got error %v, want nil", err)
	}
	t.Cleanup(func() { _ = blocker.Rollback(context.WithoutCancel(t.Context())) })
	if _, err := blocker.Exec(t.Context(), readTestSQL(t, "runtime_audit_member_conflict_insert.sql"),
		uuid.Must(uuid.NewV7()), orgID, holder.ID, conflictBlockerRole); err != nil {
		t.Fatalf("insert the conflicting membership: got error %v, want nil", err)
	}

	ctx, operation := sqlAuditContext(t, principal)
	member := &org.Member{ID: uuid.Nil, OrgID: orgID, UserID: holder.ID, Role: conflictMemberRole, CreatedAt: time.Time{}}
	added := make(chan error, 1)
	go func() { added <- postgres.NewOrgMemberRepo(admin).AddMember(ctx, member) }()
	waitForBlockedMemberInsert(t, admin, added)
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatalf("commit the conflicting transaction: got error %v, want nil", err)
	}
	if err := <-added; err != nil {
		t.Fatalf("AddMember after a conflicting commit: got error %v, want nil", err)
	}

	stored := requireMemberRole(t, admin, orgID, holder.ID)
	if stored.Role != conflictMemberRole || stored.ID != member.ID {
		t.Fatalf("stored membership = %+v, want role %d and ID %s", stored, conflictMemberRole, member.ID)
	}
	changes, failures := 0, 0
	for _, event := range sqlOutboxEvents(t, admin, operation.Context.OperationID) {
		switch event.Context.Phase {
		case audit.PhaseChange:
			changes++
			if event.Verb != string(audit.VerbOrgMemberRoleSet) {
				t.Fatalf("change event verb = %s, want %s", event.Verb, audit.VerbOrgMemberRoleSet)
			}
		case audit.PhaseResult:
			failures++
		}
	}
	if changes != 1 || failures != 0 {
		t.Fatalf("outbox events for operation %s = %d changes and %d results, want 1 change and 0 results",
			operation.Context.OperationID, changes, failures)
	}
}

// waitForBlockedMemberInsert waits until another session runs the membership
// insert of AddMember. It fails the test when AddMember returns first or the
// deadline passes.
func waitForBlockedMemberInsert(t *testing.T, pool *pgxpool.Pool, added <-chan error) {
	t.Helper()
	query := readTestSQL(t, "runtime_audit_member_conflict_waiting.sql")
	deadline := clock.Now().Add(conflictWaitDeadline)
	for clock.Now().Before(deadline) {
		select {
		case err := <-added:
			t.Fatalf("AddMember returned before the conflicting transaction committed: %v", err)
		default:
		}
		var waiting int
		if err := pool.QueryRow(t.Context(), query).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: got error %v, want nil", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(conflictPollInterval)
	}
	t.Fatalf("AddMember did not block on the conflicting transaction within %s", conflictWaitDeadline)
}

// requireMemberRole returns the membership for userID in orgID.
// It fails the test if ListMembers fails or the match count is not 1.
func requireMemberRole(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID) *org.Member {
	t.Helper()
	listed, err := postgres.NewOrgMemberRepo(pool).ListMembers(t.Context(), orgID)
	if err != nil {
		t.Fatalf("ListMembers(%s): got error %v, want nil", orgID, err)
	}
	var found []*org.Member
	for _, stored := range listed {
		if stored.UserID == userID {
			found = append(found, stored)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ListMembers(%s) has %d memberships for user %s, want 1", orgID, len(found), userID)
	}
	return found[0]
}
