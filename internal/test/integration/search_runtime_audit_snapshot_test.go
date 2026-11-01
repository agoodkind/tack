//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchRuntimeAuditPartialPointInTimeDeleted makes the engine response of
// a point in time create report one failed shard. The search must return the
// unexpected error and delete the partial point in time under an audited step.
func TestSearchRuntimeAuditPartialPointInTimeDeleted(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	rewrite := capture.engine.Rewrite
	capture.engine.Rewrite = func(outbound *httputil.ProxyRequest) {
		rewrite(outbound)
		outbound.Out.Header.Del("Accept-Encoding")
	}
	capture.engine.ModifyResponse = failPointInTimeShards

	if _, err := integration.TrySearch(harness, failureAuditQuery, ""); err == nil || !strings.Contains(err.Error(), unexpectedErrorText) {
		t.Fatalf("tack_search with a partial point in time returned %v, want an error containing %q", err, unexpectedErrorText)
	}
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(partialDeletes(current)) == 1 &&
			len(snapshotResults(current, audit.VerbSearchPointInTimeCreate, audit.OutcomePartiallyCompleted)) == 2
	})
	step := onlyRow(t, partialDeletes(rows), "the partial point in time delete")
	var observed struct {
		PITID         string `json:"pit_id"`
		AlreadyClosed bool   `json:"already_closed"`
	}
	if err := json.Unmarshal(step.Extra, &observed); err != nil || observed.PITID == "" || observed.AlreadyClosed || step.Outcome != audit.OutcomeCompleted {
		t.Fatalf("delete step = %s with outcome %s: %v, want a completed delete of an open point in time", step.Extra, step.Outcome, err)
	}
	requireDeleteSent(t, capture, []string{observed.PITID})
	requirePITsClosed(t, fixture.QueryFixture, []string{observed.PITID})
	requirePartialSnapshotRecords(t, fixture, rows, observed.PITID)
}

// failPointInTimeShards sets the failed shard count of a point in time create
// response to one.
func failPointInTimeShards(response *http.Response) error {
	if response.Request.Method != http.MethodPost || !strings.HasSuffix(response.Request.URL.Path, pointInTimePathSuffix) {
		return nil
	}
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return fmt.Errorf("read the point in time response: %w", err)
	}
	var created, shards map[string]json.RawMessage
	if err := json.Unmarshal(payload, &created); err != nil {
		return fmt.Errorf("decode the point in time response: %w", err)
	}
	if err := json.Unmarshal(created["_shards"], &shards); err != nil {
		return fmt.Errorf("decode the point in time shards: %w", err)
	}
	shards["failed"] = json.RawMessage("1")
	if created["_shards"], err = json.Marshal(shards); err != nil {
		return fmt.Errorf("encode the point in time shards: %w", err)
	}
	if payload, err = json.Marshal(created); err != nil {
		return fmt.Errorf("encode the point in time response: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	return nil
}

func partialDeletes(rows []audit.Row) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		target := row.Context.Target
		if row.Action == string(audit.VerbSearchPointInTimeDelete) && row.Context.Phase == audit.PhaseResult &&
			target != nil && target.Kind == audit.TargetKindExternal {
			selected = append(selected, row)
		}
	}
	return selected
}

// TestSearchRuntimeAuditIntentFailureRefusesSnapshot searches without an audit
// operation. The intent of the point in time create cannot save. The search
// must return the engine unavailable error and must not open a point in time.
func TestSearchRuntimeAuditIntentFailureRefusesSnapshot(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	searcher := integration.QueryService(t, fixture, failureAuditResponseBytes, failureAuditIdle, failureAuditAbsolute)
	request := integration.ServiceRequest(t, fixture, failureAuditQuery)
	before := openPITs(t, fixture)
	_, err := searcher.Search(t.Context(), request)
	if !errors.Is(err, searchdomain.ErrEngineUnavailable) || !errors.Is(err, audit.ErrMissingMutationContext) {
		t.Fatalf("Search without an operation = %v, want the engine unavailable error and the missing operation error", err)
	}
	for id := range openPITs(t, fixture) {
		if !before[id] {
			t.Fatalf("the refused search opened point in time %s", id)
		}
	}
}

// TestSearchRuntimeAuditCorruptHeaderSweepRecorded replaces the header of an
// expired session with invalid JSON. The sweep of the session must fail its
// Load and record one session_delete_failed event in the system organization
// without an owner.
func TestSearchRuntimeAuditCorruptHeaderSweepRecorded(t *testing.T) {
	fixture := newFailureFixture(t)
	searcher := integration.QueryService(t, fixture.QueryFixture, failureAuditResponseBytes, failureAuditIdle, failureAuditAbsolute)
	request := integration.ServiceRequest(t, fixture.QueryFixture, failureAuditQuery)
	expired := fixture.openExpired(t, searcher, request)
	integration.ReplaceRawValue(t, sessionHeaderKey(t, fixture, expired.ID), []byte("{"))
	if _, err := searcher.Search(integration.WithTestOperation(t, t.Context()), request); err != nil {
		t.Fatalf("the search that sweeps session %s failed: %v", expired.ID, err)
	}
	rows := fixture.audited.waitForRows(t, audit.SystemOrgID(), func(current []audit.Row) bool {
		return len(cleanupFailures(current, expired.ID, searchdomain.CleanupDelete)) == 1
	})
	failure := onlyRow(t, cleanupFailures(rows, expired.ID, searchdomain.CleanupDelete), "the Load failure")
	if failure.Outcome != audit.OutcomeFailed || failure.Context.Target.OwnerID != uuid.Nil || failure.Action != string(audit.VerbSearchSessionDelete) {
		t.Fatalf("Load failure row = %+v, target %+v, want a failed delete without an owner", failure, failure.Context.Target)
	}
}

func sessionHeaderKey(t *testing.T, fixture failureFixture, sessionID uuid.UUID) fdb.Key {
	t.Helper()
	keys := readStateKeys(t, parentEdgeFixture{cfg: fixture.Config})
	for _, key := range stateKeysOf(keys, sessionHeaderFamily) {
		if key.text(t, sessionKeyLayouts[sessionHeaderFamily].session) == sessionID.String() {
			packed := tuple.Tuple(key.elements).Pack()
			return fdb.Key(append(bytes.Clone(fdbadapter.TestPrefixRange()), packed...))
		}
	}
	t.Fatalf("session %s does not have a header key", sessionID)
	return nil
}
