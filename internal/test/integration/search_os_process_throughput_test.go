package integration

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/testenv"
)

const (
	throughputSessions  = 48
	throughputMutations = 48
	throughputClients   = 4
)

type processThroughputCorpus struct {
	fixture        queryFixture
	readKind       opaqueKind
	workerKind     opaqueKind
	readIDs        []uuid.UUID
	workerIDs      []uuid.UUID
	calibrationIDs []uuid.UUID
	indexInfo      search.IndexInfo
	settings       search.PhysicalSettings
}

type processThroughputTrial struct {
	seconds           float64
	requests          int
	errors            int
	latencies         []time.Duration
	backlog           int
	oldest            time.Duration
	sessionsCompleted int
	mutationsIndexed  int
}

func (trial processThroughputTrial) rate() float64 {
	return float64(trial.sessionsCompleted+trial.mutationsIndexed) / trial.seconds
}

// TestSearchActualOSProcessThroughput compares server count with fixed clients and unchanged read nodes.
func TestSearchActualOSProcessThroughput(t *testing.T) {
	if os.Getenv("TACK_SEARCH_INTEGRATION") != "1" {
		t.Skip("OpenSearch integration requires TACK_SEARCH_INTEGRATION=1")
	}
	revision := actualServerRevision(t)
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	corpus := newProcessThroughputCorpus(t)
	binary := buildActualSearchServer(t, revision)
	first := startActualSearchServer(t, binary, corpus.fixture.Config)
	recordThroughputResources(t, corpus)
	containers := recordThroughputLimits(t, corpus)
	verifyThroughputFixtureContract(t, corpus, first)
	calibrateProcessThroughput(t, corpus, first)
	controls := make([]float64, 0, 3)
	baseline := make([]float64, 0, 3)
	treatment := make([]float64, 0, 3)
	for ordinal, processes := range []int{1, 1, 1, 1, 2, 2, 1, 1, 2} {
		servers := []*actualSearchServer{first}
		if processes == 2 {
			second := startActualSearchServer(t, binary, corpus.fixture.Config)
			if first.command.Process.Pid == second.command.Process.Pid {
				t.Fatal("throughput requires distinct OS process IDs")
			}
			servers = append(servers, second)
		}
		warmupProcessThroughput(t, corpus, servers)
		trial := runMeasuredThroughputTrial(t, corpus, servers, ordinal, containers)
		latencies := slices.Clone(trial.latencies)
		slices.Sort(latencies)
		t.Logf("throughput trial=%d complete_sessions_per_second=%.6f indexed_mutations_per_second=%.6f request_error_rate=%.6f", ordinal, float64(trial.sessionsCompleted)/trial.seconds, float64(trial.mutationsIndexed)/trial.seconds, float64(trial.errors)/float64(max(1, trial.requests)))
		t.Logf("throughput trial=%d processes=%d clients=%d completed_sessions=%d indexed_mutations=%d requests=%d errors=%d elapsed_seconds=%.6f units_per_second=%.6f request_p50=%s request_p95=%s request_max=%s sampled_backlog=%d sampled_oldest=%s", ordinal, processes, throughputClients, trial.sessionsCompleted, trial.mutationsIndexed, trial.requests, trial.errors, trial.seconds, trial.rate(), throughputPercentile(latencies, 50), throughputPercentile(latencies, 95), throughputPercentile(latencies, 100), trial.backlog, trial.oldest)
		if len(servers) == 2 {
			servers[1].stop(t)
		}
		if trial.errors != 0 {
			t.Fatalf("throughput trial %d contains %d failed public requests", ordinal, trial.errors)
		}
		if ordinal < 3 {
			controls = append(controls, trial.rate())
		} else if processes == 1 {
			baseline = append(baseline, trial.rate())
		} else {
			treatment = append(treatment, trial.rate())
		}
	}
	slices.Sort(controls)
	slices.Sort(baseline)
	slices.Sort(treatment)
	spread := controls[2] - controls[0]
	gain := treatment[1] - baseline[1]
	t.Logf("throughput verdict baseline_median=%.6f treatment_median=%.6f gain=%.6f unchanged_baseline_spread=%.6f absolute_SLA=unmeasured production_capacity=unmeasured", baseline[1], treatment[1], gain, spread)
	if gain <= spread {
		t.Fatalf("two-process throughput gain %.6f does not exceed unchanged baseline spread %.6f", gain, spread)
	}
}

func throughputPercentile(values []time.Duration, percent int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	return values[(len(values)*percent+99)/100-1]
}

func newProcessThroughputCorpus(t *testing.T) processThroughputCorpus {
	t.Helper()
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
		t.Fatalf("throughput disposable cluster is not empty: %v", err)
	}
	adapter, client := nativeSearchClients(t)
	requireNativeJVMHeap(t, client)
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
	fixture := queryFixture{Stores: stores, Adapter: adapter, Client: client, Index: index, Config: cfg, Harness: harness, Workspaces: identities.Workspaces, Worker: newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(runtimePageBytes))}
	corpus := processThroughputCorpus{fixture: fixture, readKind: putOpaqueKind(t, fixture, workspace.OrgID), workerKind: putOpaqueKind(t, fixture, workspace.OrgID)}
	parent := entryPoint(t, fixture, workspace)
	for ordinal := range 140 {
		corpus.readIDs = append(corpus.readIDs, putOpaqueNode(t, fixture, corpus.readKind, parent, fmt.Sprintf("read-%03d", ordinal), "copper meadow unchanged reading corpus", "excluded"))
	}
	for ordinal := range throughputMutations {
		corpus.workerIDs = append(corpus.workerIDs, createThroughputWorkerNode(t, fixture, corpus.workerKind, parent, workspace.Actors[0].UserID, fmt.Sprintf("worker-%03d", ordinal), "initial worker content"))
	}
	for ordinal := range 2 {
		corpus.calibrationIDs = append(corpus.calibrationIDs, createThroughputWorkerNode(t, fixture, corpus.workerKind, parent, workspace.Actors[0].UserID, fmt.Sprintf("calibration-%03d", ordinal), "initial calibration content"))
	}
	drainSearchWork(t, fixture.Worker, 3000)
	corpus.indexInfo, err = adapter.IndexInfo(t.Context(), index)
	if err != nil {
		t.Fatal(err)
	}
	corpus.settings, err = adapter.IndexSettings(t.Context(), index)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("throughput fixed corpus read_nodes=%d worker_nodes=%d index=%s model=%s mapping=1 primaries=1 routing_shards=24 replicas=0", len(corpus.readIDs), len(corpus.workerIDs), index, model.ID)
	return corpus
}
