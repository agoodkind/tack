package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// memberRole is the server actor's role in the second organization.
	memberRole = 20
	// harnessTransactionTimeout limits the duration of each FoundationDB transaction for identity lookup.
	harnessTransactionTimeout = 5 * time.Second
)

func TestRuntimeAuditMultiOrganizationRead(t *testing.T) {
	server := startPublicServer(t, nil)
	secondOrg := addSecondOrganization(t, server)
	organizations := []uuid.UUID{server.orgID, secondOrg}
	workspacesID := newRequestID(t, "multi-workspaces")
	workspaces := server.rpc(
		t,
		workspacesID,
		"tools/call",
		map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}},
	)
	ledger := startPublicLedger(t, server)

	cases := []struct {
		requestID string
		body      []byte
		verb      audit.Verb
	}{
		{workspacesID, workspaces.body, audit.VerbWorkspaceList},
	}
	for _, current := range cases {
		operationID := uuid.Nil

		for _, orgID := range organizations {
			row := ledger.row(t, orgID, current.requestID)
			requireReadRow(t, row, current.verb, audit.OutcomeAttempted, "", current.body)
			if operationID == uuid.Nil {
				operationID = row.Context.OperationID
			}

			if row.Context.OperationID != operationID {
				t.Fatalf(
					"event %s operation ID = %s, want %s",
					row.EventID,
					row.Context.OperationID,
					operationID,
				)
			}

		}
	}
}

// addSecondOrganization creates a second organization, adds the server actor as a member, and returns its ID.
func addSecondOrganization(t *testing.T, server *publicServer) uuid.UUID {
	t.Helper()
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("datagen.ParseScale failed: %v", err)
	}

	ctx := integration.WithTestOperation(t, t.Context())
	identities, err := datagen.BootstrapIdentities(
		ctx,
		server.config,
		clock.Now().UnixNano(),
		scale,
	)
	if err != nil {
		t.Fatalf("datagen.BootstrapIdentities failed for the second organization: %v", err)
	}

	pool, err := postgres.NewPool(t.Context(), server.config.DatabaseURL, nil)
	if err != nil {
		t.Fatalf("postgres.NewPool failed: %v", err)
	}

	t.Cleanup(pool.Close)
	secondOrg := identities.Workspaces[0].OrgID
	member := &org.Member{ID: uuid.Nil, OrgID: secondOrg, UserID: server.userID, Role: memberRole}
	if err := postgres.NewOrgMemberRepo(pool).AddMember(ctx, member); err != nil {
		t.Fatalf(
			"the test could not add user %s to organization %s: %v",
			server.userID,
			secondOrg,
			err,
		)
	}

	return secondOrg
}
