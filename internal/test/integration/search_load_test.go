package integration_test

import (
	"bytes"
	"encoding/json"
	"strconv"
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
)

// TestSearchLoadReportsMeasuredRate verifies successful requests with distinct query texts and positive median latency.
func TestSearchLoadReportsMeasuredRate(t *testing.T) {
	cfg := newCohortEngines(t)
	workers, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("qa datagen search-load: build search workers: %v", err)
	}
	t.Cleanup(workers.Close)
	workers.StartSearchWorkers(t.Context())
	seed := strconv.FormatInt(clock.Now().UnixNano(), 10)
	if output, err := runDatagenSearchCommand(t, cfg, "--verify-cohort", "--seed", seed, "--commit"); err != nil {
		t.Fatalf("qa datagen search-load: index search cohort: %v; output=%s", err, output)
	}
	output, err := runSearchLoadCommand(t, cfg, "--seed", seed, "--rate", "120", "--duration", "5s", "--concurrency", "8", "--commit")
	if err != nil {
		t.Fatalf("qa datagen search-load: run workload: %v; output=%s", err, output)
	}
	var envelope struct {
		Result struct {
			Sent               int     `json:"sent"`
			Completed          int     `json:"completed"`
			Dropped            int     `json:"dropped"`
			DistinctQueryTexts int     `json:"distinct_query_texts"`
			CompletedPerMinute float64 `json:"completed_per_minute"`
			Errors             struct {
				Unavailable    int `json:"unavailable"`
				ToolError      int `json:"tool_error"`
				TransportError int `json:"transport_error"`
			} `json:"errors"`
			LatencyMs struct {
				P50 float64 `json:"p50"`
			} `json:"latency_ms"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("qa datagen search-load: decode result: %v; output=%s", err, output)
	}
	result := envelope.Result
	errorCount := result.Errors.Unavailable + result.Errors.ToolError + result.Errors.TransportError
	if result.Completed <= 0 || errorCount != 0 || result.LatencyMs.P50 <= 0 || result.DistinctQueryTexts != result.Sent {
		t.Fatalf("qa datagen search-load: expected successful requests, distinct query texts, and positive median latency: %s", output)
	}
	t.Logf("search load sent=%d completed=%d dropped=%d completed_per_minute=%.1f p50_ms=%.1f",
		result.Sent, result.Completed, result.Dropped, result.CompletedPerMinute, result.LatencyMs.P50)
}

func runSearchLoadCommand(t *testing.T, cfg *config.Config, flags ...string) ([]byte, error) {
	t.Helper()
	var output bytes.Buffer
	factory := cli.System(cfg)
	factory.Out = &output
	pool, err := pgxpool.New(t.Context(), cfg.DatabaseURL)
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
