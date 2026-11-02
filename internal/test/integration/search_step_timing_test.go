package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"goodkind.io/tack/internal/test/integration"
)

const (
	stepTimingQuery      = "vermilion quay step timing"
	stepTimingNodes      = 3
	stepAdmissionMessage = "search.admission.granted"
	stepSnapshotMessage  = "search.snapshot.timing"
	stepBatchMessage     = "search.query.batch_read"
)

type stepTimingRecord struct {
	Level       string `json:"level"`
	Message     string `json:"msg"`
	Wait        *int64 `json:"wait_ms"`
	Duration    *int64 `json:"duration_ms"`
	Alias       *int64 `json:"alias_ms"`
	Mapping     *int64 `json:"mapping_ms"`
	Model       *int64 `json:"model_ms"`
	Predict     *int64 `json:"predict_ms"`
	PointInTime *int64 `json:"point_in_time_ms"`
	Total       *int64 `json:"total_ms"`
}

// TestSearchStepTimingLogged checks search timing records through tack_search.
func TestSearchStepTimingLogged(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	for number := range stepTimingNodes {
		integration.PutOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Step timing node %d", number), stepTimingQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, 10*stepTimingNodes+100)

	logged, restoreLog := captureSweepLog(t)
	page := integration.CallSearch(t, fixture.Harness, stepTimingQuery, "")
	restoreLog()
	if len(page.IDs) == 0 {
		t.Fatalf("search %q returned no results", stepTimingQuery)
	}

	counts := map[string]int{}
	for line := range bytes.SplitSeq(bytes.TrimSpace(logged.contents()), []byte("\n")) {
		var record stepTimingRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode log record %s: %v", line, err)
		}
		if requireStepRecord(t, record) {
			counts[record.Message]++
			if bytes.Contains(line, []byte(stepTimingQuery)) {
				t.Errorf("record %s contains the query text: %s", record.Message, line)
			}
		}
	}
	for _, message := range []string{stepAdmissionMessage, stepSnapshotMessage, stepBatchMessage} {
		if counts[message] == 0 {
			t.Errorf("the search did not log %s", message)
		}
	}
}

// requireStepRecord reports whether record is a step timing record and requires its fields.
func requireStepRecord(t *testing.T, record stepTimingRecord) bool {
	t.Helper()
	var values []*int64
	switch record.Message {
	case stepAdmissionMessage:
		values = []*int64{record.Wait}
	case stepBatchMessage:
		values = []*int64{record.Duration}
	case stepSnapshotMessage:
		values = []*int64{record.Alias, record.Mapping, record.Model, record.Predict, record.PointInTime, record.Total}
	default:
		return false
	}
	if record.Level != slog.LevelDebug.String() {
		t.Errorf("record %s has level %s, want %s", record.Message, record.Level, slog.LevelDebug)
	}
	for _, value := range values {
		if value == nil || *value < 0 {
			t.Errorf("record %s has a missing or negative duration: %+v", record.Message, record)
		}
	}
	return true
}
