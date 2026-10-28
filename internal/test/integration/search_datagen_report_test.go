package integration_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// relevanceProbeQueries lists the queries that the relevance probe must report in order.
var relevanceProbeQueries = []string{"restart containers one by one", "rolling Docker restart", "restart containers sequentially"}

// TestSearchDatagenReportsWarningAndRelevance runs ops qa datagen search --commit, ops
// search verify, and an ops verify strict-validation dry run through Cobra with real
// FoundationDB, YugabyteDB, and OpenSearch. It checks the reported warning and the
// relevance probe result fields. It then requires ops search verify to omit the warning
// of the deleted warning node and strict validation to report 0 invalid records.
func TestSearchDatagenReportsWarningAndRelevance(t *testing.T) {
	cfg := newCohortEngines(t)
	output, err := runDatagenSearchCommand(t, cfg, "--commit")
	if err != nil {
		t.Fatalf("execute ops qa datagen search: %v; output=%s", err, output)
	}
	var envelope struct {
		Result struct {
			Verified bool                        `json:"verified"`
			Search   *datagen.SearchVerification `json:"search"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode ops qa datagen search result: %v; output=%s", err, output)
	}
	if !envelope.Result.Verified || envelope.Result.Search == nil {
		t.Fatalf("ops qa datagen search did not report verified=true with a search report: %s", output)
	}
	warning := envelope.Result.Search.PropertyWarning
	if warning.NodeID == uuid.Nil || warning.PropertyName == "" || warning.PropertyDefID == uuid.Nil ||
		warning.Reason != string(node.DiagnosticInapplicable) || warning.Revision == "" {
		t.Fatalf("property warning case does not include every required field or reports a reason other than inapplicable_property: %+v", warning)
	}
	if verifyListsWarning(t, cfg, warning) {
		t.Fatalf("ops search verify still lists the property warning of the deleted warning node: %+v", warning)
	}
	strict, err := runStrictValidation(t, false)
	if err != nil || strict.Result.ViolationCount != 0 {
		t.Fatalf("ops verify strict-validation dry run after ops qa datagen search --commit = %+v, %v; want 0 invalid records", strict.Result, err)
	}
	relevance := envelope.Result.Search.Relevance
	queries := make([]string, 0, len(relevance.Queries))
	for _, query := range relevance.Queries {
		queries = append(queries, query.Query)
	}
	if relevance.TargetID == uuid.Nil || relevance.Distractors == 0 || relevance.TitleRank < 1 || !slices.Equal(queries, relevanceProbeQueries) {
		t.Fatalf("relevance probe must report a target ID, distractors, a title rank of at least 1, and all three queries in order: %+v", relevance)
	}
	for _, query := range relevance.Queries {
		if query.Returned < 1 || query.Rank < 0 || query.Rank > query.Returned {
			t.Fatalf("relevance query reports fewer than one returned node or a rank outside 0 through the returned count: %+v", query)
		}
		t.Logf("relevance probe query=%q rank=%d returned=%d title_rank=%d", query.Query, query.Rank, query.Returned, relevance.TitleRank)
	}
}

// verifyListsWarning runs ops search verify and checks its warning list for the same
// node, property, definition, and reason. It fails the test on command or decoding errors.
func verifyListsWarning(t *testing.T, cfg *config.Config, want datagen.SearchWarningCase) bool {
	t.Helper()
	output, err := runAuditedCommand(t, cfg, "ops", "search", "verify")
	if err != nil {
		t.Fatalf("execute ops search verify: %v; output=%s", err, output)
	}
	var envelope struct {
		Result struct {
			Warnings []struct {
				NodeID        string `json:"node_id"`
				PropertyName  string `json:"property_name"`
				PropertyDefID string `json:"property_def_id"`
				Reason        string `json:"reason"`
			} `json:"warnings"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode ops search verify result: %v; output=%s", err, output)
	}
	for _, listed := range envelope.Result.Warnings {
		if listed.NodeID == want.NodeID.String() && listed.PropertyName == want.PropertyName &&
			listed.PropertyDefID == want.PropertyDefID.String() && listed.Reason == want.Reason {
			return true
		}
	}
	return false
}

// runAuditedCommand runs an ops command through Cobra with --execute and JSON output.
// It uses a test operator identity and an audit outbox on the test ledger, then returns
// the command output and error.
func runAuditedCommand(t *testing.T, cfg *config.Config, command ...string) ([]byte, error) {
	t.Helper()
	var output bytes.Buffer
	factory := cli.System(cfg)
	factory.Out = &output
	pool, err := pgxpool.New(integration.WithTestOperation(t, t.Context()), cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, rendered := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(rendered)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	root.SetArgs(append([]string{
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30", "--operator-email", "operator@example.com", "--operator-name", "Search Test",
	}, command...))
	err = root.ExecuteContext(t.Context())
	return output.Bytes(), err
}
