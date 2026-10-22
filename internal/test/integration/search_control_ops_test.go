package integration

import (
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/ops"
)

// TestSearchControlCommandsUsesTheAuditedProductionPath requires the audited
// verify command to pass after the audited provision command. Both commands
// run through the rendered Cobra tree. The fixture, adapter, TLS certificate,
// SQL outbox, and operator identity are real production dependencies. The
// test runs only when TACK_SEARCH_INTEGRATION is 1.
func TestSearchControlCommandsUsesTheAuditedProductionPath(t *testing.T) {
	control := newSearchControl(t)
	control.provisionFromEmpty(t)
	if err := control.run("ops", "search", "verify"); err != nil {
		t.Fatalf("verify after provision: %v", err)
	}
}

// TestSearchControlProvisionIsIdempotentFromEmpty requires a second provision
// run to keep the physical index that the first run created on an engine
// without a control index. Verify must pass after the second run.
func TestSearchControlProvisionIsIdempotentFromEmpty(t *testing.T) {
	control := newSearchControl(t)
	control.provisionFromEmpty(t)
	first := control.indexUUID(t, searchControlIndex)
	if err := control.run("ops", "search", "provision"); err != nil {
		t.Fatalf("repeated provision: %v", err)
	}
	if second := control.indexUUID(t, searchControlIndex); second != first {
		t.Fatalf("repeated provision replaced index %s: UUID %s became %s", searchControlIndex, first, second)
	}
	if err := control.run("ops", "search", "verify"); err != nil {
		t.Fatalf("verify after repeated provision: %v", err)
	}
}

// TestSearchControlCommandsRejectIndependentTopologyAndAliasMismatches
// requires the audited verify command to reject each topology setting
// mismatch and an alias target mismatch. The test changes one value at a
// time and restores it before the next change.
func TestSearchControlCommandsRejectIndependentTopologyAndAliasMismatches(t *testing.T) {
	control := newSearchControl(t)
	control.provisionFromEmpty(t)
	for _, mismatch := range []struct {
		name    string
		shards  string
		routing string
		replica string
		want    string
	}{
		{name: "primary shards", shards: "2", routing: "8", replica: "0", want: "2/8/0"},
		{name: "routing shards", shards: "1", routing: "16", replica: "0", want: "1/16/0"},
		{name: "replicas", shards: "1", routing: "8", replica: "1", want: "1/8/1"},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			t.Setenv("OPENSEARCH_SHARDS", mismatch.shards)
			t.Setenv("OPENSEARCH_ROUTING_SHARDS", mismatch.routing)
			t.Setenv("OPENSEARCH_REPLICAS", mismatch.replica)
			err := control.run("ops", "search", "verify")
			if err == nil || !strings.Contains(err.Error(), "topology 1/8/0 does not match "+mismatch.want) {
				t.Fatalf("topology mismatch error = %v", err)
			}
		})
	}
	model, err := control.adapter.Provision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	spec := search.IndexSpec{Model: model, MappingVersion: search.MappingVersion, Primaries: 1, RoutingShards: 8, Replicas: 0}
	if err := control.adapter.EnsureIndex(t.Context(), searchControlOtherIndex, spec); err != nil {
		t.Fatal(err)
	}
	body := `{"actions":[{"remove":{"index":"` + searchControlIndex + `","alias":"` + searchControlAlias + `"}},` +
		`{"add":{"index":"` + searchControlOtherIndex + `","alias":"` + searchControlAlias + `"}}]}`
	response, err := control.client.Aliases(t.Context(), opensearchapi.AliasesReq{Body: strings.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	if response.Inspect().Response.IsError() {
		t.Fatal(response.Inspect().Response.String())
	}
	err = control.run("ops", "search", "verify")
	if err == nil || !strings.Contains(err.Error(), `alias node-pages points to "other-index"`) {
		t.Fatalf("alias mismatch error = %v", err)
	}
}

func searchCommandRoot(factory *cli.Factory) *cobra.Command {
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	return root
}
