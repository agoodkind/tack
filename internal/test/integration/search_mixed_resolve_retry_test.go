package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	resolveRetrySearchTool   = "tack_search"
	resolveRetryCreateTool   = "tack_create_issue"
	resolveRetryVictimPrefix = "victim"
	resolveRetryRecord       = "qa.datagen.search_mixed_read_retried"
	resolveRetryPauseMargin  = 2500 * time.Millisecond
)

type resolveRetryCall struct {
	Params struct {
		Name      string `json:"name"`
		Arguments struct {
			Name  string `json:"name"`
			Query string `json:"query"`
		} `json:"arguments"`
	} `json:"params"`
}

// resolveRetryTransport pauses FoundationDB after it reads the answer of the
// first victim word search that follows the first scheduled write. The
// generator reads the result nodes of that answer while FoundationDB is paused.
type resolveRetryTransport struct {
	container string
	pause     time.Duration
	armed     atomic.Bool
	paused    atomic.Bool
}

func (r *resolveRetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}

	request.Body = io.NopCloser(bytes.NewReader(body))
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		return response, err
	}

	var call resolveRetryCall
	if json.Unmarshal(body, &call) != nil {
		return response, nil
	}

	arguments := call.Params.Arguments
	if call.Params.Name == resolveRetryCreateTool &&
		strings.HasPrefix(arguments.Name, lostDeleteIssuePrefix) {
		r.armed.Store(true)
	}

	victimSearch := call.Params.Name == resolveRetrySearchTool &&
		strings.HasPrefix(arguments.Query, resolveRetryVictimPrefix)
	if !victimSearch || !r.armed.Load() || !r.paused.CompareAndSwap(false, true) {
		return response, nil
	}

	answer, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	if err := response.Body.Close(); err != nil {
		return nil, err
	}

	response.Body = io.NopCloser(bytes.NewReader(answer))
	ctx := context.WithoutCancel(request.Context())
	if err := changeContainerPause(ctx, r.container, true); err != nil {
		return nil, err
	}

	time.AfterFunc(
		r.pause,
		func() {
			if err := changeContainerPause(ctx, r.container, false); err != nil {
				slog.ErrorContext(
					ctx,
					"test.resolve_retry_resume_failed",
					slog.String("err", err.Error()),
				)
			}
		},
	)

	return response, nil
}

// TestSearchMixedResolveRetriesTimeout checks that search-mixed finishes its
// phases after a FoundationDB stall times out the read of one search result.
func TestSearchMixedResolveRetriesTimeout(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	endpoints := startSearchMixedProcesses(t, cfg)
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	t.Cleanup(func() { resumeContainerIfPaused(t, container) })
	transport := &resolveRetryTransport{
		container: container,
		pause:     cfg.FDBTransactionTimeout + resolveRetryPauseMargin,
	}
	ctx := integration.WithTestOperation(t, t.Context())
	options := datagen.SearchMixedOptions{
		Seed:           clock.Now().UnixNano(),
		Rate:           30,
		WriteRate:      60,
		Duration:       time.Minute,
		Concurrency:    4,
		Commit:         true,
		BurstWrites:    10,
		CatchUp:        0,
		Endpoints:      endpoints,
		Client:         &http.Client{Timeout: lostDeleteCallLimit, Transport: transport},
		Rebuild:        nil,
		MetadataWrites: 0,
		Moves:          0,
	}
	logged, restoreLog := captureSweepLog(t)
	summary, err := datagen.RunSearchMixed(ctx, ctx, cfg, options)
	restoreLog()
	if err != nil {
		t.Fatalf("RunSearchMixed returned an error: %v (summary=%+v)", err, summary)
	}

	if !transport.paused.Load() {
		t.Fatalf(
			"the run did not send a victim word search after its first scheduled write: %+v",
			summary,
		)
	}

	if !bytes.Contains(logged.contents(), []byte(resolveRetryRecord)) {
		t.Fatalf("the run did not log %s while FoundationDB was paused", resolveRetryRecord)
	}

	if err := summary.Verify(ctx, "none"); err != nil {
		t.Fatalf("Verify returned an error: %v", err)
	}
}
