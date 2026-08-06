package node

import (
	"encoding/json"
	"testing"
)

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return b
}

// issueTemplate is the shape seeded for work-item types: the enclosing scope's
// reference, a hyphen, then the generated sequence property. The node type is
// deliberately absent, so an epic and an issue cannot both hold "FAN-1".
func issueTemplate() ReferenceTemplate {
	return ReferenceTemplate{
		Name:      "reference",
		IsPrimary: true,
		Generated: "sequence",
		Parts: []ReferencePart{
			{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
			{Kind: ReferencePartLiteral, Value: "-"},
			{Kind: ReferencePartProperty, Value: "sequence"},
		},
	}
}

func TestReferenceTemplateRendersScopedSequence(t *testing.T) {
	got, err := issueTemplate().Render(ReferenceRenderInput{
		NodeTypeKey: "issue",
		Props:       map[string]json.RawMessage{"sequence": rawJSON(t, 13)},
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "FAN-13" {
		t.Fatalf("Render = %q, want %q", got, "FAN-13")
	}
}

func TestReferenceTemplateRendersScopedProperty(t *testing.T) {
	template := ReferenceTemplate{
		Name:      "reference",
		IsPrimary: true,
		Parts: []ReferencePart{
			{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
			{Kind: ReferencePartLiteral, Value: "::"},
			{Kind: ReferencePartProperty, Value: "name"},
		},
	}
	got, err := template.Render(ReferenceRenderInput{
		NodeTypeKey: "state",
		Props:       map[string]json.RawMessage{"name": rawJSON(t, "In Progress")},
		ScopeRefs:   map[string]string{FeatureIsScope: "CLYDE"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "CLYDE::In Progress" {
		t.Fatalf("Render = %q, want %q", got, "CLYDE::In Progress")
	}
}

func TestReferenceTemplateRendersNodeTypePart(t *testing.T) {
	template := ReferenceTemplate{
		Name:      "reference",
		IsPrimary: true,
		Generated: "sequence",
		Parts: []ReferencePart{
			{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
			{Kind: ReferencePartLiteral, Value: "-"},
			{Kind: ReferencePartNodeType},
			{Kind: ReferencePartLiteral, Value: "-"},
			{Kind: ReferencePartProperty, Value: "sequence"},
		},
	}
	got, err := template.Render(ReferenceRenderInput{
		NodeTypeKey: "epic",
		Props:       map[string]json.RawMessage{"sequence": rawJSON(t, 1)},
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "FAN-epic-1" {
		t.Fatalf("Render = %q, want %q", got, "FAN-epic-1")
	}
}

func TestReferenceTemplateRenderMissingScopeRefFails(t *testing.T) {
	_, err := issueTemplate().Render(ReferenceRenderInput{
		NodeTypeKey: "issue",
		Props:       map[string]json.RawMessage{"sequence": rawJSON(t, 13)},
		ScopeRefs:   map[string]string{},
	})
	if err == nil {
		t.Fatal("Render with no scope reference: got nil error, want failure")
	}
}

func TestReferenceTemplateRenderMissingPropertyFails(t *testing.T) {
	_, err := issueTemplate().Render(ReferenceRenderInput{
		NodeTypeKey: "issue",
		Props:       map[string]json.RawMessage{},
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err == nil {
		t.Fatal("Render with no sequence property: got nil error, want failure")
	}
}

func TestPrimaryReferenceTemplateSelectsMarkedTemplate(t *testing.T) {
	nodeType := &NodeType{ReferenceTemplates: []ReferenceTemplate{
		{Name: "external"},
		{Name: "reference", IsPrimary: true},
	}}
	primary := nodeType.PrimaryReferenceTemplate()
	if primary == nil {
		t.Fatal("PrimaryReferenceTemplate() = nil")
	}
	if primary.Name != "reference" {
		t.Fatalf("PrimaryReferenceTemplate().Name = %q, want reference", primary.Name)
	}
}

func TestPrimaryReferenceTemplateReturnsNilWhenNoneDeclared(t *testing.T) {
	if primary := (&NodeType{}).PrimaryReferenceTemplate(); primary != nil {
		t.Fatalf("PrimaryReferenceTemplate() = %+v, want nil", primary)
	}
}
