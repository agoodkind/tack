package integration_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// soakDiscoveryProject is the identifier of the first project that the
	// small seed creates.
	soakDiscoveryProject = "Q0101"
	// soakDiscoveryNamePrefix starts the Name field of each list item.
	soakDiscoveryNamePrefix = "  - Name: "
)

// TestDatagenSoakDiscoveryKeepsExistingNames seeds a small corpus and then
// runs one soak operation with the same seed. Soak discovery resolves every
// listed label, state, cycle, and module before that operation, and the first
// operation sets an issue state. The label names of the workspace and the
// state, cycle, and module names of the first project must be the same before
// and after the soak, with the same number of nodes for each name.
func TestDatagenSoakDiscoveryKeepsExistingNames(t *testing.T) {
	cfg := soakDiscoveryConfig(t)
	seed := clock.Now().UnixNano()
	ctx := integration.WithTestOperation(t, t.Context())
	seedOptions := datagen.SeedRunOptions{Scale: "small", Seed: seed, Commit: true, RedactAuditPII: false}
	if _, err := datagen.RunSeed(ctx, cfg, seedOptions); err != nil {
		t.Fatalf("seed the corpus: %v", err)
	}
	before := soakDiscoveryNames(t, cfg, seed)
	for toolName, names := range before {
		if len(names) == 0 {
			t.Fatalf("%s lists no seeded node", toolName)
		}
	}

	soakOptions := datagen.SoakOptions{
		Duration: time.Minute, Rate: 60, MaxOps: 1, Scale: "small", Seed: seed, Commit: true,
	}
	result, err := datagen.RunSoak(ctx, ctx, cfg, soakOptions, nil)
	if err != nil {
		t.Fatalf("run the soak: %v", err)
	}
	if result.Operations != 1 || result.StopReason != "max_ops" || result.Created != 0 {
		t.Fatalf("soak = %+v, want one operation that creates no node", result)
	}

	after := soakDiscoveryNames(t, cfg, seed)
	for toolName, names := range before {
		if !slices.Equal(after[toolName], names) {
			t.Fatalf("%s names after soak discovery = %q, want %q", toolName, after[toolName], names)
		}
	}
}

func soakDiscoveryConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", testenv.Ledger(t))
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.AuditKafkaBrokers = ""
	cfg.AuditWriterDSN = cfg.DatabaseURL
	cfg.AuditAllowUnrecorded = false
	cfg.AuditReadFlushInterval = 10 * time.Millisecond
	return cfg
}

// soakDiscoveryNames lists the label names of the first workspace and the
// state, cycle, and module names of its first project through the MCP list
// tools, keyed by list tool. Each list is sorted and keeps one entry per node.
func soakDiscoveryNames(t *testing.T, cfg *config.Config, seed int64) map[string][]string {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(ctx, cfg, seed, scale)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	graph, err := appruntime.BuildGraph(ctx, cfg)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	defer graph.Close()
	workspace := identities.Workspaces[0]
	driver := datagen.NewDriver(graph, false, seed)
	listNames := func(toolName string, projectReference string) []string {
		result, err := driver.Call(ctx, workspace.Actors[0].Token, toolName, datagen.ToolArguments{
			WorkspaceReference: workspace.Slug, ProjectReference: projectReference,
		})
		if err != nil {
			t.Fatalf("call %s: %v", toolName, err)
		}
		text := result.Text()
		if strings.Contains(text, "Next cursor") {
			t.Fatalf("%s returned more than one page:\n%s", toolName, text)
		}
		var names []string
		for _, line := range strings.Split(text, "\n") {
			if name, found := strings.CutPrefix(line, soakDiscoveryNamePrefix); found {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		return names
	}
	return map[string][]string{
		"tack_list_labels":  listNames("tack_list_labels", ""),
		"tack_list_states":  listNames("tack_list_states", soakDiscoveryProject),
		"tack_list_cycles":  listNames("tack_list_cycles", soakDiscoveryProject),
		"tack_list_modules": listNames("tack_list_modules", soakDiscoveryProject),
	}
}
