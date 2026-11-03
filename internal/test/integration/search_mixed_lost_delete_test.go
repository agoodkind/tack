package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

const (
	lostDeleteTool        = "tack_delete_issue"
	lostDeleteIssuePrefix = "Mixed load "
	lostDeleteCallLimit   = 2 * time.Minute
	lostDeleteStoreLimit  = 5 * time.Second
)

var errLostDeleteAnswer = errors.New("the test dropped the answer of a completed delete")

type lostDeleteCall struct {
	Params struct {
		Name      string `json:"name"`
		Arguments struct {
			NodeID string `json:"node_id"`
		} `json:"arguments"`
	} `json:"params"`
}

// lostDeleteTransport drops the HTTP answer of the first scheduled delete
// after the server responds.
type lostDeleteTransport struct {
	stores  *fdbadapter.Stores
	dropped atomic.Pointer[uuid.UUID]
}

func (l *lostDeleteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	selected, err := l.selectDelete(request)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil || selected == nil {
		return response, err
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return nil, err
	}
	if err := response.Body.Close(); err != nil {
		return nil, err
	}
	return nil, errLostDeleteAnswer
}

// selectDelete restores the request body. It returns the node ID when the request is the first
// delete of an issue that the write schedule created.
func (l *lostDeleteTransport) selectDelete(request *http.Request) (*uuid.UUID, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	var call lostDeleteCall
	if json.Unmarshal(body, &call) != nil || call.Params.Name != lostDeleteTool || l.dropped.Load() != nil {
		return nil, nil
	}
	nodeID, err := uuid.Parse(call.Params.Arguments.NodeID)
	if err != nil {
		return nil, err
	}
	view, err := l.stores.Views.Get(request.Context(), nodeID)
	if err != nil {
		return nil, err
	}
	if view == nil || !strings.HasPrefix(view.Name, lostDeleteIssuePrefix) {
		return nil, nil
	}
	if !l.dropped.CompareAndSwap(nil, &nodeID) {
		return nil, nil
	}
	return &nodeID, nil
}

// TestSearchMixedLostDeleteAnswer checks recovery after the test drops
// the HTTP answer of a completed scheduled delete.
func TestSearchMixedLostDeleteAnswer(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	endpoints := startSearchMixedProcesses(t, cfg)
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, lostDeleteStoreLimit, nil)
	if err != nil {
		t.Fatalf("open the FoundationDB stores: %v", err)
	}
	transport := &lostDeleteTransport{stores: stores}
	ctx := integration.WithTestOperation(t, t.Context())
	options := datagen.SearchMixedOptions{
		Seed: clock.Now().UnixNano(), Rate: 30, WriteRate: 120, Duration: time.Minute, Concurrency: 4,
		Commit: true, BurstWrites: 10, CatchUp: 0, Endpoints: endpoints,
		Client:  &http.Client{Timeout: lostDeleteCallLimit, Transport: transport},
		Rebuild: nil, MetadataWrites: 0, Moves: 1,
	}
	summary, err := datagen.RunSearchMixed(ctx, ctx, cfg, options)
	if err != nil {
		t.Fatalf("RunSearchMixed returned an error: %v (summary=%+v)", err, summary)
	}
	dropped := transport.dropped.Load()
	if dropped == nil {
		t.Fatalf("the run did not delete a scheduled issue: deletes=%d", summary.Deletes)
	}
	view, err := stores.Views.Get(ctx, *dropped)
	if err != nil || view != nil {
		t.Fatalf("issue %s must not exist after its delete: view=%+v err=%v", dropped, view, err)
	}
	if summary.WriteErrors != 1 {
		t.Fatalf("writes.failed = %d, want 1 for the dropped answer (edits=%d deletes=%d)", summary.WriteErrors, summary.Edits, summary.Deletes)
	}
	if err := summary.Verify(ctx, "none"); err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
}
