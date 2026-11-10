package tools

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/domain/node"
)

func TestGettingStartedDocumentsWorkflowStateTools(t *testing.T) {
	resolver := &Resolver{entryPointSlug: "workspace"}
	body := buildGettingStartedText(resolver, nil, nil)

	for _, want := range []string{
		"## Workflow states",
		"States are first-class nodes",
		"`tack_list_states`",
		"`tack_set_<tool-token>_state`",
		"Queues, filters, and list views read the node's state property",
		"Comments are not workflow signals",
		"Do not signal workflow changes in comments alone",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("getting started should contain %q:\n%s", want, body)
		}
	}
}

func TestReferencePropertyToolDescribesStateDiscovery(t *testing.T) {
	projectType := &node.NodeType{
		TypeKey:    "project",
		Slug:       "project",
		PluralSlug: "projects",
		Reference:  node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"},
	}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", Name: "Issue", CanLiveUnder: []string{"project"}}
	stateType := &node.NodeType{TypeKey: "state", Slug: "state", PluralSlug: "states", Name: "State"}
	resolver := &Resolver{
		entryPointSlug: "workspace",
		typeIndex: map[string]*node.NodeType{
			"project": projectType,
			"issue":   issueType,
			"state":   stateType,
		},
	}
	propertyDef := &node.PropertyDef{
		Name:                   "state_id",
		Type:                   node.PropertyTypeUUID,
		ReferenceTargetTypeKey: "state",
	}

	tool := referencePropertyTool(
		issueType,
		propertyDef,
		referencePropertyAlias(propertyDef.Name),
		NodeTypeBinding{Resolver: resolver},
	)

	for _, want := range []string{
		"Sets state_id on an issue",
		"Discover valid state values with tack_list_states",
	} {
		if !strings.Contains(tool.Description, want) {
			t.Fatalf("state setter description should contain %q:\n%s", want, tool.Description)
		}
	}
	if !strings.Contains(schemaDescription(t, tool, "state"), "Use tack_list_states") {
		t.Fatalf("state field should point agents to tack_list_states")
	}
	if !strings.Contains(schemaDescription(t, tool, "project_reference"), "project reference") {
		t.Fatalf("project_reference should explain project reference discovery")
	}
}
