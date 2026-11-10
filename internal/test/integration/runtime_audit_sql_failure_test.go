package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

// sqlAuditWriteFailure is the error text of a failed operator outbox write
// inside a repository change transaction.
const sqlAuditWriteFailure = "write audit event"

// requireAuditWriteFailure requires err to report a failed operator outbox
// write.
func requireAuditWriteFailure(t *testing.T, step string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sqlAuditWriteFailure) {
		t.Fatalf("%s error = %v, want an error that contains %q", step, err, sqlAuditWriteFailure)
	}
}

// TestRuntimeAuditSQLFailedUserCreate runs datagen.BootstrapIdentities for a
// new seed through a login that cannot write the operator outbox. The run
// must fail at the first user creation. The users, api_tokens, and
// org_members rows of every planned actor must stay absent, and the outbox
// must not gain events for the operation.
func TestRuntimeAuditSQLFailedUserCreate(t *testing.T) {
	admin := ledgerAdminPool(t)
	restricted := changeOnlyDSN(t, admin)
	seed := clock.Now().UnixNano()
	userIDs := actorUserIDs(plannedSQLAuditIdentities(t, seed))
	before := sqlRows(t, admin, userIDs)
	ctx, operation := sqlAuditContext(t, sqlAuditPrincipal())

	_, err := datagen.BootstrapIdentities(ctx, sqlAuditConfig(t, restricted), seed, sqlAuditScale(t))
	requireAuditWriteFailure(t, "BootstrapIdentities", err)
	requireSQLUnchanged(t, admin, userIDs, before, operation.Context.OperationID)
}

// TestRuntimeAuditSQLFailedMembershipAdd bootstraps identities, removes the
// first actor's membership, and reruns datagen.BootstrapIdentities for the
// same seed through a login that cannot write the operator outbox. The rerun
// must fail when it adds the membership again and leave every stored row
// and the outbox unchanged.
func TestRuntimeAuditSQLFailedMembershipAdd(t *testing.T) {
	admin := ledgerAdminPool(t)
	principal := sqlAuditPrincipal()
	seed := clock.Now().UnixNano()
	setupCtx, _ := sqlAuditContext(t, principal)
	identities := bootstrapSQLAuditIdentities(t, setupCtx, testenv.Ledger(t), seed)
	workspace := identities.Workspaces[0]
	removeCtx, _ := sqlAuditContext(t, principal)
	if err := postgres.NewOrgMemberRepo(admin).RemoveMember(removeCtx, workspace.OrgID, workspace.Actors[0].UserID); err != nil {
		t.Fatalf("RemoveMember error = %v, want nil", err)
	}
	requireFailedRerun(t, admin, identities, seed)
}

// TestRuntimeAuditSQLFailedMembershipRoleChange bootstraps identities, sets
// the first actor's role to 15 with OrgMemberRepo.AddMember, and reruns
// datagen.BootstrapIdentities for the same seed through a login that cannot
// write the operator outbox. The rerun must fail when it changes the role
// back to 20 and leave every stored row and the outbox unchanged. No Tack
// caller sets role 15.
func TestRuntimeAuditSQLFailedMembershipRoleChange(t *testing.T) {
	admin := ledgerAdminPool(t)
	principal := sqlAuditPrincipal()
	seed := clock.Now().UnixNano()
	setupCtx, _ := sqlAuditContext(t, principal)
	identities := bootstrapSQLAuditIdentities(t, setupCtx, testenv.Ledger(t), seed)
	workspace := identities.Workspaces[0]
	demoteCtx, _ := sqlAuditContext(t, principal)
	demoted := &org.Member{
		ID: uuid.Nil, OrgID: workspace.OrgID, UserID: workspace.Actors[0].UserID, Role: sqlAuditMemberRole, CreatedAt: time.Time{},
	}
	if err := postgres.NewOrgMemberRepo(admin).AddMember(demoteCtx, demoted); err != nil {
		t.Fatalf("AddMember with role %d error = %v, want nil", sqlAuditMemberRole, err)
	}
	requireFailedRerun(t, admin, identities, seed)
}

// requireFailedRerun reruns datagen.BootstrapIdentities for seed through a
// login that cannot write the operator outbox. It requires the rerun to fail
// on the audit write and the rows of every actor in identities and the outbox
// to stay unchanged.
func requireFailedRerun(t *testing.T, admin *pgxpool.Pool, identities datagen.Identities, seed int64) {
	t.Helper()
	userIDs := actorUserIDs(identities)
	before := sqlRows(t, admin, userIDs)
	restricted := changeOnlyDSN(t, admin)
	ctx, operation := sqlAuditContext(t, sqlAuditPrincipal())
	_, err := datagen.BootstrapIdentities(ctx, sqlAuditConfig(t, restricted), seed, sqlAuditScale(t))
	requireAuditWriteFailure(t, "BootstrapIdentities rerun", err)
	requireSQLUnchanged(t, admin, userIDs, before, operation.Context.OperationID)
}

// TestRuntimeAuditSQLFailedMembershipRemove bootstraps identities and calls
// OrgMemberRepo.RemoveMember for the first actor through a login that cannot
// write the operator outbox. The removal must fail and leave every stored row
// and the outbox unchanged. The datagen callers of RemoveMember run only
// inside the search revocation checks.
func TestRuntimeAuditSQLFailedMembershipRemove(t *testing.T) {
	admin := ledgerAdminPool(t)
	principal := sqlAuditPrincipal()
	setupCtx, _ := sqlAuditContext(t, principal)
	identities := bootstrapSQLAuditIdentities(t, setupCtx, testenv.Ledger(t), clock.Now().UnixNano())
	workspace := identities.Workspaces[0]
	userIDs := actorUserIDs(identities)
	before := sqlRows(t, admin, userIDs)

	removeCtx, operation := sqlAuditContext(t, principal)
	err := postgres.NewOrgMemberRepo(changeOnlyPool(t, admin)).RemoveMember(removeCtx, workspace.OrgID, workspace.Actors[0].UserID)
	requireAuditWriteFailure(t, "RemoveMember", err)
	requireSQLUnchanged(t, admin, userIDs, before, operation.Context.OperationID)
}

// TestRuntimeAuditSQLFailedTokenRevoke bootstraps identities and revokes the
// first actor's datagen token with ops.RevokeAuthToken through a login that
// cannot write the operator outbox. The revocation must fail and leave every
// stored row and the outbox unchanged.
func TestRuntimeAuditSQLFailedTokenRevoke(t *testing.T) {
	admin := ledgerAdminPool(t)
	principal := sqlAuditPrincipal()
	setupCtx, _ := sqlAuditContext(t, principal)
	identities := bootstrapSQLAuditIdentities(t, setupCtx, testenv.Ledger(t), clock.Now().UnixNano())
	holderID := identities.Workspaces[0].Actors[0].UserID
	tokens, err := postgres.NewTokenRepo(admin).List(t.Context(), holderID)
	if err != nil || len(tokens) == 0 {
		t.Fatalf("TokenRepo.List(%s) = %d tokens, error = %v; want at least one token and nil error", holderID, len(tokens), err)
	}
	userIDs := actorUserIDs(identities)
	before := sqlRows(t, admin, userIDs)

	ctx, commandID := sqlOperatorContext(t.Context(), principal)
	_, err = ops.RevokeAuthToken(ctx, changeOnlyPool(t, admin), tokens[0].ID)
	requireAuditWriteFailure(t, "RevokeAuthToken", err)
	requireSQLUnchanged(t, admin, userIDs, before, commandID)
}
