package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
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
	"goodkind.io/tack/internal/test/integration"
)

// searchLoadMinimumSamples sets the minimum sample count to include a sample at the start and
// at the end of a run.
const searchLoadMinimumSamples = 2

type searchLoadOutput struct {
	Result struct {
		Sent               int     `json:"sent"`
		Completed          int     `json:"completed"`
		Dropped            int     `json:"dropped"`
		DistinctQueryTexts int     `json:"distinct_query_texts"`
		CompletedPerMinute float64 `json:"completed_per_minute"`
		Endpoints          int     `json:"endpoints"`
		Errors             struct {
			Unavailable    int `json:"unavailable"`
			ToolError      int `json:"tool_error"`
			TransportError int `json:"transport_error"`
		} `json:"errors"`
		LatencyMs struct {
			P50 float64 `json:"p50"`
		} `json:"latency_ms"`
		PointInTime struct {
			Peak     int `json:"peak"`
			Answered int `json:"answered_samples"`
			Samples  []struct {
				Index       string `json:"index"`
				Unavailable bool   `json:"unavailable"`
				Status      int    `json:"status"`
				Error       string `json:"error"`
			} `json:"samples"`
		} `json:"point_in_time"`
	} `json:"result"`
}

// TestSearchLoadReportsMeasuredRate starts two local Tack HTTP servers with httptest.NewServer
// over separate runtime graphs, runs search workers on the first graph, indexes the cohort
// corpus, and runs search-load with --endpoints set to both servers. It requires an endpoint
// count of 2, completed requests, zero errors in every class, positive median latency, and one
// distinct query text per sent request. It also requires at least two answered point-in-time
// samples for the start and end of the run and checks that every sample reports HTTP 200, an
// index that starts with node-pages-, and empty error text.
func TestSearchLoadReportsMeasuredRate(t *testing.T) {
	cfg := newCohortEngines(t)
	endpoints := startSearchMixedProcesses(t, cfg)
	seed := strconv.FormatInt(clock.Now().UnixNano(), 10)
	if output, err := runDatagenSearchCommand(t, cfg, "--verify-cohort", "--seed", seed, "--commit"); err != nil {
		t.Fatalf("qa datagen search-load: index search cohort: %v; output=%s", err, output)
	}
	output, err := runSearchLoadCommand(t, cfg, "--seed", seed, "--rate", "120", "--duration", "5s", "--concurrency", "8",
		"--endpoints", strings.Join(endpoints, ","), "--commit")
	if err != nil {
		t.Fatalf("qa datagen search-load: run workload: %v; output=%s", err, output)
	}
	var envelope searchLoadOutput
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("qa datagen search-load: decode result: %v; output=%s", err, output)
	}
	result := envelope.Result
	errorCount := result.Errors.Unavailable + result.Errors.ToolError + result.Errors.TransportError
	if result.Endpoints != len(endpoints) || result.Completed <= 0 || errorCount != 0 || result.LatencyMs.P50 <= 0 ||
		result.DistinctQueryTexts != result.Sent {
		t.Fatalf("qa datagen search-load: result did not show successful requests to both endpoints, distinct query texts, and positive median latency: %s", output)
	}
	samples := result.PointInTime.Samples
	if len(samples) < searchLoadMinimumSamples || result.PointInTime.Answered != len(samples) {
		t.Fatalf("qa datagen search-load: result did not contain answered point-in-time samples at the start and end of the run: %s", output)
	}
	for _, sample := range samples {
		if sample.Unavailable || sample.Status != http.StatusOK || !strings.HasPrefix(sample.Index, "node-pages-") || sample.Error != "" {
			t.Fatalf("qa datagen search-load: a point-in-time sample did not confirm a successful read of the serving index: %s", output)
		}
	}
	t.Logf("search load sent=%d completed=%d dropped=%d completed_per_minute=%.1f p50_ms=%.1f point_in_time_peak=%d",
		result.Sent, result.Completed, result.Dropped, result.CompletedPerMinute, result.LatencyMs.P50, result.PointInTime.Peak)
}

func runSearchLoadCommand(t *testing.T, cfg *config.Config, flags ...string) ([]byte, error) {
	t.Helper()
	var output bytes.Buffer
	factory := cli.System(cfg)
	factory.Out = &output
	pool, err := pgxpool.New(integration.WithTestOperation(t, t.Context()), cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("qa datagen search-load: open audit ledger connection: %v", err)
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
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30", "--operator-email", "operator@example.com", "--operator-name", "Search Test", "ops", "qa", "datagen", "search-load",
	}, flags...))
	err = root.ExecuteContext(t.Context())
	return output.Bytes(), err
}
