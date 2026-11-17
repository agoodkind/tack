package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

func TestParentRelationshipMigrationRefusesConflictingParent(t *testing.T) {
	env, project := parentMigrationRefusalScope(t)
	issue := createRepairIssue(t, env, project, "Conflicting parent readers")
	storeLegacyParentFixture(t, env, issue, uuid.Must(uuid.NewV7()), false)
	requireParentMigrationRefused(t, []uuid.UUID{issue}, "disagree")
}

func TestParentRelationshipMigrationRefusesAmbiguousParents(t *testing.T) {
	env, project := parentMigrationRefusalScope(t)
	issue := createRepairIssue(t, env, project, "Ambiguous parent readers")
	otherParent := createRepairIssue(t, env, project, "Additional parent")
	err := env.Stores.Relationships.Add(
		env.Ctx,
		&node.Relationship{
			OrgID:        env.OrgID,
			SourceID:     issue,
			RelationType: node.RelChildOf,
			TargetID:     otherParent,
			CreatedBy:    uuid.Must(uuid.NewV7()),
			CreatedAt:    time.Now(),
			Props:        nil,
		},
	)
	if err != nil {
		t.Fatalf("add ambiguous parent fixture: %v", err)
	}

	requireParentMigrationRefused(t, []uuid.UUID{issue}, "ambiguous")
}

func TestParentRelationshipMigrationRefusesGraphOnlyParent(t *testing.T) {
	env, project := parentMigrationRefusalScope(t)
	issue := createRepairIssue(t, env, project, "Graph-only parent")
	removePrimaryParentFixture(t, env, issue)
	view, err := env.Stores.Views.Get(env.Ctx, issue)
	if err != nil || view == nil {
		t.Fatalf("read graph-only view: %v", err)
	}

	delete(view.Props, "parent_id")
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("encode graph-only view: %v", err)
	}

	database, err := fdbadapter.Open(testenv.FoundationDB(t), nodeAuditTransactionTimeout)
	if err != nil {
		t.Fatalf("open graph-only fixture store: %v", err)
	}

	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("create graph-only fixture transaction: %v", err)
	}
	defer transaction.Cancel()
	transaction.Set(
		parentMigrationKey(tuple.Tuple{
			"node_view",
			env.OrgID.String(),
			view.NodeType,
			issue.String(),
		}),
		encoded,
	)
	if err := transaction.Commit().Get(); err != nil {
		t.Fatalf("commit graph-only fixture: %v", err)
	}

	requireParentMigrationRefused(t, []uuid.UUID{issue}, "disagree")
}
