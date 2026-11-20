package integration_test

import (
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

func requireParentMigrationAudit(
	t *testing.T,
	ledger *nodeAuditLedger,
	env *integration.TestEnv,
	operatorID uuid.UUID,
	missingID uuid.UUID,
	existingID uuid.UUID,
	projectID uuid.UUID,
	definition *node.PropertyDef,
) map[uuid.UUID]struct{} {
	t.Helper()
	var changes []audit.Row
	waitFor(
		t,
		"permanent parent migration change events",
		func() bool {
			page, err := ledger.reader.QueryPage(
				t.Context(),
				audit.QueryFilter{
					OrgID:     env.OrgID,
					ActorID:   operatorID,
					Oldest:    clock.Now().Add(-time.Hour),
					Latest:    clock.Now().Add(time.Hour),
					Action:    "",
					EntityID:  uuid.Nil,
					RequestID: "",
					TraceID:   "",
					Limit:     10,
				},
				"",
			)
			if err != nil {
				t.Fatalf("read parent migration audit for operator %s: %v", operatorID, err)
			}

			changes = nil

			for _, row := range page.Rows {
				if row.Context.Phase == audit.PhaseChange {
					changes = append(changes, row)
				}
			}

			return len(changes) >= 4
		},
	)
	if len(changes) != 4 {
		t.Fatalf(
			"parent migration recorded %d changes, want exactly four: %+v",
			len(changes),
			changes,
		)
	}

	counts := make(map[string]int)
	nodeUpdates := make(map[uuid.UUID]bool)
	eventIDs := make(map[uuid.UUID]struct{})

	for _, row := range changes {
		if row.ActorID != operatorID || row.Outcome != audit.OutcomeCompleted || row.Delta == nil {
			t.Fatalf("parent migration recorded incomplete operator evidence: %+v", row)
		}

		counts[row.Action]++
		eventIDs[row.EventID] = struct{}{}
		switch row.Action {
		case string(audit.VerbNodeUpdate):
			if row.EntityID != missingID && row.EntityID != existingID {
				t.Fatalf("parent migration updated unexpected node %s", row.EntityID)
			}

			old := decodeAuditedIssueFields(t, row.Delta.Before)
			updated := decodeAuditedIssueFields(t, row.Delta.After)
			if len(old.Props["parent_id"]) == 0 || len(updated.Props["parent_id"]) != 0 {
				t.Fatalf(
					"parent migration delta for %s did not record property removal: %+v",
					row.EntityID,
					row.Delta,
				)
			}

			requireJSONEqual(
				t,
				"migrated node record",
				row.Delta.After,
				storedNodeRecord(t, env, row.EntityID),
			)

			nodeUpdates[row.EntityID] = true
		case string(audit.VerbRelationshipAdd):
			target := row.Context.Target
			if target == nil || target.Kind != audit.TargetKindRelationship ||
				target.SourceID != missingID ||
				target.RelationType != node.RelChildOf ||
				target.TargetID != projectID ||
				len(row.Delta.Before) != 0 {
				t.Fatalf("parent migration relationship evidence = %+v", row)
			}

			requireJSONEqual(
				t,
				"migrated parent relationship",
				row.Delta.After,
				storedEdge(t, env, missingID, node.RelChildOf, projectID),
			)
		case string(audit.VerbPropertyDefDelete):
			if row.EntityID != definition.ID || len(row.Delta.After) != 0 {
				t.Fatalf("parent migration definition deletion evidence = %+v", row)
			}

			oldDefinition, err := json.Marshal(definition)
			if err != nil {
				t.Fatalf("encode legacy parent definition: %v", err)
			}

			requireJSONEqual(t, "deleted parent definition", row.Delta.Before, oldDefinition)
		}
	}

	want := map[string]int{
		string(audit.VerbNodeUpdate):        2,
		string(audit.VerbRelationshipAdd):   1,
		string(audit.VerbPropertyDefDelete): 1,
	}
	if !maps.Equal(counts, want) || len(nodeUpdates) != 2 || len(eventIDs) != 4 {
		t.Fatalf(
			"parent migration change events = %v for nodes %v with %d event IDs, want %v",
			counts,
			nodeUpdates,
			len(eventIDs),
			want,
		)
	}

	return eventIDs
}
