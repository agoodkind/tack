package integration_test

import (
	"slices"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

func storeStaleParentIndexFixture(
	t *testing.T,
	env *integration.TestEnv,
	nodeID uuid.UUID,
	oldParentID uuid.UUID,
) {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), nodeAuditTransactionTimeout)
	if err != nil {
		t.Fatalf("open stale parent index fixture store: %v", err)
	}

	_, err = database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(
			parentMigrationKey(tuple.Tuple{
				"node_by_property",
				env.OrgID.String(),
				"issue",
				"parent_id",
				[]byte(quoted(oldParentID.String())),
				nodeID.String(),
			}),
			[]byte{},
		)

		return nil, nil
	})
	if err != nil {
		t.Fatalf("store stale parent index for node %s: %v", nodeID, err)
	}
}

func requireParentMigrationIndexes(
	t *testing.T,
	env *integration.TestEnv,
	projectID uuid.UUID,
	nodeIDs []uuid.UUID,
	removed bool,
	oldParentIDs ...uuid.UUID,
) {
	t.Helper()
	wantLegacy := 1
	if removed {
		wantLegacy = 0
	}

	for _, oldParentID := range oldParentIDs {
		legacy, err := env.Stores.Nodes.ListByProperty(
			env.Ctx,
			env.OrgID,
			"issue",
			"parent_id",
			quoted(oldParentID.String()),
		)
		if err != nil || len(legacy) != wantLegacy {
			t.Fatalf(
				"legacy parent index for %s = %+v, error %v, removed=%t",
				oldParentID,
				legacy,
				err,
				removed,
			)
		}
	}

	issues, err := env.Stores.Nodes.ListByProperty(
		env.Ctx,
		env.OrgID,
		"issue",
		"scope_id",
		quoted(projectID.String()),
	)
	if err != nil || len(issues) != len(nodeIDs) {
		t.Fatalf(
			"scope index for %s = %+v, error %v, want issues %v",
			projectID,
			issues,
			err,
			nodeIDs,
		)
	}

	for _, issue := range issues {
		if !slices.Contains(nodeIDs, issue.ID) ||
			string(issue.Props["scope_id"]) != string(quoted(projectID.String())) {
			t.Fatalf("scope index returned unexpected issue %+v for project %s", issue, projectID)
		}
	}
}
