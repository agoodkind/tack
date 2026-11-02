//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchRuntimeAuditTruncatedSnapshotResult requires permanent incomplete
// operation and step records after a real delete response ends early while
// the public MCP caller's context remains live.
func TestSearchRuntimeAuditTruncatedSnapshotResult(t *testing.T) {
	requireUnconfirmedSnapshotResult(t, truncateSnapshotResponse)
}

func TestSearchRuntimeAuditBackendErrorSnapshotResult(t *testing.T) {
	requireUnconfirmedSnapshotResult(t, replaceSnapshotResponseWithError)
}

func TestSearchRuntimeAuditSecurityErrorSnapshotResult(t *testing.T) {
	requireUnconfirmedSnapshotResult(t, replaceSnapshotResponseWithSecurityError)
}

func requireUnconfirmedSnapshotResult(t *testing.T, replace func(*http.Response) (snapshotResponse, error)) {
	t.Helper()
	fixture := newFailureFixture(t)
	t.Setenv("OPENSEARCH_MAX_RETRIES", "0")
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	page := integration.CallSearch(t, harness, failureAuditQuery, "")
	if page.Cursor == "" {
		t.Fatal("the truncated-delete fixture did not open a continuing session")
	}
	responses := make(chan snapshotResponse, 1)
	capture.engine.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method != http.MethodDelete || !strings.HasSuffix(response.Request.URL.Path, pointInTimePathSuffix) {
			return capture.record(response)
		}
		observed, err := replace(response)
		if err != nil {
			return err
		}
		responses <- observed
		return nil
	}
	ctx := t.Context()
	result := callSnapshotSearch(ctx, fixture, harness, page.Cursor)
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("the MCP continuation returned HTTP %d with error %v: %s", result.status, result.err, result.text)
	}
	if ctx.Err() != nil {
		t.Fatalf("the unconfirmed response ended the caller's context: %v", ctx.Err())
	}
	observed := waitSnapshotResponse(t, responses)
	requirePITsClosed(t, fixture.QueryFixture, []string{observed.id})
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(rows []audit.Row) bool {
		results := snapshotResults(rows, audit.VerbSearchPointInTimeDelete, audit.OutcomeIncomplete)
		return len(results) == 2 && snapshotResultsHaveIntents(rows, results)
	})
	requireInterruptedSnapshotRows(t, fixture, rows, audit.VerbSearchPointInTimeDelete)
	for _, row := range snapshotResults(rows, audit.VerbSearchPointInTimeDelete, audit.OutcomeIncomplete) {
		if row.Context.Target.Kind != audit.TargetKindExternal {
			continue
		}
		var evidence struct {
			PITID string `json:"pit_id"`
		}
		if err := json.Unmarshal(row.Extra, &evidence); err != nil || evidence.PITID != observed.id {
			t.Fatalf("the permanent delete result does not identify the actual point in time %s: %s; error=%v", observed.id, row.Extra, err)
		}
	}
}
