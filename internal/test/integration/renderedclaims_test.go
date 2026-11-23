package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

func seededReferenceKey(
	t *testing.T,
	env *TestEnv,
	current *node.NodeView,
	scopeReference string,
) node.ReferenceKey {
	t.Helper()
	types, err := env.Stores.NodeTypes.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}

	for _, nodeType := range types {
		if nodeType.TypeKey != current.NodeType {
			continue
		}

		template := nodeType.PrimaryReferenceTemplate()
		if template == nil {
			t.Fatalf("node type %q has no primary reference template", current.NodeType)
		}

		encoded, renderErr := template.Render(node.ReferenceRenderInput{
			NodeTypeKey: current.NodeType,
			Props:       current.Props,
			ScopeRefs:   map[string]string{node.FeatureIsScope: scopeReference},
		})
		if renderErr != nil {
			t.Fatalf("render reference for %s: %v", current.ID, renderErr)
		}

		return node.ReferenceKey{TemplateName: template.Name, Encoded: encoded}
	}

	t.Fatalf("node type %q not seeded", current.NodeType)

	return node.ReferenceKey{}
}

func referenceTestNode(orgID uuid.UUID) *node.Node {
	now := clock.Now().UTC()

	return &node.Node{
		ID:        uuid.New(),
		OrgID:     orgID,
		NodeType:  "test",
		Name:      "Test",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func referenceTestView(current *node.Node) *node.NodeView {
	return &node.NodeView{
		ID:        current.ID,
		OrgID:     current.OrgID,
		NodeType:  current.NodeType,
		Name:      current.Name,
		CreatedAt: current.CreatedAt,
		UpdatedAt: current.UpdatedAt,
	}
}
