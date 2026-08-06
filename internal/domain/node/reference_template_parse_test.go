package node

import "testing"

func TestReferenceTemplateParsesScopedSequence(t *testing.T) {
	got, err := issueTemplate().Parse("FAN-13")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ScopeRefs[FeatureIsScope] != "FAN" {
		t.Fatalf("scope reference = %q, want FAN", got.ScopeRefs[FeatureIsScope])
	}
	if got.Props["sequence"] != "13" {
		t.Fatalf("sequence = %q, want 13", got.Props["sequence"])
	}
}

func TestReferenceTemplateParsesHyphenatedScope(t *testing.T) {
	got, err := issueTemplate().Parse("FAN-QA-13")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ScopeRefs[FeatureIsScope] != "FAN-QA" {
		t.Fatalf("scope reference = %q, want FAN-QA", got.ScopeRefs[FeatureIsScope])
	}
	if got.Props["sequence"] != "13" {
		t.Fatalf("sequence = %q, want 13", got.Props["sequence"])
	}
}

func TestReferenceTemplateParseRejectsMissingSeparator(t *testing.T) {
	if _, err := issueTemplate().Parse("FAN13"); err == nil {
		t.Fatal("Parse without separator: got nil error")
	}
}

// TestReferenceTemplateParsesNodeTypePart covers a template whose delimiter
// repeats: scope, hyphen, node type, hyphen, sequence. The scope must take
// only its own segment, not swallow the type's, and a hyphenated scope must
// still keep its surplus separators.
func TestReferenceTemplateParsesNodeTypePart(t *testing.T) {
	template := ReferenceTemplate{Parts: []ReferencePart{
		{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
		{Kind: ReferencePartLiteral, Value: "-"},
		{Kind: ReferencePartNodeType},
		{Kind: ReferencePartLiteral, Value: "-"},
		{Kind: ReferencePartProperty, Value: "sequence"},
	}}
	got, err := template.Parse("FAN-epic-1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ScopeRefs[FeatureIsScope] != "FAN" {
		t.Fatalf("scope reference = %q, want FAN", got.ScopeRefs[FeatureIsScope])
	}
	if got.NodeTypeKey != "epic" {
		t.Fatalf("node type = %q, want epic", got.NodeTypeKey)
	}
	if got.Props["sequence"] != "1" {
		t.Fatalf("sequence = %q, want 1", got.Props["sequence"])
	}

	hyphenated, err := template.Parse("FAN-QA-epic-7")
	if err != nil {
		t.Fatalf("Parse hyphenated scope: %v", err)
	}
	if hyphenated.ScopeRefs[FeatureIsScope] != "FAN-QA" {
		t.Fatalf("hyphenated scope = %q, want FAN-QA", hyphenated.ScopeRefs[FeatureIsScope])
	}
	if hyphenated.NodeTypeKey != "epic" || hyphenated.Props["sequence"] != "7" {
		t.Fatalf("hyphenated parse = %#v, want epic and 7", hyphenated)
	}
}

func TestReferenceTemplateParsesLeadingLiteral(t *testing.T) {
	template := ReferenceTemplate{Parts: []ReferencePart{
		{Kind: ReferencePartLiteral, Value: "ISSUE-"},
		{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
		{Kind: ReferencePartLiteral, Value: "-"},
		{Kind: ReferencePartProperty, Value: "sequence"},
	}}
	got, err := template.Parse("ISSUE-FAN-13")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ScopeRefs[FeatureIsScope] != "FAN" || got.Props["sequence"] != "13" {
		t.Fatalf("Parse = %#v, want FAN and 13", got)
	}
}
