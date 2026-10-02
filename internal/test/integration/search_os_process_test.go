package integration

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchCursorActualOSProcesses uses a fresh cluster because production servers use unprefixed keys.
func TestSearchCursorActualOSProcesses(t *testing.T) {
	if os.Getenv("TACK_SEARCH_INTEGRATION") != "1" {
		t.Skip("OpenSearch integration requires TACK_SEARCH_INTEGRATION=1")
	}
	revision := actualServerRevision(t)
	prefix := slices.Clone(fdbadapter.TestPrefixRange())
	fdbadapter.SetTestPrefix(nil)
	t.Cleanup(func() { fdbadapter.SetTestPrefix(prefix) })
	cluster := testenv.FreshFoundationDB(t)
	stores, err := fdbadapter.NewStores(cluster, testTransactionTimeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	stores.EnableSearchWork()
	index := "node-pages-" + uuid.NewString()
	if err := stores.InitializeSearchIndex(t.Context(), index); err != nil {
		t.Fatalf("disposable cluster is not empty: %v", err)
	}
	adapter, client := nativeSearchClients(t)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	spec := search.IndexSpec{Model: model, MappingVersion: "1", Primaries: 1, RoutingShards: 24, Replicas: 0}
	if err := adapter.EnsureIndex(t.Context(), index, spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteNativeIndex(t, client, index) })
	if err := adapter.SetAlias(t.Context(), search.PublicAlias, index); err != nil {
		t.Fatal(err)
	}
	configureSearchRuntime(t)
	configureQueryEnvironment(t, defaultQueryOptions())
	cfg := queryConfig(t)
	cfg.FDBClusterFile = cluster
	cfg.DatagenAllowTarget = "local"
	if err := datagen.ValidateTarget(cfg); err != nil {
		t.Fatal(err)
	}
	scale, err := datagen.ParseScale("medium")
	if err != nil {
		t.Fatal(err)
	}
	identities, err := datagen.BootstrapIdentities(t.Context(), cfg, nextHarnessSeed(), scale)
	if err != nil {
		t.Fatal(err)
	}
	workspace := identities.Workspaces[0]
	harness := &MCPHarness{token: workspace.Actors[0].Token, ledgerDSN: cfg.DatabaseURL, orgID: workspace.OrgID, Workspace: workspace.Slug}
	fixture := queryFixture{
		Stores: stores, Adapter: adapter, Client: client, Index: index, Config: cfg, Harness: harness, Workspaces: identities.Workspaces,
		Worker: newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(runtimePageBytes)),
	}
	const query = "copper meadow"
	nodes := putCursorCorpus(t, fixture, query, 140)
	binary := buildActualSearchServer(t, revision)
	first := startActualSearchServer(t, binary, cfg)
	second := startActualSearchServer(t, binary, cfg)
	if first.command.Process.Pid == second.command.Process.Pid {
		t.Fatal("server PIDs are not distinct")
	}
	t.Logf("shared disposable dependencies fdb_cluster=%s search_endpoint=%s index=%s org=%s", cfg.FDBClusterFile, cfg.SearchEndpoint, index, workspace.OrgID)
	servers := []*actualSearchServer{first, second}
	session := uuid.NewString()
	ids := make([]uuid.UUID, 0)
	cursor := ""
	for number := range 1000 {
		page, err := actualProcessSearch(t, servers[number%2], harness, session, query, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if number == 0 && page.Complete {
			t.Fatal("actual process corpus returned no continuation")
		}
		ids = append(ids, page.IDs...)
		if page.Complete {
			break
		}
		cursor = page.Cursor
		if number == 999 {
			t.Fatal("actual process traversal did not complete")
		}
	}
	requireCorpusOnce(t, ids, nodes, entryPoint(t, fixture, workspace))
	replaySession := uuid.NewString()
	opened, err := actualProcessSearch(t, first, harness, replaySession, query, "")
	if err != nil || opened.Cursor == "" {
		t.Fatalf("open restart session: %v", err)
	}
	continued, err := actualProcessSearch(t, second, harness, replaySession, query, opened.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	oldPID := first.command.Process.Pid
	first.stop(t)
	restarted := startActualSearchServer(t, binary, cfg)
	if restarted.command.Process.Pid == oldPID || restarted.command.Process.Pid == second.command.Process.Pid {
		t.Fatal("restart did not create an independent OS process")
	}
	replayed, err := actualProcessSearch(t, restarted, harness, replaySession, query, opened.Cursor)
	if err != nil || !slices.Equal(continued.IDs, replayed.IDs) || continued.Cursor != replayed.Cursor || continued.Complete != replayed.Complete {
		t.Fatalf("fresh process replay differs: %v", err)
	}
	pool, err := postgres.NewPool(t.Context(), cfg.DatabaseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.NewOrgMemberRepo(pool).RemoveMember(t.Context(), workspace.OrgID, workspace.Actors[0].UserID); err != nil {
		t.Fatal(err)
	}
	for _, server := range []*actualSearchServer{second, restarted} {
		_, err := actualProcessSearch(t, server, harness, replaySession, query, opened.Cursor)
		var refusal actualSearchToolRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("actual process did not return a valid MCP refusal after membership revocation: %v", err)
		}
	}
	if len(workspace.Actors) < 2 {
		t.Fatal("revocation control requires a second current member")
	}
	control := *harness
	control.token = workspace.Actors[1].Token
	controlSession := uuid.NewString()
	controlIDs := make([]uuid.UUID, 0)
	controlCursor := ""
	for number := range 1000 {
		server := []*actualSearchServer{second, restarted}[number%2]
		page, err := actualProcessSearch(t, server, &control, controlSession, query, controlCursor)
		if err != nil {
			t.Fatalf("current member control failed: %v", err)
		}
		controlIDs = append(controlIDs, page.IDs...)
		if page.Complete {
			break
		}
		controlCursor = page.Cursor
		if number == 999 {
			t.Fatal("current member control did not complete")
		}
	}
	requireCorpusOnce(t, controlIDs, nodes, entryPoint(t, fixture, workspace))
	t.Logf("actual OS process traversal=%d corpus=%d restart_pid=%d previous_pid=%d with durable replay and current membership refusal", len(ids), len(nodes), restarted.command.Process.Pid, oldPID)
}
