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
	// rawPropertiesClose is the line break and fence that close the JSON block in
	// tack_get_properties output.
	rawPropertiesClose = "\n```"
	// legacyOptionSlices is the maximum number of search worker slices that
	// putLegacyOptionNode runs to drain search work.
	legacyOptionSlices = 2000
	// declaredOptionText is the valid text of the second node that
	// putLegacyOptionNode creates.
	declaredOptionText = "declared option text"
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
	requireStoredProperty(t, fixture, legacy)
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

// legacyOptionNode records the organization ID, node ID, select property
// definition, stored JSON value, and undeclared option token.
type legacyOptionNode struct {
	OrgID      uuid.UUID
	NodeID     uuid.UUID
	Definition *node.PropertyDef
	Stored     json.RawMessage
	Token      string
}

// putLegacyOptionNode declares a select property with one option in the
// organization of workspace and explicitly includes the property in search.
// It creates an opaque node through NodeService.Create with validText in an
// included property, then stores an undeclared option through NodeStore.Set,
// which does not check the value against the definition. The option token
// derives from a new UUID and does not occur in another field or node in the
// fixture.
//
// It creates a second node with declaredOptionText and stores the declared
// option key through NodeStore.Set. After draining search work, it requires
// the second node's page_text to contain the option label. This check confirms
// that the select property renders valid values into page_text.
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
	declaredID := integration.PutOpaqueNode(t, fixture, kind, entry, name+" declared", declaredOptionText, "excluded")
	storeUncheckedProperty(t, fixture, workspace.OrgID, declaredID, definition.Name, option.Key)
	integration.DrainSearchWork(t, fixture.Worker, legacyOptionSlices)
	requireIndexedText(t, fixture, declaredID, option.Label, token)
	return legacyOptionNode{OrgID: workspace.OrgID, NodeID: nodeID, Definition: definition, Stored: jsonString(t, token), Token: token}
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

// requireStoredProperty calls tack_get_properties as the fixture harness caller
// to read the node's raw properties from its NodeListView row. It compares the
// select property value with legacy.Stored byte for byte, then reads the primary
// record through NodeStore.Get and repeats the comparison.
func requireStoredProperty(t *testing.T, fixture integration.QueryFixture, legacy legacyOptionNode) {
	t.Helper()
	nodeID, name := legacy.NodeID, legacy.Definition.Name
	arguments := datagen.ToolArguments{
		WorkspaceReference: "", ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: nodeID.String(), Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
	text := fixture.Harness.Call(t, "tack_get_properties", arguments).Text()
	start := strings.Index(text, rawPropertiesFence)
	end := strings.LastIndex(text, rawPropertiesClose)
	if start < 0 || end < start+len(rawPropertiesFence) {
		t.Fatalf("tack_get_properties output has no JSON block:\n%s", text)
	}
	var props map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text[start+len(rawPropertiesFence):end]), &props); err != nil {
		t.Fatalf("decode the properties of node %s: %v\n%s", nodeID, err, text)
	}
	if !bytes.Equal(props[name], legacy.Stored) {
		t.Fatalf("tack_get_properties returned %s for property %s of node %s, want %s", props[name], name, nodeID, legacy.Stored)
	}
	primary, err := fixture.Stores.Nodes.Get(integration.WithTestOperation(t, t.Context()), legacy.OrgID, nodeID)
	if err != nil || primary == nil {
		t.Fatalf("read the primary record of node %s: %v", nodeID, err)
	}
	if !bytes.Equal(primary.Props[name], legacy.Stored) {
		t.Fatalf("the primary record of node %s stores %s for property %s, want %s", nodeID, primary.Props[name], name, legacy.Stored)
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
