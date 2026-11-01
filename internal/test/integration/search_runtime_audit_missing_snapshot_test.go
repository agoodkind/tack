//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchRuntimeAuditMissingSnapshotIdentity requires uncertainty without
// committed-step evidence when a partial response omits the real PIT ID.
func TestSearchRuntimeAuditMissingSnapshotIdentity(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	rewrite := capture.engine.Rewrite
	capture.engine.Rewrite = func(request *httputil.ProxyRequest) {
		rewrite(request)
		request.Out.Header.Del("Accept-Encoding")
	}
	responses := make(chan snapshotResponse, 1)
	capture.engine.ModifyResponse = func(response *http.Response) error {
		if response.Request.Method != http.MethodPost || !strings.HasSuffix(response.Request.URL.Path, pointInTimePathSuffix) {
			return capture.record(response)
		}
		observed, err := omitSnapshotIdentity(response)
		if err != nil {
			return err
		}
		responses <- observed
		return nil
	}
	if _, err := integration.TrySearch(harness, failureAuditQuery, ""); err == nil || !strings.Contains(err.Error(), unexpectedErrorText) {
		t.Fatalf("the unidentified partial snapshot returned %v, want the unexpected error", err)
	}
	observed := waitSnapshotResponse(t, responses)
	t.Cleanup(func() { deleteObservedSnapshot(t, fixture, observed.id) })
	requirePITOpen(t, fixture.QueryFixture, []string{observed.id})
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(rows []audit.Row) bool {
		return len(snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeIncomplete)) == 2
	})
	requireInterruptedSnapshotRows(t, fixture, rows, audit.VerbSearchPointInTimeCreate)
	for _, row := range snapshotResults(rows, audit.VerbSearchPointInTimeCreate, audit.OutcomeIncomplete) {
		if len(row.Context.CommittedSteps) != 0 {
			t.Fatalf("the unidentified partial result invented committed steps: %+v", row)
		}
		if row.Context.Target.Kind == audit.TargetKindExternal {
			if _, found := deltaMembers(t, row.Extra)["pit_id"]; found {
				t.Fatalf("the unidentified partial result recorded a PIT ID: %s", row.Extra)
			}
		}
	}
	if deleted := partialDeletes(rows); len(deleted) != 0 {
		t.Fatalf("the unidentified response caused an audited PIT delete: %s", describeRows(deleted))
	}
}

func omitSnapshotIdentity(response *http.Response) (snapshotResponse, error) {
	if err := failPointInTimeShards(response); err != nil {
		return snapshotResponse{}, err
	}
	observed, err := readSnapshotResponse(response)
	if err != nil {
		return snapshotResponse{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&fields); err != nil {
		return snapshotResponse{}, fmt.Errorf("decode the partial snapshot response: %w", err)
	}
	delete(fields, "pit_id")
	payload, err := json.Marshal(fields)
	if err != nil {
		return snapshotResponse{}, fmt.Errorf("encode the unidentified snapshot response: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", fmt.Sprint(len(payload)))
	return observed, nil
}
