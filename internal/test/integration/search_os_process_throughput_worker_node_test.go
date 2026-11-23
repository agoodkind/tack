package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/testenv"
)

func createThroughputWorkerNode(
	t *testing.T,
	fixture queryFixture,
	kind opaqueKind,
	parent uuid.UUID,
	actor uuid.UUID,
	name string,
	content string,
) uuid.UUID {
	t.Helper()
	stores := fixture.Stores
	members := postgres.NewOrgMemberRepo(testenv.LedgerPool(t, fixture.Config.DatabaseURL))
	svc, err := service.NewNodeService(
		stores.Nodes,
		stores.Views,
		stores.NodeTypes,
		stores.PropertyDefs,
		stores.Relationships,
		stores.NodeDeleter,
		members,
		stores.Relationships,
	)
	if err != nil {
		t.Fatalf("build the node service: %v", err)
	}

	result, err := svc.Create(
		WithTestOperation(t, t.Context()),
		service.CreateInput{
			ParentID:    parent,
			ScopeID:     parent,
			NodeTypeKey: kind.TypeKey,
			Name:        name,
			ActorID:     actor,
			Props: map[string]json.RawMessage{
				kind.IncludedKey: mustJSON(content),
				kind.ExcludedKey: mustJSON("excluded"),
			},
		},
	)
	if err != nil || result == nil || result.View == nil {
		t.Fatalf("create production worker fixture: %v", err)
	}

	stored, err := stores.Views.Get(WithTestOperation(t, t.Context()), result.View.ID)
	if err != nil || stored == nil {
		t.Fatalf("read production worker fixture: %v", err)
	}

	var scopeID string
	if err := json.Unmarshal(stored.Props["scope_id"], &scopeID); err != nil ||
		scopeID != parent.String() {
		t.Fatalf("production worker fixture scope_id does not match its entry point: %v", err)
	}

	parents, err := stores.Relationships.ListBySource(
		WithTestOperation(t, t.Context()),
		stored.OrgID,
		stored.ID,
		node.RelChildOf,
	)
	if err != nil || len(parents) != 1 || parents[0].TargetID != parent {
		t.Fatalf(
			"production worker fixture child_of does not match its entry point: %v, %v",
			parents,
			err,
		)
	}

	return stored.ID
}
