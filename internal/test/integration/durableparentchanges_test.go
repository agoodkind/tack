package integration_test

import (
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
	_ uuid.UUID,
	projectID uuid.UUID,
	_ *node.PropertyDef,
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

			return len(changes) >= 1
		},
	)
	if len(changes) != 1 {
		t.Fatalf("parent migration recorded %d changes, want one: %+v", len(changes), changes)
	}

	row := changes[0]
	if row.ActorID != operatorID || row.Outcome != audit.OutcomeCompleted || row.Delta == nil ||
		row.Action != string(audit.VerbRelationshipAdd) {
		t.Fatalf("parent migration recorded incomplete operator evidence: %+v", row)
	}

	target := row.Context.Target
	if target == nil || target.Kind != audit.TargetKindRelationship ||
		target.SourceID != missingID || target.RelationType != node.RelChildOf ||
		target.TargetID != projectID || len(row.Delta.Before) != 0 {
		t.Fatalf("parent migration relationship evidence = %+v", row)
	}

	requireJSONEqual(
		t,
		"added parent relationship",
		row.Delta.After,
		storedEdge(t, env, missingID, node.RelChildOf, projectID),
	)

	return map[uuid.UUID]struct{}{row.EventID: {}}
}
