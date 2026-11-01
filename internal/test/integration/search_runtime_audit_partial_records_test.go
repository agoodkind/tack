//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
)

func requirePartialSnapshotRecords(t *testing.T, fixture failureFixture, rows []audit.Row, identifier string) {
	t.Helper()
	results := snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomePartiallyCompleted)
	if len(results) != 2 {
		t.Fatalf("the partial create did not record both terminal rows: %s", describeRows(rows))
	}
	var operation, step *audit.Row
	for _, row := range results {
		if row.Error == nil || row.Error.Code != "point_in_time_partial" || row.Error.Message != "OpenSearch created a partial point in time" || len(row.Context.CommittedSteps) != 1 ||
			!strings.HasPrefix(row.Context.CommittedSteps[0], "opensearch/point-in-time/") || !strings.HasSuffix(row.Context.CommittedSteps[0], "/create") {
			t.Fatalf("the partial create lost its committed effect: %+v", row)
		}
		requireSnapshotIdentity(t, fixture, rows, row)
		if row.Context.Target.Kind == audit.TargetKindExternal {
			step = &row
			var observed struct {
				PITID string `json:"pit_id"`
			}
			if err := json.Unmarshal(row.Extra, &observed); err != nil || observed.PITID != identifier || row.Context.CommittedSteps[0] != row.Context.Target.Reference {
				t.Fatalf("partial create evidence %s does not identify the cleaned PIT %s: %v", row.Extra, identifier, err)
			}
		} else if row.Context.Target.Kind == audit.TargetKindResource {
			operation = &row
		}
	}
	if operation == nil || step == nil || step.Context.ParentOperationID != operation.Context.OperationID ||
		operation.Context.CommittedSteps[0] != step.Context.Target.Reference || !strings.Contains(step.Context.Target.Reference, operation.EntityID.String()) {
		t.Fatalf("the partial results lost the enclosing operation and committed step relationship: %s", describeRows(results))
	}
	if failed := snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeFailed); len(failed) != 0 {
		t.Fatalf("the created partial PIT has a failed create result: %s", describeRows(failed))
	}
}
