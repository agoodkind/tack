package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// repairConsoleFixture is the fixture for a repair console over the test
// stores and one project.
type repairConsoleFixture struct {
	env       *integration.TestEnv
	console   *ops.RepairConsole
	projectID uuid.UUID
}

// newRepairConsoleFixture creates a project and a repair console over the
// test stores.
func newRepairConsoleFixture(t *testing.T) repairConsoleFixture {
	t.Helper()
	env := integration.SetupTestEnv(t)
	stores := env.Stores
	members := postgres.NewOrgMemberRepo(env.Ops.Pool)
	console, err := ops.NewRepairConsole(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, members, stores.Relationships, stores.Relationships)
	if err != nil {
		t.Fatalf("build the repair console: %v", err)
	}
	return repairConsoleFixture{env: env, console: console, projectID: createSchemaProject(t, env)}
}

// legacyNode stores legacy property values on target through test storage
// setup. A JSON null value removes the property.
func (f repairConsoleFixture) legacyNode(t *testing.T, target *node.NodeView, legacy map[string]json.RawMessage) *node.NodeView {
	t.Helper()
	stored, err := f.env.Stores.Nodes.Get(f.env.Ctx, f.env.OrgID, target.ID)
	if err != nil || stored == nil {
		t.Fatalf("get node %s: %v, %v", target.ID, stored, err)
	}
	for name, value := range legacy {
		if string(value) == "null" {
			delete(stored.Props, name)
			continue
		}
		stored.Props[name] = value
	}
	view := *target
	view.Props = stored.Props
	if err := f.env.Stores.Nodes.Set(f.env.Ctx, stored, &view); err != nil {
		t.Fatalf("store legacy properties on node %s: %v", target.ID, err)
	}
	return storedStoreView(t, f.env.Stores, target.ID)
}

// legacyIssue creates an issue in the project and stores legacy property
// values on it.
func (f repairConsoleFixture) legacyIssue(t *testing.T, legacy map[string]json.RawMessage) *node.NodeView {
	t.Helper()
	issue := createSchemaNode(t, f.env, f.projectID, f.projectID, "issue", nil)
	return f.legacyNode(t, issue, legacy)
}

// uuidProp returns the UUID value for name in props, or uuid.Nil.
func uuidProp(props map[string]json.RawMessage, name string) uuid.UUID {
	value, err := strconv.Unquote(string(props[name]))
	if err != nil {
		return uuid.Nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

// legacyStatusProfile returns a profile for repairing state_id from the
// legacy status property, removing status, and selecting the candidate with
// the largest sort_order.
func legacyStatusProfile() *ops.RepairReferenceProfile {
	return &ops.RepairReferenceProfile{
		Name: "legacy-status", TargetProperty: "state_id", SourceFields: []string{"status"}, ScopeFields: []string{"scope_id"},
		CleanupBehavior: ops.RepairCleanupSourceFields, ConflictPolicy: ops.RepairConflictPreferHighestRank, RankProperty: "sort_order",
	}
}

// parentProfile returns a profile for repairing parent_id to a node of
// targetTypeKey using the legacy property source. When matchProperty is
// empty, matching uses the target type's reference strategy.
func parentProfile(targetTypeKey, matchProperty, source string) *ops.RepairReferenceProfile {
	return &ops.RepairReferenceProfile{
		Name: "legacy-parent", TargetTypeKey: targetTypeKey, TargetMatchProperty: matchProperty,
		SourceFields: []string{source}, ScopeFields: []string{"scope_id"},
	}
}
