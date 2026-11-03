//go:build runtimeaudit

package integration_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchRuntimeAuditInterruptedSnapshotWithRetries verifies that an interrupted
// point in time create with the production retry setting produces one create
// at the engine and an uncertain audit result.
func TestSearchRuntimeAuditInterruptedSnapshotWithRetries(t *testing.T) {
	fixture := newFailureFixture(t)
	t.Setenv("OPENSEARCH_MAX_RETRIES", "2")
	t.Setenv("OPENSEARCH_REQUEST_TIMEOUT", "5s")
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	before := listOpenPITs(t, fixture)
	t.Cleanup(func() {
		for _, identifier := range addedPITs(listOpenPITs(t, fixture), before) {
			deleteObservedSnapshot(t, fixture, identifier)
		}
	})
	response, release := holdSnapshotResponse(t, capture, http.MethodPost)
	creates := countSnapshotCreates(capture)
	done := make(chan snapshotSearchResult, 1)
	go func() {
		done <- callSnapshotSearch(t.Context(), fixture, harness, "")
	}()
	observed := waitSnapshotResponse(t, response)
	requirePITOpen(t, fixture.QueryFixture, []string{observed.id})
	select {
	case result := <-done:
		t.Logf("interrupted search: status=%d error=%v result=%q", result.status, result.err, result.text)
	case <-time.After(sessionTimeoutHoldLimit):
		t.Fatal("the interrupted search did not finish")
	}
	if sent := creates.Load(); sent != 1 {
		t.Fatalf("the engine applied %d point in time creates, want 1", sent)
	}
	if added := addedPITs(listOpenPITs(t, fixture), before); !slices.Equal(added, []string{observed.id}) {
		t.Fatalf("the search added points in time %v, want only %s", added, observed.id)
	}
	release()
	verb := audit.VerbSearchPointInTimeCreate
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(rows []audit.Row) bool {
		results := snapshotResults(rows, verb, audit.OutcomeIncomplete)
		return len(results) == 2 && snapshotResultsHaveIntents(rows, results)
	})
	requireInterruptedSnapshotRows(t, fixture, rows, verb)
	if completed := snapshotResults(rows, verb, audit.OutcomeCompleted); len(completed) != 0 {
		t.Fatalf("the uncertain create has a completed result: %s", describeRows(completed))
	}
}

func countSnapshotCreates(capture *searchCapture) *atomic.Int32 {
	var creates atomic.Int32
	hold := capture.engine.ModifyResponse
	capture.engine.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method == http.MethodPost && strings.HasSuffix(response.Request.URL.Path, pointInTimePathSuffix) {
			creates.Add(1)
		}
		return hold(response)
	}
	return &creates
}

func listOpenPITs(t *testing.T, fixture failureFixture) []string {
	t.Helper()
	ctx := integration.WithTestOperation(t, context.WithoutCancel(t.Context()))
	listed, err := fixture.Client.PointInTime.GetAll(ctx, nil)
	if err != nil {
		t.Fatalf("list the open points in time: %v", err)
	}
	identifiers := make([]string, 0, len(listed.PITs))
	for _, pit := range listed.PITs {
		identifiers = append(identifiers, pit.PITID.String())
	}
	return identifiers
}

func addedPITs(current, before []string) []string {
	added := []string{}
	for _, identifier := range current {
		if !slices.Contains(before, identifier) {
			added = append(added, identifier)
		}
	}
	slices.Sort(added)
	return added
}
