package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// putStoredBuiltIn stores a built-in property definition for name with
// the supplied search inclusion and feature list.
func (env *migrationEnv) putStoredBuiltIn(t *testing.T, name string, include bool, features []string) *node.PropertyDef {
	t.Helper()
	definition := &node.PropertyDef{
		ID: node.SystemPropID(env.OrgID, name), OrgID: env.OrgID, Name: name, Type: node.PropertyTypeText,
		AppliesToFeatures: features, Indexed: true, Options: nil, Required: false, DefaultValue: nil,
		DefaultReference: nil, ReferenceTargetTypeKey: "",
		Search: &node.SearchProjection{Include: include, Order: 50, Rule: node.TextRule{Mode: node.TextRuleScalar, Fields: nil, Items: nil, Labels: nil}},
	}
	if err := env.Stores.PropertyDefs.Set(integration.WithTestOperation(t, t.Context()), definition); err != nil {
		t.Fatalf("store built-in definition %s: %v", name, err)
	}
	return definition
}

func (env *migrationEnv) pageText(t *testing.T, nodeID uuid.UUID) string {
	t.Helper()
	var text strings.Builder
	for _, page := range env.pages(t, nodeID, false) {
		if page.PageText != nil {
			text.WriteString(*page.PageText)
		}
	}
	return text.String()
}

// TestSearchProjectionMigrationCorrectsStoredReferenceDefinitions checks
// corrections to definitions from an older seed. It stores parent_id with
// search inclusion and restricts scope_id to the sequence feature. It
// requires the correction to apply the seeded search exclusion to
// parent_id and the seeded applicability to scope_id. Each correction must
// record one property_def.update event with a delta. A second run must not
// change the definitions. After the worker runs, the child's pages must
// omit the parent UUID and retain the child's text. The child must have a
// later revision. Nodes without parent_id must keep their page count,
// revision, and projection version.
func TestSearchProjectionMigrationCorrectsStoredReferenceDefinitions(t *testing.T) {
	env := newMigrationEnv(t, 256)
	parentDefinition := env.putStoredBuiltIn(t, "parent_id", true, nil)
	scopeDefinition := env.putStoredBuiltIn(t, "scope_id", false, []string{node.FeatureHasSequenceID})
	unaffected := env.putNodes(t, 2, "copper meadow lantern")
	child := uuid.Must(uuid.NewV7())
	props := map[string]json.RawMessage{env.IncludedKey: encodeText(t, "zephyr orchard beacon"), "parent_id": encodeText(t, unaffected[0].String())}
	env.putNodeProps(t, child, props)
	env.runIdle(t)
	if !strings.Contains(env.pageText(t, child), unaffected[0].String()) {
		t.Fatal("the stored parent_id inclusion did not index the parent UUID")
	}
	before := map[uuid.UUID][]migrationPage{unaffected[0]: env.pages(t, unaffected[0], false), unaffected[1]: env.pages(t, unaffected[1], false)}
	childBefore := env.revisions(t, []uuid.UUID{child})

	principal := audit.OperatorPrincipal{ID: uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca31"), Email: "operator@example.invalid", Name: "Operator", Source: "test"}
	ctx := audit.WithOperatorPrincipal(integration.WithTestOperation(t, t.Context()), principal)
	preview, err := ops.RunFixedDefinitionCorrection(ctx, env.Stores.PropertyDefs, true)
	if err != nil || preview.Corrected != 2 {
		t.Fatalf("preview = %+v, err %v; want two corrections", preview, err)
	}
	applied, err := ops.RunFixedDefinitionCorrection(ctx, env.Stores.PropertyDefs, false)
	if err != nil || applied.Corrected != 2 || applied.Conflicted != 0 {
		t.Fatalf("correction = %+v, err %v; want two corrections", applied, err)
	}
	repeated, err := ops.RunFixedDefinitionCorrection(ctx, env.Stores.PropertyDefs, false)
	if err != nil || repeated.Corrected != 0 {
		t.Fatalf("repeated correction = %+v, err %v; want no correction", repeated, err)
	}
	env.requireCorrectionEvents(t, []uuid.UUID{parentDefinition.ID, scopeDefinition.ID})
	stored, err := env.Stores.PropertyDefs.Get(integration.WithTestOperation(t, t.Context()), env.OrgID, parentDefinition.ID)
	if err != nil || stored.Search == nil || stored.Search.Include {
		t.Fatalf("stored parent_id definition = %+v, err %v; want a search exclusion", stored, err)
	}
	scope, err := env.Stores.PropertyDefs.Get(integration.WithTestOperation(t, t.Context()), env.OrgID, scopeDefinition.ID)
	if err != nil || slices.Contains(scope.AppliesToFeatures, node.FeatureHasSequenceID) {
		t.Fatalf("stored scope_id definition = %+v, err %v; want the seeded applicability", scope, err)
	}

	env.runIdle(t)
	if text := env.pageText(t, child); strings.Contains(text, unaffected[0].String()) || !strings.Contains(text, "zephyr orchard beacon") {
		t.Fatalf("child pages after the correction = %q, want its text without the parent UUID", text)
	}
	env.requireNewRevisions(t, []uuid.UUID{child}, childBefore, "the parent_id correction")
	for nodeID, pages := range before {
		after := env.pages(t, nodeID, false)
		if len(after) != len(pages) || after[0].NodeRevision != pages[0].NodeRevision || after[0].ProjectionVersion != pages[0].ProjectionVersion {
			t.Fatalf("unaffected node %s pages changed from %+v to %+v", nodeID, pages, after)
		}
	}
}

// requireCorrectionEvents checks the audit outbox for exactly one
// property_def.update event with a nonempty delta.Changed list for each
// definition in definitionIDs.
func (env *migrationEnv) requireCorrectionEvents(t *testing.T, definitionIDs []uuid.UUID) {
	t.Helper()
	entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(integration.WithTestOperation(t, t.Context()), nil, 100)
	if err != nil {
		t.Fatalf("read the audit outbox: %v", err)
	}
	found := make(map[uuid.UUID]int, len(definitionIDs))
	for _, entry := range entries {
		var event audit.Event
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			t.Fatalf("decode audit event: %v", err)
		}
		if event.Verb == string(audit.VerbPropertyDefUpdate) && event.Delta != nil && len(event.Delta.Changed) > 0 {
			found[event.Entity.ID]++
		}
	}
	for _, definitionID := range definitionIDs {
		if found[definitionID] != 1 {
			t.Fatalf("definition %s has %d correction events, want 1", definitionID, found[definitionID])
		}
	}
}
