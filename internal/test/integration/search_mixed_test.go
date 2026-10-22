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

// TestSearchMixedReportsFreshness verifies public search results, issue creates, edits, deletes, positive freshness lag, and deletion verification through the command.
func TestSearchMixedReportsFreshness(t *testing.T) {
	cfg := newCohortEngines(t)
	workers, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("qa datagen search-mixed: build search workers: %v", err)
	}
	t.Cleanup(workers.Close)
	workers.StartSearchWorkers(t.Context())
	seed := strconv.FormatInt(clock.Now().UnixNano(), 10)
	output, err := runSearchMixedCommand(t, cfg, "--seed", seed, "--rate", "60", "--write-rate", "60", "--duration", "30s", "--concurrency", "8", "--commit")
	if err != nil {
		t.Fatalf("qa datagen search-mixed: run workload: %v; output=%s", err, output)
	}
	var envelope struct {
		Result struct {
			Completed int `json:"completed"`
			Writes    struct {
				Created int `json:"created"`
				Edited  int `json:"edited"`
				Deleted int `json:"deleted"`
				Failed  int `json:"failed"`
			} `json:"writes"`
			WritesPerMinute float64 `json:"writes_per_minute"`
			FreshnessLagMs  struct {
				P50 float64 `json:"p50"`
				Max float64 `json:"max"`
			} `json:"freshness_lag_ms"`
			MarkersNotFound      int `json:"markers_not_found"`
			DeletedNodesReturned int `json:"deleted_nodes_returned"`
			DeletesUnverified    int `json:"deletes_unverified"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("qa datagen search-mixed: decode result: %v; output=%s", err, output)
	}
	result := envelope.Result
	writes := result.Writes
	if result.Completed <= 0 || writes.Created <= 0 || writes.Edited <= 0 || writes.Deleted <= 0 {
		t.Fatalf("qa datagen search-mixed: expected completed searches and issue creates, edits, and deletes: %s", output)
	}
	if result.FreshnessLagMs.P50 <= 0 || result.MarkersNotFound != 0 || result.DeletedNodesReturned != 0 || result.DeletesUnverified != 0 {
		t.Fatalf("qa datagen search-mixed: expected positive freshness lag, all markers found, all deletions verified, and no deleted issues returned: %s", output)
	}
	t.Logf("search mixed completed=%d created=%d edited=%d deleted=%d failed=%d writes_per_minute=%.1f freshness_p50_ms=%.1f freshness_max_ms=%.1f",
		result.Completed, writes.Created, writes.Edited, writes.Deleted, writes.Failed, result.WritesPerMinute,
		result.FreshnessLagMs.P50, result.FreshnessLagMs.Max)
}

func runSearchMixedCommand(t *testing.T, cfg *config.Config, flags ...string) ([]byte, error) {
	t.Helper()
	var output bytes.Buffer
	factory := cli.System(cfg)
	factory.Out = &output
	pool, err := pgxpool.New(t.Context(), cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("qa datagen search-mixed: open audit database pool: %v", err)
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
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30", "--operator-email", "operator@example.com", "--operator-name", "Search Test", "ops", "qa", "datagen", "search-mixed",
	}, flags...))
	err = root.ExecuteContext(t.Context())
	return output.Bytes(), err
}
