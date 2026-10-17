package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchProjectionIndexesValidContent verifies that repeated renders
// return identical text and diagnostics. The text must include the node name
// and included value, and omit an explicitly excluded value, an invalid label
// value, and two undeclared values. Diagnostics must identify the node and
// list the invalid and undeclared properties in property name order. They
// must omit the excluded property.
func TestSearchProjectionIndexesValidContent(t *testing.T) {
	env := integration.SetupTestEnv(t)
	env.Stores.EnableSearchWork()
	orgID := uuid.Must(uuid.NewV7())
	included, excluded, labeled := opaqueName("p"), opaqueName("p"), opaqueName("c")
	laterName, earlierName := opaqueName("b"), opaqueName("a")
	optioned, restricted := opaqueName("o"), opaqueName("r")
	scalar := node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}}
	definitions := []*node.PropertyDef{
		projectionDefinition(orgID, included, scalar),
		projectionDefinition(orgID, excluded, node.SearchProjection{Include: false, Order: 1, Rule: node.TextRule{Mode: node.TextRuleScalar}}),
		projectionDefinition(orgID, labeled, node.SearchProjection{
			Include: true, Order: 2, Rule: node.TextRule{Mode: node.TextRuleLabels, Labels: map[string]string{"declared": "Declared label"}},
		}),
		{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: optioned, Type: node.PropertyTypeSelect,
			Options: []node.EnumOption{{Key: "planned", Label: "Planned"}},
		},
		{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: restricted, Type: node.PropertyTypeText,
			AppliesToFeatures: []string{node.FeatureHasDueDates}, Search: &scalar,
		},
	}
	want := map[string]expectedDiagnostic{
		earlierName: {reason: node.DiagnosticUndeclared, definitionID: uuid.Nil},
		laterName:   {reason: node.DiagnosticUndeclared, definitionID: uuid.Nil},
		labeled:     {reason: node.DiagnosticInvalidValue, definitionID: definitions[2].ID},
		optioned:    {reason: node.DiagnosticInvalidValue, definitionID: definitions[3].ID},
		restricted:  {reason: node.DiagnosticInapplicable, definitionID: definitions[4].ID},
	}
	typeKey := opaqueName("n")
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: typeKey, TypeKey: typeKey, Slug: typeKey, PluralSlug: typeKey + "s",
		Features: node.Features{node.FeatureIsEntryPoint},
	}
	for _, definition := range definitions {
		kind.PropertyDefIDs = append(kind.PropertyDefIDs, definition.ID)
		if err := env.Stores.PropertyDefs.Set(env.Ctx, definition); err != nil {
			t.Fatalf("store property definition %s: %v", definition.Name, err)
		}
	}
	if err := env.Stores.NodeTypes.Set(env.Ctx, kind); err != nil {
		t.Fatalf("store node type %s: %v", typeKey, err)
	}
	props := map[string]json.RawMessage{
		included:    json.RawMessage(`"amber included value"`),
		excluded:    json.RawMessage(`"amber excluded value"`),
		labeled:     json.RawMessage(`"undeclared-label"`),
		laterName:   json.RawMessage(`"amber undeclared later"`),
		earlierName: json.RawMessage(`"amber undeclared earlier"`),
		optioned:    json.RawMessage(`"amber-missing-option"`),
		restricted:  json.RawMessage(`"amber inapplicable value"`),
	}
	nodeID := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()
	value := &node.Node{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: "Projection node", Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: "Projection node", Props: props, CreatedAt: now, UpdatedAt: now}
	if err := env.Stores.Nodes.Set(env.Ctx, value, view); err != nil {
		t.Fatalf("store node %s: %v", nodeID, err)
	}

	reader := env.Stores.SearchContent(env.Stores.SearchPolicySet())
	request := searchdomain.ContentRequest{NodeID: nodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: 4096, SearchGeneration: 0}
	first, err := reader.Content(env.Ctx, request)
	if err != nil {
		t.Fatalf("read the search text of node %s: %v", nodeID, err)
	}
	second, err := reader.Content(env.Ctx, request)
	if err != nil || second.Text != first.Text || !slices.Equal(second.Diagnostics, first.Diagnostics) {
		t.Fatalf("a second read returned %+v, error %v, want the first read %+v", second, err, first)
	}
	if want := "Projection node\namber included value\n"; first.Text != want || !first.Done {
		t.Fatalf("search text = %q, done %t, want %q on one page", first.Text, first.Done, want)
	}
	wantNames := make([]string, 0, len(want))
	for name := range want {
		wantNames = append(wantNames, name)
	}
	slices.Sort(wantNames)
	names := make([]string, 0, len(first.Diagnostics))
	for _, diagnostic := range first.Diagnostics {
		names = append(names, diagnostic.PropertyName)
		requireDiagnostic(t, diagnostic, nodeID, want[diagnostic.PropertyName])
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("diagnostics name %v, want %v", names, wantNames)
	}
}

func opaqueName(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}

// expectedDiagnostic stores the expected reason and definition ID of one
// diagnostic.
type expectedDiagnostic struct {
	reason       node.DiagnosticReason
	definitionID uuid.UUID
}

// requireDiagnostic checks that diagnostic identifies nodeID and the
// revision, reports the expected reason, and omits the source value.
func requireDiagnostic(t *testing.T, diagnostic node.PropertyDiagnostic, nodeID uuid.UUID, want expectedDiagnostic) {
	t.Helper()
	if diagnostic.NodeID != nodeID || diagnostic.Revision == "" || diagnostic.ProjectionVersion == "" ||
		diagnostic.Reason != want.reason || diagnostic.PropertyDefID != want.definitionID || strings.Contains(diagnostic.Message, "amber") ||
		strings.Contains(diagnostic.Message, "undeclared-label") {
		t.Fatalf("diagnostic %+v, want node %s, reason %s, and definition %s without the source value", diagnostic, nodeID, want.reason, want.definitionID)
	}
}

// projectionDefinition returns a definition with an unfamiliar property type
// and the explicit search declaration search.
func projectionDefinition(orgID uuid.UUID, name string, search node.SearchProjection) *node.PropertyDef {
	return &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Type: node.PropertyType(opaqueName("t")), Search: &search,
	}
}
