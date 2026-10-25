package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchExcludedPropertyRepairClearsWarning stores the label of an option
// in a select property that search text excludes. ops search verify must list
// an invalid value warning for the property. An update that changes only that
// property to the option key must clear the warning after the worker drains
// the search work.
func TestSearchExcludedPropertyRepairClearsWarning(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	entry := integration.EntryPoint(t, fixture, workspace)
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	option := node.EnumOption{Key: opaqueName("k"), Label: opaqueName("l")}
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: workspace.OrgID, Name: opaqueName("p"), Type: node.PropertyTypeSelect,
		Options: []node.EnumOption{option},
		Search:  &node.SearchProjection{Include: false, Order: 2, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := fixture.Stores.PropertyDefs.Set(integration.WithTestOperation(t, t.Context()), definition); err != nil {
		t.Fatalf("declare select property %s: %v", definition.Name, err)
	}
	affected := integration.PutOpaqueNode(t, fixture, kind, entry, "excluded repair node", "included value", "excluded value")
	storeUncheckedProperty(t, fixture, workspace.OrgID, affected, definition.Name, option.Label)
	integration.DrainSearchWork(t, fixture.Worker, 2000)
	warning, warned := listedWarning(t, fixture.Config, affected, definition.Name)
	if !warned || warning.Reason != string(node.DiagnosticInvalidValue) || warning.PropertyDefID != definition.ID.String() {
		t.Fatalf("ops search verify warning of node %s = %+v, listed %t, want an invalid value warning", affected, warning, warned)
	}

	stores := fixture.Stores
	nodes := service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships, stores.NodeDeleter)
	if _, err := nodes.Update(integration.WithTestOperation(t, t.Context()), service.UpdateInput{
		NodeID: affected, Props: map[string]json.RawMessage{definition.Name: jsonString(t, option.Key)},
	}); err != nil {
		t.Fatalf("repair property %s of node %s: %v", definition.Name, affected, err)
	}
	integration.DrainSearchWork(t, fixture.Worker, 2000)
	if _, warned := listedWarning(t, fixture.Config, affected, definition.Name); warned {
		t.Fatalf("ops search verify still lists the warning of node %s after the repair", affected)
	}
}

// listedPropertyWarning decodes the definition ID and reason of one property
// warning from the JSON output of ops search verify.
type listedPropertyWarning struct {
	NodeID        string `json:"node_id"`
	PropertyName  string `json:"property_name"`
	PropertyDefID string `json:"property_def_id"`
	Reason        string `json:"reason"`
}

// listedWarning runs ops search verify and returns the warning for the
// property propertyName of node nodeID. It reports false when ops search
// verify does not list that warning.
func listedWarning(t *testing.T, cfg *config.Config, nodeID uuid.UUID, propertyName string) (listedPropertyWarning, bool) {
	t.Helper()
	output, err := runAuditedCommand(t, cfg, "ops", "search", "verify")
	if err != nil {
		t.Fatalf("execute ops search verify: %v; output=%s", err, output)
	}
	var envelope struct {
		Result struct {
			Warnings []listedPropertyWarning `json:"warnings"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode ops search verify result: %v; output=%s", err, output)
	}
	for _, warning := range envelope.Result.Warnings {
		if warning.NodeID == nodeID.String() && warning.PropertyName == propertyName {
			return warning, true
		}
	}
	return listedPropertyWarning{}, false
}

// storeUncheckedProperty writes value under name on nodeID through
// NodeStore.Set, which does not validate the value against its definition.
func storeUncheckedProperty(t *testing.T, fixture integration.QueryFixture, orgID, nodeID uuid.UUID, name, value string) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	current, err := fixture.Stores.Nodes.Get(ctx, orgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read node %s: %v", nodeID, err)
	}
	view, err := fixture.Stores.Views.Get(ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read the view of node %s: %v", nodeID, err)
	}
	current.Props[name] = jsonString(t, value)
	view.Props[name] = jsonString(t, value)
	if err := fixture.Stores.Nodes.Set(ctx, current, view); err != nil {
		t.Fatalf("store property %s of node %s: %v", name, nodeID, err)
	}
}

// jsonString returns value encoded as a JSON string.
func jsonString(t *testing.T, value string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %q as JSON: %v", value, err)
	}
	return encoded
}
