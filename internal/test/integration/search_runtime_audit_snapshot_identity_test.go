//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

func snapshotResults(rows []audit.Row, verb audit.Verb, outcome audit.Outcome) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		if row.Action == string(verb) && row.Context.Phase == audit.PhaseResult && row.Outcome == outcome {
			selected = append(selected, row)
		}
	}
	return selected
}

func requireInterruptedSnapshotRows(t *testing.T, fixture failureFixture, rows []audit.Row, verb audit.Verb) {
	t.Helper()
	results := snapshotResults(rows, verb, audit.OutcomeIncomplete)
	if len(results) != 2 {
		t.Fatalf("the interrupted point in time operation did not record both results: %s", describeRows(rows))
	}
	var operation, step *audit.Row
	for _, result := range results {
		if result.Error == nil || result.Error.Code != "point_in_time_uncertain" || result.Error.Message != "OpenSearch did not confirm the point in time result" {
			t.Fatalf("the interrupted result did not use the fixed uncertain failure fields: %+v", result)
		}
		requireSnapshotIdentity(t, fixture, rows, result)
		if result.Context.Target.Kind == audit.TargetKindResource {
			operation = &result
		} else if result.Context.Target.Kind == audit.TargetKindExternal {
			step = &result
		}
	}
	if operation == nil || step == nil || operation.EntityID == uuid.Nil ||
		operation.Context.Target.ID != operation.EntityID || operation.Context.Target.OwnerID != operation.ActorID ||
		step.Context.ParentOperationID != operation.Context.OperationID ||
		!strings.Contains(step.Context.Target.Reference, operation.EntityID.String()) {
		t.Fatalf("the interrupted results lost the operation and session relationship: %s", describeRows(results))
	}
	if failed := snapshotResults(rows, verb, audit.OutcomeFailed); len(failed) != 0 {
		t.Fatalf("the applied effect has a failed result: %s", describeRows(failed))
	}
}

// requireSnapshotIdentity compares the terminal record with its original
// intent in the permanent ledger, including actor, target, operation and time.
func requireSnapshotIdentity(t *testing.T, fixture failureFixture, rows []audit.Row, result audit.Row) {
	t.Helper()
	intents := []audit.Row{}
	for _, row := range rows {
		if row.Action == result.Action && row.Context.Phase == audit.PhaseIntent &&
			row.Context.OperationID == result.Context.OperationID {
			intents = append(intents, row)
		}
	}
	intent := onlyRow(t, intents, "the original point in time intent")
	if result.ActorID != fixture.Workspaces[0].Actors[0].UserID || result.ActorID != intent.ActorID ||
		result.Context.OperationID == uuid.Nil || result.Context.ParentOperationID != intent.Context.ParentOperationID ||
		result.Context.Target == nil || !bytes.Equal(encodeJSON(t, result.Context.Target), encodeJSON(t, intent.Context.Target)) ||
		result.Context.StartedAt.IsZero() || !result.Context.StartedAt.Equal(intent.Context.StartedAt) ||
		result.Context.CompletedAt.Before(result.Context.StartedAt) {
		t.Fatalf("the terminal record changed its original identity or time: intent=%+v result=%+v", intent, result)
	}
}
