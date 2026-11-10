package tools

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/domain/node"
)

func TestReferencePropertyToolUsesMetadataNames(t *testing.T) {
	resolver := &Resolver{
		entryPointSlug:    "workspace",
		entryPointTypeKey: "workspace",
		typeIndex: map[string]*node.NodeType{
			"phase": {TypeKey: "phase", Slug: "phase"},
		},
	}
	binding := NodeTypeBinding{Resolver: resolver}
	nodeType := &node.NodeType{
		TypeKey: "task",
		Slug:    "task",
		Name:    "Task",
	}
	propertyDef := &node.PropertyDef{
		Name:                   "phase_id",
		Type:                   node.PropertyTypeUUID,
		ReferenceTargetTypeKey: "phase",
	}

	tool := referencePropertyTool(nodeType, propertyDef, referencePropertyAlias(propertyDef.Name), binding)

	if tool.Name != "tack_set_task_phase" {
		t.Fatalf("tool name: got %q want tack_set_task_phase", tool.Name)
	}
}

func TestGettingStartedListsGeneratedReferenceSetters(t *testing.T) {
	taskType := &node.NodeType{
		TypeKey:    "task",
		Slug:       "task",
		PluralSlug: "tasks",
		Name:       "Task",
		AllowedOps: []node.Op{node.OpUpdate},
	}
	phaseType := &node.NodeType{TypeKey: "phase", Slug: "phase", Name: "Phase"}
	resolver := &Resolver{
		entryPointSlug:    "workspace",
		entryPointTypeKey: "workspace",
		typeIndex: map[string]*node.NodeType{
			"task":  taskType,
			"phase": phaseType,
		},
	}
	propertyDefs := []*node.PropertyDef{{
		Name:                   "phase_id",
		Type:                   node.PropertyTypeUUID,
		ReferenceTargetTypeKey: "phase",
	}}

	body := buildGettingStartedText(resolver, []*node.NodeType{taskType, phaseType}, propertyDefs)

	if !strings.Contains(body, "tack_set_task_phase") {
		t.Fatalf("getting started should list generated setter, got:\n%s", body)
	}
}
