package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// putStoredBuiltIn stores a built-in property definition for name with
// the supplied search inclusion and feature list.
func (env *migrationEnv) putStoredBuiltIn(
	t *testing.T,
	name string,
	include bool,
	features []string,
) *node.PropertyDef {
	t.Helper()
	definition := &node.PropertyDef{
		ID:                     node.SystemPropID(env.OrgID, name),
		OrgID:                  env.OrgID,
		Name:                   name,
		Type:                   node.PropertyTypeText,
		AppliesToFeatures:      features,
		Indexed:                true,
		Options:                nil,
		Required:               false,
		DefaultValue:           nil,
		DefaultReference:       nil,
		ReferenceTargetTypeKey: "",
		Search: &node.SearchProjection{
			Include: include,
			Order:   50,
			Rule:    node.TextRule{Mode: node.TextRuleScalar, Fields: nil, Items: nil, Labels: nil},
		},
	}
	if err := env.Stores.PropertyDefs.Set(
		integration.WithTestOperation(t, t.Context()),
		definition,
	); err != nil {
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

// requireDefinitionChanges first fails when the reported and expected entry
// counts differ. It then encodes each expected entry and its reported entry
// as JSON, matching entries by property definition ID. It fails when the
// entries differ and uses run to label the failure message.
func requireDefinitionChanges(
	t *testing.T,
	run string,
	changes []service.FixedDefinitionChange,
	want []service.FixedDefinitionChange,
) {
	t.Helper()
	if len(changes) != len(want) {
		t.Fatalf(
			"%s reported %d definition changes %+v, want %d",
			run,
			len(changes),
			changes,
			len(want),
		)
	}

	got := make(map[uuid.UUID]string, len(changes))
	for _, change := range changes {
		encoded, err := json.Marshal(change)
		if err != nil {
			t.Fatalf("encode the %s change for %s: %v", run, change.Name, err)
		}

		got[change.PropertyDefID] = string(encoded)
	}

	for _, expected := range want {
		encoded, err := json.Marshal(expected)
		if err != nil {
			t.Fatalf("encode the expected change for %s: %v", expected.Name, err)
		}

		if got[expected.PropertyDefID] != string(encoded) {
			t.Fatalf(
				"%s change for %s = %s, want %s",
				run,
				expected.Name,
				got[expected.PropertyDefID],
				encoded,
			)
		}
	}
}

// requireCorrectionEvents checks the audit outbox for exactly one
// property_def.update event with a nonempty delta.Changed list for each
// definition in definitionIDs.
func (env *migrationEnv) requireCorrectionEvents(t *testing.T, definitionIDs []uuid.UUID) {
	t.Helper()
	entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(
		integration.WithTestOperation(t, t.Context()),
		nil,
		100,
	)
	if err != nil {
		t.Fatalf("read the audit outbox: %v", err)
	}

	found := make(map[uuid.UUID]int, len(definitionIDs))

	for _, entry := range entries {
		var event audit.Event
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			t.Fatalf("decode audit event: %v", err)
		}

		if event.Verb == string(audit.VerbPropertyDefUpdate) && event.Delta != nil &&
			len(event.Delta.Changed) > 0 {
			found[event.Entity.ID]++
		}
	}

	for _, definitionID := range definitionIDs {
		if found[definitionID] != 1 {
			t.Fatalf(
				"definition %s has %d correction events, want 1",
				definitionID,
				found[definitionID],
			)
		}
	}
}
