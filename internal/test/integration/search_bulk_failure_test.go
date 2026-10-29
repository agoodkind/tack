package integration

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// releasedWorkWait bounds the wait for released work to become claimable
	// after its retry delay.
	releasedWorkWait = time.Minute
	// accessWriteFailedRecord identifies the record the adapter logs when an
	// access bulk write fails.
	accessWriteFailedRecord = "search.access.write_failed"
)

// bulkLogRecord stores the message and bulk group that the test decodes
// from one JSON log record.
type bulkLogRecord struct {
	Message string        `json:"msg"`
	Bulk    *bulkLogGroup `json:"bulk"`
}

// bulkLogGroup stores the bulk group of one log record.
// Its DurationMS pointer is nil when the record did not contain the
// duration_ms key.
type bulkLogGroup struct {
	DurationMS *int64 `json:"duration_ms"`
	Pages      int    `json:"pages"`
	Bytes      int    `json:"bytes"`
}

// requireBulkMeasured decodes the JSON records in logged and finds the
// first record with message in its msg field. It fails the test unless
// that record has a bulk group with a non-negative duration_ms value,
// at least minPages pages, and a positive byte size. It also fails the
// test if logged does not contain a matching record.
func requireBulkMeasured(t *testing.T, logged *bytes.Buffer, message string, minPages int) {
	t.Helper()
	decoder := json.NewDecoder(logged)
	for decoder.More() {
		var record bulkLogRecord
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("decode a worker log record: %v", err)
		}
		if record.Message != message {
			continue
		}
		bulk := record.Bulk
		if bulk == nil || bulk.DurationMS == nil || *bulk.DurationMS < 0 || bulk.Pages < minPages || bulk.Bytes <= 0 {
			t.Fatalf("record %s has bulk group %+v, want a duration, at least %d pages, and a positive byte size", message, bulk, minPages)
		}
		return
	}
	t.Fatalf("the worker did not log %s", message)
}

// rewriteSearchGeneration indexes page again at its own external version
// with search_generation stored as text or as a number. Stored text makes
// the guarded access script fail for that one bulk item.
func rewriteSearchGeneration(t *testing.T, client *opensearchapi.Client, index string, page searchPageSource, asText bool) {
	t.Helper()
	value := pageGeneration(t, page)
	generation := json.RawMessage(strconv.FormatInt(value, 10))
	if asText {
		generation = mustJSON(strconv.FormatInt(value, 10))
	}
	document := map[string]json.RawMessage{
		"node_id": mustJSON(page.NodeID), "node_type": mustJSON(page.NodeType), "name": mustJSON(page.Name),
		"node_revision": mustJSON(page.NodeRevision), "projection_version": mustJSON(page.ProjectionVersion),
		"page_ordinal": mustJSON(page.PageOrdinal), "page_text": mustJSON(*page.PageText), "retired": json.RawMessage("false"),
		"search_generation": generation, "access": mustJSON(page.Access),
	}
	requireBulkSucceeded(t, nativeBulk(t, client, nativeIndexAction(t, index, page.ID, int(value), encodeNativeJSON(t, document))))
}

// TestSearchPartialAccessFailureResumes requires the store to checkpoint only
// the accepted prefix when one item in the middle of an access-only batch
// fails. The retried work must resume after that prefix.
func TestSearchPartialAccessFailureResumes(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	moved := newMovableChild(t, stores, strings.Repeat("partial bulk page text ", 12))
	worker := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(accessPageBytes))
	runSearchWorkerUntilIdle(t, worker)
	before := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	if len(before) < 4 {
		t.Fatalf("child has %d pages, want at least four", len(before))
	}
	rewriteSearchGeneration(t, client, index, before[2], true)
	moveChild(t, stores, moved)

	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	failed := false
	for range 50 {
		claimed, err := worker.RunSlice(telemetry.WithLogger(WithTestOperation(t, t.Context()), logger))
		if err != nil {
			failed = true
			break
		}
		if !claimed {
			break
		}
	}
	if !failed {
		t.Fatal("the access batch with a failing item succeeded")
	}
	requireBulkMeasured(t, &logged, accessWriteFailedRecord, 3)
	// The check skips the pages after the failed item. OpenSearch applies
	// each bulk item separately, and those pages can receive the update in
	// the same request. The retry leaves them unchanged because their
	// generation already matches.
	partial := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	for position, page := range partial[:3] {
		updated := !slices.Equal(page.Access.Keys, before[position].Access.Keys)
		if updated != (position < 2) {
			t.Fatalf("page %d updated = %t, want the first two pages updated and the failed page unchanged", position, updated)
		}
	}
	store := stores.SearchWork(source)
	var pending searchdomain.Work
	released := waitFor(t, releasedWorkWait, func() bool {
		work, claimed, err := store.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassAccess, "inspector", shortLease)
		if err != nil {
			t.Fatalf("claim access work: %v", err)
		}
		if !claimed {
			return false
		}
		pending = work
		return work.NodeID == moved.Fixture.NodeID
	})
	if !released || pending.Phase != searchdomain.PhasePages || pending.Cursor == "" {
		t.Fatalf("pending access work = %+v, want the pages phase with the checkpoint after the accepted prefix", pending)
	}
	waitPastLeases(t, pending)
	rewriteSearchGeneration(t, client, index, before[2], false)
	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, searchWorkerSettings(accessPageBytes)))
	for position, page := range searchNodePages(t, client, index, moved.Fixture.NodeID, false) {
		if slices.Equal(page.Access.Keys, before[position].Access.Keys) {
			t.Fatalf("page %d kept its old access after the retry", position)
		}
	}
}

// TestSearchRejectsInvalidPageText requires validation to reject a page above
// 4,096 bytes and an active page without page_text before any indexing
// request.
func TestSearchRejectsInvalidPageText(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	fixture := putSearchText(t, stores, "valid text", readerExcludedValue)
	store := stores.SearchWork(clock.Wall{})
	work, claimed, err := store.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, "validator", time.Minute)
	for err == nil && claimed && work.NodeID != fixture.NodeID {
		work, claimed, err = store.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, "validator", time.Minute)
	}
	if err != nil || !claimed {
		t.Fatalf("claim live work = claimed %t, err %v", claimed, err)
	}
	page := readSearchPages(t, stores, fixture.NodeID, searchdomain.MaxPageBytes)[0]
	for name, text := range map[string]string{"oversized": strings.Repeat("a", searchdomain.MaxPageBytes+1), "missing": ""} {
		invalid := page
		invalid.Text = text
		if _, err := store.Register(WithTestOperation(t, t.Context()), work, invalid); err == nil {
			t.Fatalf("register accepted the %s page", name)
		}
		documentID := searchdomain.DocumentID(work.OrgID, work.NodeID, invalid.Revision, invalid.ProjectionVersion, invalid.Ordinal)
		intent := searchdomain.WriteIntent{Work: work, Page: invalid, DocumentID: documentID}
		if _, err := adapter.Put(WithTestOperation(t, t.Context()), intent); err == nil {
			t.Fatalf("put accepted the %s page", name)
		}
		response, _ := client.Document.Get(WithTestOperation(t, t.Context()), opensearchapi.GetReq{Index: index, ID: documentID})
		if response != nil && response.Found {
			t.Fatalf("OpenSearch stored the %s page", name)
		}
	}
}
