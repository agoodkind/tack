package integration

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchVerifyChecksTheRecordedServingIndex runs a full FoundationDB
// replacement, which moves the public alias to a new physical index. The
// audited ops search verify command must then pass against that index. The
// test then points the alias at a third index. Verify must fail and report
// the alias target and the serving index that FoundationDB records.
func TestSearchVerifyChecksTheRecordedServingIndex(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	t.Setenv("OPENSEARCH_SHARDS", "1")
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", "24")
	t.Setenv("OPENSEARCH_REPLICAS", "0")
	workspace := fixture.Workspaces[0]
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	putOpaqueNode(t, fixture, kind, entryPoint(t, fixture, workspace), "verified node", "amber verify serving", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)

	request := searchdomain.BeginRebuild{Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Replicas: 0, Restored: false, Reason: "test"}
	rebuild := beginRebuild(t, fixture, request)
	runRebuildUntil(t, fixture, rebuildFinished)
	requireServing(t, fixture, rebuild.TargetIndex, fixture.Index)
	if output, err := runOpsSearchVerify(t, fixture.Config); err != nil {
		t.Fatalf("ops search verify after the replacement: %v\n%s", err, output)
	}

	model, err := fixture.Adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("provision search model: %v", err)
	}
	other := "node-pages-" + uuid.Must(uuid.NewV7()).String()
	t.Cleanup(func() { deleteNativeIndex(t, fixture.Client, other) })
	spec := search.IndexSpec{Model: model, MappingVersion: "1", Primaries: 1, RoutingShards: 24, Replicas: 0}
	if err := fixture.Adapter.EnsureIndex(t.Context(), other, spec); err != nil {
		t.Fatalf("create index %s: %v", other, err)
	}
	body := `{"actions":[{"remove":{"index":"` + rebuild.TargetIndex + `","alias":"` + search.PublicAlias + `"}},` +
		`{"add":{"index":"` + other + `","alias":"` + search.PublicAlias + `"}}]}`
	response, err := fixture.Client.Aliases(t.Context(), opensearchapi.AliasesReq{Body: strings.NewReader(body)})
	if err != nil || response.Inspect().Response.IsError() {
		t.Fatalf("point the public alias at %s: %v", other, err)
	}
	_, err = runOpsSearchVerify(t, fixture.Config)
	if err == nil || !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), rebuild.TargetIndex) {
		t.Fatalf("ops search verify with the alias on %s = %v, want an error that names %s and the serving index %s", other, err, other, rebuild.TargetIndex)
	}
}

// runOpsSearchVerify runs the audited ops search verify command through the
// rendered Cobra tree and returns its output and error.
func runOpsSearchVerify(t *testing.T, cfg *config.Config) (string, error) {
	t.Helper()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	pool, err := pgxpool.New(t.Context(), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("open audit ledger pool: %v", err)
	}
	defer pool.Close()
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	root := searchCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Search Test",
		"ops", "search", "verify",
	})
	runErr := root.Execute()
	return output.String(), runErr
}
