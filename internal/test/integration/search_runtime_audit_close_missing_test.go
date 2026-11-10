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

const missingSnapshotResponse = `{"error":{"type":"resource_not_found_exception","reason":"point in time does not exist"},"status":404}`

// TestSearchRuntimeAuditMissingSnapshotDeleteRecordsAlreadyClosed checks that
// audit.events stores a completed delete step with already_closed true and
// the point in time ID when a proxy replaces the engine's delete response
// with HTTP 404 during a public continuation.
func TestSearchRuntimeAuditMissingSnapshotDeleteRecordsAlreadyClosed(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	page := integration.CallSearch(t, harness, failureAuditQuery, "")
	if page.Cursor == "" {
		t.Fatal("the missing-delete fixture did not open a continuing session")
	}
	session := integration.CursorSession(t, fixture.QueryFixture, page.Cursor)
	responses := make(chan snapshotResponse, 1)
	capture.engine.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method != http.MethodDelete || !strings.HasSuffix(response.Request.URL.Path, pointInTimePathSuffix) {
			return capture.record(response)
		}
		observed, err := replaceSnapshotErrorResponse(response, missingSnapshotResponse)
		if err != nil {
			return err
		}
		response.StatusCode = http.StatusNotFound
		responses <- observed
		return nil
	}
	result := callSnapshotSearch(t.Context(), fixture, harness, page.Cursor)
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("the MCP continuation returned HTTP %d with error %v: %s", result.status, result.err, result.text)
	}
	observed := waitSnapshotResponse(t, responses)
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(stepRows(current, session.ID, audit.VerbSearchPointInTimeDelete, "delete")) == 2
	})
	step := finishedStep(t, stepRows(rows, session.ID, audit.VerbSearchPointInTimeDelete, "delete"))
	var evidence struct {
		PITID         string `json:"pit_id"`
		AlreadyClosed bool   `json:"already_closed"`
	}
	if err := json.Unmarshal(step.Extra, &evidence); err != nil || !evidence.AlreadyClosed || evidence.PITID != observed.id || step.Outcome != audit.OutcomeCompleted {
		t.Fatalf("delete step = %s with outcome %s: %v, want a completed delete of %s with already_closed true", step.Extra, step.Outcome, err, observed.id)
	}
}
