//go:build runtimeaudit

package integration_test

import (
	"context"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
)

// TestSearchRuntimeAuditCanceledBeforeSnapshot keeps the prediction request
// open before the adapter can create a point in time. Cancellation must
// preserve a failed result for this known failure before the effect.
func TestSearchRuntimeAuditCanceledBeforeSnapshot(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, proxy := newPredictHoldHarness(t, fixture.QueryFixture)
	proxy.arm()
	before := openPITs(t, fixture.QueryFixture)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan snapshotSearchResult, 1)
	go func() {
		done <- callSnapshotSearch(ctx, fixture, harness, "")
	}()
	waitForSignal(t, proxy.held, "prediction before point in time creation")
	cancel()
	select {
	case result := <-done:
		t.Logf("search canceled before its effect: status=%d error=%v result=%q", result.status, result.err, result.text)
	case <-time.After(sessionTimeoutHoldLimit):
		t.Fatal("the search did not finish after cancellation before its effect")
	}
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(rows []audit.Row) bool {
		results := snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeFailed)
		return len(results) == 2 && snapshotResultsHaveIntents(rows, results)
	})
	for _, result := range snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeFailed) {
		if result.Error == nil || result.Error.Code != "point_in_time_failed" {
			t.Fatalf("the known failure before the effect changed its failure code: %+v", result)
		}
		requireSnapshotIdentity(t, fixture, rows, result)
	}
	for identifier := range openPITs(t, fixture.QueryFixture) {
		if !before[identifier] {
			t.Fatalf("the canceled prediction created point in time %s", identifier)
		}
	}
}
