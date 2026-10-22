package integration_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityFixedNodeTypesCorrectsCanLiveUnder stores the issue node
// type with the older CanLiveUnder list containing only project. A dry run
// of RunFixedNodeTypeCorrection must report the issue type as corrected
// without changing it. The execute run must store the seeded list. A second
// execute run must leave the issue type unchanged.
func TestNodeIntegrityFixedNodeTypesCorrectsCanLiveUnder(t *testing.T) {
	env := integration.SetupTestEnv(t)
	issueType := storedNodeType(t, env, "issue")
	seeded := slices.Clone(issueType.CanLiveUnder)
	issueType.CanLiveUnder = []string{"project"}
	if err := env.Stores.NodeTypes.Set(env.Ctx, issueType); err != nil {
		t.Fatalf("store the older issue node type: %v", err)
	}
	principal := audit.OperatorPrincipal{ID: uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca32"), Email: "operator@example.invalid", Name: "Operator", Source: "test"}
	ctx := audit.WithOperatorPrincipal(env.Ctx, principal)

	preview, err := ops.RunFixedNodeTypeCorrection(ctx, env.Stores.NodeTypes, true)
	if err != nil || !slices.Contains(preview.CorrectedIDs, issueType.ID) {
		t.Fatalf("dry run = %+v, err %v; want issue type %s corrected", preview, err, issueType.ID)
	}
	if got := storedNodeType(t, env, "issue").CanLiveUnder; !slices.Equal(got, []string{"project"}) {
		t.Fatalf("issue CanLiveUnder after the dry run = %v, want [project]", got)
	}
	applied, err := ops.RunFixedNodeTypeCorrection(ctx, env.Stores.NodeTypes, false)
	if err != nil || !slices.Contains(applied.CorrectedIDs, issueType.ID) {
		t.Fatalf("execute run = %+v, err %v; want issue type %s corrected", applied, err, issueType.ID)
	}
	if got := storedNodeType(t, env, "issue").CanLiveUnder; !slices.Equal(got, seeded) {
		t.Fatalf("issue CanLiveUnder after the execute run = %v, want %v", got, seeded)
	}
	repeated, err := ops.RunFixedNodeTypeCorrection(ctx, env.Stores.NodeTypes, false)
	if err != nil || slices.Contains(repeated.CorrectedIDs, issueType.ID) {
		t.Fatalf("second execute run = %+v, err %v; want issue type %s unchanged", repeated, err, issueType.ID)
	}
}

// storedNodeType returns the test organization's stored node type for
// typeKey.
func storedNodeType(t *testing.T, env *integration.TestEnv, typeKey string) *node.NodeType {
	t.Helper()
	types, err := env.Stores.NodeTypes.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}
	for _, nodeType := range types {
		if nodeType.TypeKey == typeKey {
			return nodeType
		}
	}
	t.Fatalf("organization %s has no node type %s", env.OrgID, typeKey)
	return nil
}
