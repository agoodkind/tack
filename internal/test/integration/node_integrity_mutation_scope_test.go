package integration_test

import (
	"encoding/json"
	"slices"
	"testing"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// scopeReferenceTemplate is the name of the issue reference template that
// TestNodeIntegrityMutationRefusesMoveOutsideScope declares.
const scopeReferenceTemplate = "reference"

// TestNodeIntegrityMutationRefusesMoveOutsideScope creates an issue in a
// project in one workspace and an epic in a project in another workspace.
// It calls NodeService.Update to set the issue's parent_id to the epic.
// The test requires an error that wraps domain.ErrInvalidArgument.
// The issue's primary record must remain unchanged. The issue must keep one
// child_of edge to the original project and the original reference key FAN-1.
func TestNodeIntegrityMutationRefusesMoveOutsideScope(t *testing.T) {
	env := integration.SetupTestEnv(t)
	declareScopeReference(t, env, "issue")
	home := createSchemaNode(t, env, env.OrgID, env.OrgID, "workspace", schemaProps("slug", quoted("home")))
	away := createSchemaNode(t, env, env.OrgID, env.OrgID, "workspace", schemaProps("slug", quoted("away")))
	origin := createSchemaNode(t, env, home.ID, home.ID, "project", map[string]json.RawMessage{
		"slug": quoted("fan"), "identifier": quoted("FAN"),
	})
	destination := createSchemaNode(t, env, away.ID, away.ID, "project", map[string]json.RawMessage{
		"slug": quoted("qat"), "identifier": quoted("QAT"),
	})
	epic := createSchemaNode(t, env, destination.ID, destination.ID, "epic", nil)
	mover := createSchemaNode(t, env, origin.ID, origin.ID, "issue", nil)
	before, _ := mutationSnapshot(t, env, mover.ID)

	_, err := env.NodeSvc.Update(env.Ctx, moveUpdate(mover.ID, epic.ID))
	requireError(t, err, domain.ErrInvalidArgument, "the move of issue "+mover.ID.String()+" under epic "+epic.ID.String())

	requireUnchanged(t, env, before)
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, mover.ID, node.RelChildOf)
	if err != nil || len(edges) != 1 || edges[0].TargetID != origin.ID {
		t.Fatalf("child_of edges of issue %s after the refused move = %v, %v, want one edge to %s", mover.ID, edges, err, origin.ID)
	}
	owner, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, scopeReferenceTemplate, "FAN-1")
	if err != nil || owner != mover.ID {
		t.Fatalf("FAN-1 owner after the refused move = %s, %v, want issue %s", owner, err, mover.ID)
	}
}

// declareScopeReference sets one primary reference template on each node type
// in typeKeys. The template renders the reference of the enclosing scope
// followed by a hyphen and the generated sequence.
func declareScopeReference(t *testing.T, env *integration.TestEnv, typeKeys ...string) {
	t.Helper()
	types, err := env.Stores.NodeTypes.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}
	for _, nodeType := range types {
		if !slices.Contains(typeKeys, nodeType.TypeKey) {
			continue
		}
		nodeType.ReferenceTemplates = []node.ReferenceTemplate{{
			Name: scopeReferenceTemplate, IsPrimary: true, Generated: "sequence",
			Parts: []node.ReferencePart{
				{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
				{Kind: node.ReferencePartLiteral, Value: "-"},
				{Kind: node.ReferencePartProperty, Value: "sequence"},
			},
		}}
		if err := env.Stores.NodeTypes.Set(env.Ctx, nodeType); err != nil {
			t.Fatalf("set the reference template of %s: %v", nodeType.TypeKey, err)
		}
	}
}
