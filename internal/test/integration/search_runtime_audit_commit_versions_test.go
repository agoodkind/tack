//go:build runtimeaudit

package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchRuntimeAuditSnapshotCommitVersions runs one tack_search through
// the real outbox, relay, and consumer. It reads the four ledger rows for one
// point in time create. The operation intent and step intent must share one
// FoundationDB transaction version. The step result and operation result must
// each use a separate transaction version. The shared intent version must
// precede the step result version, which must precede the operation result
// version. Each result must retain the actor, target, operation, and time of
// its intent.
func TestSearchRuntimeAuditSnapshotCommitVersions(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, _ := newCaptureHarness(t, fixture.QueryFixture, false)
	integration.CallSearch(t, harness, failureAuditQuery, "")
	rows := fixture.audited.waitForRows(
		t,
		fixture.Workspaces[0].OrgID,
		func(found []audit.Row) bool {
			results := snapshotResults(
				found,
				audit.VerbSearchPointInTimeCreate,
				audit.OutcomeCompleted,
			)
			return len(results) == 2 && snapshotResultsHaveIntents(found, results)
		},
	)
	results := snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeCompleted)
	operationResult, stepResult := splitSnapshotRows(t, results)
	operationIntent := onlyRow(t, snapshotIntents(rows, operationResult), "the operation intent")
	stepIntent := onlyRow(t, snapshotIntents(rows, stepResult), "the step intent")
	for _, result := range results {
		requireSnapshotIdentity(t, fixture, rows, result)
	}
	ordered := []string{
		operationIntent.Context.CommitVersion,
		stepIntent.Context.CommitVersion,
		stepResult.Context.CommitVersion,
		operationResult.Context.CommitVersion,
	}
	for position, version := range ordered {
		if len(version) != commitVersionLength {
			t.Fatalf(
				"commit versions %v include a version with the wrong length; want %d",
				ordered,
				commitVersionLength,
			)
		}
		if position > 0 && ordered[position-1] >= version {
			t.Fatalf("commit versions %v do not increase in ledger order", ordered)
		}
	}
	operationIntentVersion, stepIntentVersion := ordered[0][:transactionVersionLength], ordered[1][:transactionVersionLength]
	stepResultVersion, operationResultVersion := ordered[2][:transactionVersionLength], ordered[3][:transactionVersionLength]
	if operationIntentVersion != stepIntentVersion || stepIntentVersion == stepResultVersion ||
		stepResultVersion == operationResultVersion {
		t.Fatalf(
			"commit versions %v do not use one transaction for the intents and one transaction for each result",
			ordered,
		)
	}
}

func splitSnapshotRows(t *testing.T, rows []audit.Row) (audit.Row, audit.Row) {
	t.Helper()
	var operation, step []audit.Row
	for _, row := range rows {
		if row.Context.Target != nil && row.Context.Target.Kind == audit.TargetKindExternal {
			step = append(step, row)
			continue
		}
		operation = append(operation, row)
	}
	return onlyRow(t, operation, "the operation result"), onlyRow(t, step, "the step result")
}
