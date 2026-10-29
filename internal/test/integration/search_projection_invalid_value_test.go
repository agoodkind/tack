package integration_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// legacyOptionText provides valid text for
	// TestSearchInvalidOptionKeepsSourceAndValidText.
	legacyOptionText = "juniper causeway lattice"
	// forbiddenOptionText provides valid text for
	// TestSearchForbiddenNodeWithWarningIsWithheld.
	forbiddenOptionText = "marigold viaduct ember"
	// rawPropertiesFence is the opening line of the JSON block in
	// tack_get_properties output.
	rawPropertiesFence = "```json\n"
)

// TestSearchInvalidOptionKeepsSourceAndValidText requires ops search verify to
// report an invalid select value with its property definition ID.
// It checks that tack_get_properties returns the stored value byte for byte,
// indexed page_text includes valid text and omits the option token, and
// tack_search returns the node for the valid text.
func TestSearchInvalidOptionKeepsSourceAndValidText(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	legacy := putLegacyOptionNode(t, fixture, fixture.Workspaces[0], "Legacy option node", legacyOptionText)
	requireInvalidOptionWarning(t, fixture, legacy)
	requireStoredProperty(t, fixture.Harness, legacy.NodeID, legacy.Definition.Name, legacy.Stored)
	// Public tack_search combines a lexical clause with a sparse semantic clause
	// that can return a node for a word absent from its pages.
	// This check reads indexed page_text to prove that the index omits the option.
	requireIndexedText(t, fixture, legacy.NodeID, legacyOptionText, legacy.Token)
	if results := integration.CallEverySearchPage(t, legacyOptionText, fixture.Harness); !slices.Contains(results, legacy.NodeID) {
		t.Fatalf("search for the valid text returned %v, want node %s", results, legacy.NodeID)
	}
}

// TestSearchForbiddenNodeWithWarningIsWithheld requires an invalid value warning
// and indexed page_text that includes valid text and omits the option token.
// It checks that page access keys permit the organization member but reject
// the caller, and that tack_search returns the node to the member but withholds
// it from the caller. After rewriting page access for the caller, it requires
// raw OpenSearch ranking to return the node and tack_search to withhold it.
func TestSearchForbiddenNodeWithWarningIsWithheld(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	caller, owner := fixture.Workspaces[0], integration.OtherOrganization(t, fixture)
	legacy := putLegacyOptionNode(t, fixture, owner, "Forbidden option node", forbiddenOptionText)
	requireInvalidOptionWarning(t, fixture, legacy)
	requireIndexedText(t, fixture, legacy.NodeID, forbiddenOptionText, legacy.Token)
	callerFilter := integration.CallerAccess(t, fixture, caller, integration.EntryPoint(t, fixture, caller))
	ownerFilter := integration.CallerAccess(t, fixture, owner, integration.EntryPoint(t, fixture, owner))
	for _, page := range integration.SearchNodePages(t, fixture, legacy.NodeID) {
		if !ownerFilter.Permits(page.Access.Keys) || callerFilter.Permits(page.Access.Keys) {
			t.Fatalf("page %s stores access keys %v; the member filter must permit them and the caller filter must reject them", page.ID, page.Access.Keys)
		}
	}
	member := integration.ActorHarness(fixture, owner, 0)
	if results := integration.CallEverySearchPage(t, forbiddenOptionText, member); !slices.Contains(results, legacy.NodeID) {
		t.Fatalf("search by a member of the organization returned %v, want node %s", results, legacy.NodeID)
	}
	if results := integration.CallEverySearchPage(t, forbiddenOptionText, fixture.Harness); slices.Contains(results, legacy.NodeID) {
		t.Fatalf("search by the caller returned the forbidden node %s", legacy.NodeID)
	}
	integration.CorruptIndexedAccess(t, fixture, legacy.NodeID, callerFilter)
	if !slices.Contains(integration.RawRankedNodes(t, fixture, callerFilter, forbiddenOptionText), legacy.NodeID) {
		t.Fatalf("OpenSearch ranking with the caller's filter did not return node %s after the page access rewrite", legacy.NodeID)
	}
	if results := integration.CallEverySearchPage(t, forbiddenOptionText, fixture.Harness); slices.Contains(results, legacy.NodeID) {
		t.Fatalf("tack_search returned forbidden node %s to the caller after the page access rewrite", legacy.NodeID)
	}
}

// legacyOptionNode records the node ID, select property definition, stored JSON
// value, and undeclared option token.
type legacyOptionNode struct {
	NodeID     uuid.UUID
	Definition *node.PropertyDef
	Stored     json.RawMessage
	Token      string
}

// putLegacyOptionNode declares a select property with one option and explicit
// search inclusion in workspace's organization. It creates an opaque node
// through NodeService.Create with validText in an included property.
// It stores an undeclared option through NodeStore.Set without validating the
// value against the definition, then drains search work. The option token
// does not occur in another field or node in the fixture.
func putLegacyOptionNode(t *testing.T, fixture integration.QueryFixture, workspace datagen.WorkspaceIdentity, name, validText string) legacyOptionNode {
	t.Helper()
	option := node.EnumOption{Key: opaqueName("k"), Label: opaqueName("l")}
	labels := node.TextRule{Mode: node.TextRuleLabels, Labels: map[string]string{option.Key: option.Label}}
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: workspace.OrgID, Name: opaqueName("s"), Type: node.PropertyTypeSelect,
		Options: []node.EnumOption{option}, Search: &node.SearchProjection{Include: true, Order: 2, Rule: labels},
	}
	if err := fixture.Stores.PropertyDefs.Set(integration.WithTestOperation(t, t.Context()), definition); err != nil {
		t.Fatalf("declare select property %s: %v", definition.Name, err)
	}
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entry := integration.EntryPoint(t, fixture, workspace)
	nodeID := integration.PutOpaqueNode(t, fixture, kind, entry, name, validText, "excluded")
	token := opaqueName("v")
	storeUncheckedProperty(t, fixture, workspace.OrgID, nodeID, definition.Name, token)
	integration.DrainSearchWork(t, fixture.Worker, 2000)
	return legacyOptionNode{NodeID: nodeID, Definition: definition, Stored: jsonString(t, token), Token: token}
}

// requireInvalidOptionWarning requires ops search verify to list an invalid
// value warning for the select property of legacy with its definition ID.
func requireInvalidOptionWarning(t *testing.T, fixture integration.QueryFixture, legacy legacyOptionNode) {
	t.Helper()
	warning, warned := listedWarning(t, fixture.Config, legacy.NodeID, legacy.Definition.Name)
	if !warned || warning.Reason != string(node.DiagnosticInvalidValue) || warning.PropertyDefID != legacy.Definition.ID.String() {
		t.Fatalf("ops search verify warning of node %s = %+v, listed %t, want an invalid value warning", legacy.NodeID, warning, warned)
	}
}

// requireStoredProperty reads nodeID's raw properties through
// tack_get_properties as the harness caller and compares the value of the
// property specified by name with want byte for byte.
func requireStoredProperty(t *testing.T, harness *integration.MCPHarness, nodeID uuid.UUID, name string, want json.RawMessage) {
	t.Helper()
	text := integration.CallToolSuccess(t, harness, "tack_get_properties", map[string]any{"node_id": nodeID.String()})
	start := strings.Index(text, rawPropertiesFence)
	end := strings.LastIndex(text, "\n```")
	if start < 0 || end < start+len(rawPropertiesFence) {
		t.Fatalf("tack_get_properties output has no JSON block:\n%s", text)
	}
	var props map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text[start+len(rawPropertiesFence):end]), &props); err != nil {
		t.Fatalf("decode the properties of node %s: %v\n%s", nodeID, err, text)
	}
	if !bytes.Equal(props[name], want) {
		t.Fatalf("tack_get_properties returned %s for property %s of node %s, want %s", props[name], name, nodeID, want)
	}
}

// requireIndexedText reads nodeID's active pages from the serving index and
// requires their joined page_text to contain present and omit absent.
func requireIndexedText(t *testing.T, fixture integration.QueryFixture, nodeID uuid.UUID, present, absent string) {
	t.Helper()
	var text strings.Builder
	for _, page := range integration.SearchNodePages(t, fixture, nodeID) {
		if page.PageText != nil {
			text.WriteString(*page.PageText)
		}
	}
	if indexed := text.String(); !strings.Contains(indexed, present) || strings.Contains(indexed, absent) {
		t.Fatalf("pages of node %s contain %q, want %q and not %q", nodeID, indexed, present, absent)
	}
}
