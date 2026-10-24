package integration_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityFixedNodeTypesCorrectsCanLiveUnder stores the issue node
// type with the older CanLiveUnder list containing only project. A dry run
// of RunFixedNodeTypeCorrection must report the issue type as corrected
// without changing it. The execute run must store the seeded list. Both
// runs must report the stored and seeded CanLiveUnder lists as the before
// and after values in Changes. A second execute run must leave the issue
// type unchanged.
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
	wantChange := service.FixedNodeTypeChange{
		OrgID: env.OrgID, NodeTypeID: issueType.ID, TypeKey: "issue", Outcome: service.FixedOutcomeCorrected,
		CanLiveUnder: service.FixedListChange{Before: []string{"project"}, After: seeded},
	}
	requireNodeTypeChange(t, "dry run", preview.Changes, wantChange)
	applied, err := ops.RunFixedNodeTypeCorrection(ctx, env.Stores.NodeTypes, false)
	if err != nil || !slices.Contains(applied.CorrectedIDs, issueType.ID) {
		t.Fatalf("execute run = %+v, err %v; want issue type %s corrected", applied, err, issueType.ID)
	}
	requireNodeTypeChange(t, "execute run", applied.Changes, wantChange)
	if got := storedNodeType(t, env, "issue").CanLiveUnder; !slices.Equal(got, seeded) {
		t.Fatalf("issue CanLiveUnder after the execute run = %v, want %v", got, seeded)
	}
	repeated, err := ops.RunFixedNodeTypeCorrection(ctx, env.Stores.NodeTypes, false)
	if err != nil || slices.Contains(repeated.CorrectedIDs, issueType.ID) {
		t.Fatalf("second execute run = %+v, err %v; want issue type %s unchanged", repeated, err, issueType.ID)
	}
}

// requireNodeTypeChange finds the reported entry with the node type ID of
// want and encodes both entries as JSON. It fails when the entries differ
// or the reported list does not include that node type.
func requireNodeTypeChange(t *testing.T, run string, changes []service.FixedNodeTypeChange, want service.FixedNodeTypeChange) {
	t.Helper()
	encodedWant, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encode the expected change for node type %s: %v", want.NodeTypeID, err)
	}
	for _, change := range changes {
		if change.NodeTypeID != want.NodeTypeID {
			continue
		}
		encoded, err := json.Marshal(change)
		if err != nil {
			t.Fatalf("encode the %s change for node type %s: %v", run, change.NodeTypeID, err)
		}
		if string(encoded) != string(encodedWant) {
			t.Fatalf("%s change for node type %s = %s, want %s", run, want.NodeTypeID, encoded, encodedWant)
		}
		return
	}
	t.Fatalf("%s changes %+v do not include node type %s", run, changes, want.NodeTypeID)
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
