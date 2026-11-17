package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityMutationUpdateKeepsLegacyProperty checks updates to an
// issue with an undeclared legacy property from test storage setup. A service
// update of a declared property must preserve the legacy value. A later
// update that sets the legacy property to JSON null must remove it.
func TestNodeIntegrityMutationUpdateKeepsLegacyProperty(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, issue.ID)
	if err != nil || stored == nil {
		t.Fatalf("get issue %s: %v, %v", issue.ID, stored, err)
	}

	stored.Props["parent_epic_title"] = quoted("Legacy epic")
	legacyView := *issue
	legacyView.Props = stored.Props
	if err := env.Stores.Nodes.Set(env.Ctx, stored, &legacyView); err != nil {
		t.Fatalf("store the legacy property on %s: %v", issue.ID, err)
	}

	update := service.UpdateInput{
		NodeID:              issue.ID,
		Name:                nil,
		Props:               schemaProps("priority", quoted("high")),
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
		ActorID:             uuid.New(),
	}
	if _, err := env.NodeSvc.Update(env.Ctx, update); err != nil {
		t.Fatalf("update priority of %s: %v", issue.ID, err)
	}

	if got := string(
		storedStoreView(t, env.Stores, issue.ID).Props["parent_epic_title"],
	); got != `"Legacy epic"` {
		t.Fatalf("parent_epic_title after the priority update = %s, want the legacy value", got)
	}

	update.Props = schemaProps("parent_epic_title", []byte("null"))
	if _, err := env.NodeSvc.Update(env.Ctx, update); err != nil {
		t.Fatalf("remove parent_epic_title of %s: %v", issue.ID, err)
	}

	if _, kept := storedStoreView(t, env.Stores, issue.ID).Props["parent_epic_title"]; kept {
		t.Fatalf("issue %s keeps parent_epic_title after its removal", issue.ID)
	}
}
