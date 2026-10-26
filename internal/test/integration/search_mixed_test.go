package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/test/integration"
)

type searchMixedOutput struct {
	Result struct {
		Completed int `json:"completed"`
		Writes    struct {
			Created int `json:"created"`
			Edited  int `json:"edited"`
			Deleted int `json:"deleted"`
		} `json:"writes"`
		FreshnessLagMs struct {
			P50 float64 `json:"p50"`
		} `json:"freshness_lag_ms"`
		MarkersNotFound   int `json:"markers_not_found"`
		DeletesUnverified int `json:"deletes_unverified"`
		Endpoints         int `json:"endpoints"`
		Tenants           int `json:"tenants"`
		Burst             struct {
			Created int `json:"created"`
			Failed  int `json:"failed"`
		} `json:"burst"`
		Sessions struct {
			Checks      int  `json:"checks"`
			Replays     int  `json:"replays"`
			Endpoints   int  `json:"endpoints"`
			Unavailable *int `json:"unavailable"`
		} `json:"sessions"`
		PendingDeletion struct {
			Checks          int `json:"checks"`
			PendingObserved int `json:"pending_observed"`
		} `json:"pending_deletion"`
		Revocation struct {
			Checks  int  `json:"checks"`
			Refused bool `json:"refused"`
		} `json:"revocation"`
		Rebuild struct {
			Requested   bool   `json:"requested"`
			TargetIndex string `json:"target_index"`
		} `json:"rebuild"`
		CatchUp struct {
			Completed bool `json:"completed"`
		} `json:"catch_up"`
		Samples []struct {
			OldestPendingWorkMs float64           `json:"oldest_pending_work_ms"`
			OpenSearchNodes     []json.RawMessage `json:"opensearch_nodes"`
			Error               string            `json:"error"`
		} `json:"samples"`
		StopReason    string                 `json:"stop_reason"`
		SkippedPhases []string               `json:"skipped_phases"`
		CorpusIndex   searchMixedCorpusIndex `json:"corpus_index"`
	} `json:"result"`
}

// TestSearchMixedWorkload runs the search-mixed command over HTTP against two runtime graphs
// in the test process.
func TestSearchMixedWorkload(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	endpoints := startSearchMixedProcesses(t, cfg)
	if output, err := runSearchMixedCommand(t, cfg, "--commit", "--endpoints", "ftp://a.example"); err == nil || len(output) != 0 {
		t.Fatalf("qa datagen search-mixed: the command must reject the ftp:// endpoint before writing output (error=%v; output=%s)", err, output)
	}
	seed := strconv.FormatInt(clock.Now().UnixNano(), 10)
	output, err := runSearchMixedCommand(t, cfg, "--seed", seed, "--rate", "60", "--write-rate", "60", "--duration", "40s",
		"--concurrency", "8", "--burst-writes", "60", "--catch-up", "5m", "--rebuild", "--endpoints", strings.Join(endpoints, ","), "--commit")
	var envelope searchMixedOutput
	if decodeErr := json.Unmarshal(output, &envelope); decodeErr != nil {
		t.Fatalf("qa datagen search-mixed: the test could not decode the result: %v (command error=%v; output=%s)", decodeErr, err, output)
	}
	result := envelope.Result
	deleteSearchMixedIndex(t, cfg, result.Rebuild.TargetIndex)
	if err != nil {
		t.Fatalf("qa datagen search-mixed: the workload failed: %v (output=%s)", err, output)
	}
	writes := result.Writes
	if result.Completed <= 0 || writes.Created <= 0 || writes.Edited <= 0 || writes.Deleted <= 0 || result.FreshnessLagMs.P50 <= 0 {
		t.Fatalf("qa datagen search-mixed: the result must include completed searches, creates, edits, deletes, and a positive median freshness lag: %s", output)
	}
	if result.MarkersNotFound != 0 || result.DeletesUnverified != 0 || result.Endpoints != 2 || result.Tenants != 2 {
		t.Fatalf("qa datagen search-mixed: the command must find every marker and verify every deletion using two endpoints and two tenants: %s", output)
	}
	if result.Burst.Created != 60 || result.Burst.Failed != 0 || result.Sessions.Endpoints != 2 || result.Sessions.Replays <= 0 ||
		result.Sessions.Unavailable == nil {
		t.Fatalf("qa datagen search-mixed: the burst must create 60 issues without failures. The sessions must include replays across two endpoints and report unavailable responses: %s", output)
	}
	if result.PendingDeletion.Checks != 1 || result.PendingDeletion.PendingObserved != 1 || result.Revocation.Checks <= 0 || !result.Revocation.Refused {
		t.Fatalf("qa datagen search-mixed: the result must confirm one deletion check with pending work and search refusal after membership removal: %s", output)
	}
	if !result.Rebuild.Requested || result.Rebuild.TargetIndex == "" || !result.CatchUp.Completed || result.StopReason != "none" ||
		len(result.SkippedPhases) != 0 {
		t.Fatalf("qa datagen search-mixed: the command must request a rebuild with a target index, complete catch-up, and finish every phase without a stop: %s", output)
	}
	requireSearchMixedSamples(t, output, envelope)
	requireSearchMixedCorpusIndex(t, output, result.CorpusIndex)
	requireSearchMixedReindexAudit(t, result.Rebuild.TargetIndex)
}

func requireSearchMixedSamples(t *testing.T, output []byte, envelope searchMixedOutput) {
	t.Helper()
	oldest := 0.0
	for _, sample := range envelope.Result.Samples {
		if sample.Error != "" || len(sample.OpenSearchNodes) == 0 {
			t.Fatalf("qa datagen search-mixed: every sample must report OpenSearch node statistics without an error: %s", output)
		}
		oldest = max(oldest, sample.OldestPendingWorkMs)
	}
	if len(envelope.Result.Samples) == 0 || oldest <= 0 {
		t.Fatalf("qa datagen search-mixed: at least one sample must report a positive age for pending search work: %s", output)
	}
}

// startSearchMixedProcesses serves MCP from two runtime graphs and runs the search workers in the
// first graph. It returns the two base URLs.
func startSearchMixedProcesses(t *testing.T, cfg *config.Config) []string {
	t.Helper()
	first, firstURL := serveSearchMixedProcess(t, cfg)
	first.StartSearchWorkers(integration.WithTestOperation(t, t.Context()))
	_, secondURL := serveSearchMixedProcess(t, cfg)
	return []string{firstURL, secondURL}
}

// serveSearchMixedProcess serves MCP from one runtime graph without search workers. It returns
// the graph and the base URL.
func serveSearchMixedProcess(t *testing.T, cfg *config.Config) (*appruntime.Graph, string) {
	t.Helper()
	graph, err := appruntime.BuildGraph(integration.WithTestOperation(t, t.Context()), cfg)
	if err != nil {
		t.Fatalf("qa datagen search-mixed: the test could not build a runtime graph: %v", err)
	}
	t.Cleanup(graph.Close)
	server := httptest.NewServer(twoProcessHandler(graph))
	t.Cleanup(server.Close)
	return graph, server.URL
}

func runSearchMixedCommand(t *testing.T, cfg *config.Config, flags ...string) ([]byte, error) {
	t.Helper()
	var output bytes.Buffer
	factory := cli.System(cfg)
	factory.Out = &output
	pool, err := pgxpool.New(integration.WithTestOperation(t, t.Context()), cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("qa datagen search-mixed: the test could not create the audit database connection pool: %v", err)
	}
	t.Cleanup(pool.Close)
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	root.SetArgs(append([]string{
		"--execute", "--output", "json", "--operator-id", searchMixedOperatorID, "--operator-email", "operator@example.com", "--operator-name", "Search Test", "ops", "qa", "datagen", "search-mixed",
	}, flags...))
	err = root.ExecuteContext(t.Context())
	return output.Bytes(), err
}
