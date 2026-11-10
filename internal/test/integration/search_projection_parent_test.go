package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchProjectionOmitsParentReference builds a workspace with a child
// project and a state child of that project. The text reference property
// definition targets state and does not declare search. The test asserts that
// the search text starts with "Reference node\n", contains "started\n", and
// does not contain the project ID or the referenced node ID.
func TestSearchProjectionOmitsParentReference(t *testing.T) {
	env := integration.SetupTestEnv(t)
	env.Stores.EnableSearchWork()
	orgID := uuid.Must(uuid.NewV7())
	if err := env.Seeder.SeedOrg(env.Ctx, orgID); err != nil {
		t.Fatalf("seed organization %s: %v", orgID, err)
	}
	referenceName := opaqueName("r")
	reference := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: referenceName, Type: node.PropertyTypeText, ReferenceTargetTypeKey: "state",
	}
	if err := env.Stores.PropertyDefs.Set(env.Ctx, reference); err != nil {
		t.Fatalf("store reference definition %s without a search declaration: %v", referenceName, err)
	}
	workspaceID := putHierarchyNode(t, env, orgID, "workspace", "Reference workspace", nil, uuid.Nil)
	parentID := putHierarchyNode(t, env, orgID, "project", "Reference project", nil, workspaceID)
	referencedID := uuid.Must(uuid.NewV7())
	props := map[string]json.RawMessage{
		"parent_id":   json.RawMessage(strconv.Quote(parentID.String())),
		"scope_id":    json.RawMessage(strconv.Quote(parentID.String())),
		referenceName: json.RawMessage(strconv.Quote(referencedID.String())),
		"group":       json.RawMessage(`"started"`),
	}
	nodeID := putHierarchyNode(t, env, orgID, "state", "Reference node", props, parentID)
	page, err := env.Stores.SearchContent(env.Stores.SearchPolicySet()).Content(env.Ctx, searchdomain.ContentRequest{
		NodeID: nodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: 4096, SearchGeneration: 0,
	})
	if err != nil {
		t.Fatalf("read the search text of node %s: %v", nodeID, err)
	}
	if !strings.HasPrefix(page.Text, "Reference node\n") || !strings.Contains(page.Text, "started\n") {
		t.Fatalf("search text = %q, want the name and the group label", page.Text)
	}
	for _, referenced := range []uuid.UUID{parentID, referencedID} {
		if strings.Contains(page.Text, referenced.String()) {
			t.Fatalf("search text %q contains the referenced node ID %s", page.Text, referenced)
		}
	}
}

// putHierarchyNode creates a node of typeKey with CreateAtomic. When parentID
// is not uuid.Nil, it stores one child_of relationship from the new node to
// parentID in the same transaction. It returns the new node ID.
func putHierarchyNode(
	t *testing.T,
	env *integration.TestEnv,
	orgID uuid.UUID,
	typeKey, name string,
	props map[string]json.RawMessage,
	parentID uuid.UUID,
) uuid.UUID {
	t.Helper()
	nodeID := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()
	value := &node.Node{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	var relationships []*node.Relationship
	if parentID != uuid.Nil {
		relationships = append(relationships, &node.Relationship{
			OrgID: orgID, SourceID: nodeID, RelationType: node.RelChildOf, TargetID: parentID, CreatedAt: now,
		})
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, value, view, relationships, nil, nil, nil); err != nil {
		t.Fatalf("create %s node %s: %v", typeKey, nodeID, err)
	}
	return nodeID
}
