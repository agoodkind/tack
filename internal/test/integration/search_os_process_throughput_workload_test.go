package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

type throughputRequestResult struct {
	latencies []time.Duration
	err       error
	session   bool
}

type throughputRegisteredTool struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func throughputRegistry(t *testing.T, server *actualSearchServer, corpus processThroughputCorpus) map[string]throughputRegisteredTool {
	t.Helper()
	call, err := actualProcessRPC(t, server, corpus.fixture.Harness, uuid.NewString(), "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("public registry preflight: %v", err)
	}
	var result struct {
		Tools []throughputRegisteredTool `json:"tools"`
	}
	if err := json.Unmarshal(call.RawResult, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) == 0 {
		t.Fatal("public registry returned no tools")
	}
	tools := make(map[string]throughputRegisteredTool, len(result.Tools))
	for _, tool := range result.Tools {
		if _, duplicate := tools[tool.Name]; duplicate {
			t.Fatal("public registry returned duplicate tool names")
		}
		tools[tool.Name] = tool
	}
	t.Logf("throughput public registry pid=%d request_id=%s registered_tools=%d", server.command.Process.Pid, call.ID, len(tools))
	return tools
}

func verifyThroughputFixtureContract(t *testing.T, corpus processThroughputCorpus, server *actualSearchServer) {
	t.Helper()
	name := "tack_update_" + corpus.workerKind.TypeKey
	if _, exists := throughputRegistry(t, server, corpus)[name]; exists {
		t.Fatal("missing-operation control unexpectedly registered an update tool")
	}
	probe := corpus
	probe.workerIDs = corpus.calibrationIDs
	before, err := readSearchPageSequence(t, corpus.fixture.Stores, probe.workerIDs[0], runtimePageBytes)
	if err != nil || len(before) == 0 {
		t.Fatalf("read missing-operation control: %v", err)
	}
	refused := processThroughputMutation(t, probe, []*actualSearchServer{server}, -2, 0)
	var protocol actualProtocolError
	if !errors.As(refused.err, &protocol) {
		t.Fatalf("missing-operation control did not return a real protocol refusal: %v", refused.err)
	}
	after, err := readSearchPageSequence(t, corpus.fixture.Stores, probe.workerIDs[0], runtimePageBytes)
	if err != nil || len(after) == 0 || after[0].Revision != before[0].Revision {
		t.Fatalf("missing-operation control changed stored content: %v", err)
	}
	types, err := corpus.fixture.Stores.NodeTypes.List(t.Context(), corpus.workerKind.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, kind := range types {
		if kind.TypeKey == corpus.workerKind.TypeKey {
			kind.AllowedOps = []node.Op{node.OpRead, node.OpUpdate}
			if err := corpus.fixture.Stores.NodeTypes.Set(t.Context(), kind); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("owned worker type is absent from its organization")
	}
	tool, exists := throughputRegistry(t, server, corpus)[name]
	if !exists {
		t.Fatal("owned worker update tool is absent after operation declaration")
	}
	requireThroughputMutationSchema(t, tool)
}

func requireThroughputMutationSchema(t *testing.T, tool throughputRegisteredTool) {
	t.Helper()
	var schema struct {
		Type                 string                     `json:"type"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties || len(schema.Properties) != 4 {
		t.Fatal("public mutation schema has unexpected object or property constraints")
	}
	required := slices.Clone(schema.Required)
	slices.Sort(required)
	if !slices.Equal(required, []string{"node_id", "workspace_reference"}) {
		t.Fatalf("public mutation required parameters changed: %v", required)
	}
	for key, expected := range map[string]string{"node_id": "string", "workspace_reference": "string", "properties": "object", "name": "string"} {
		var field struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(schema.Properties[key], &field); err != nil || field.Type != expected {
			t.Fatalf("public mutation field %s does not match type %s: %v", key, expected, err)
		}
	}
	t.Logf("throughput public mutation schema tool=%s schema=%s", tool.Name, tool.InputSchema)
}

func calibrateProcessThroughput(t *testing.T, corpus processThroughputCorpus, server *actualSearchServer) {
	t.Helper()
	probe := corpus
	probe.workerIDs = corpus.calibrationIDs
	previous := make(map[uuid.UUID]string, len(probe.workerIDs))
	for _, id := range probe.workerIDs {
		pages, err := readSearchPageSequence(t, probe.fixture.Stores, id, runtimePageBytes)
		if err != nil || len(pages) == 0 {
			t.Fatalf("read calibration prior revision: %v", err)
		}
		previous[id] = pages[0].Revision
	}
	info, err := os.Stat(server.logPath)
	if err != nil {
		t.Fatal(err)
	}
	servers := []*actualSearchServer{server}
	for ordinal := range probe.workerIDs {
		result := processThroughputMutation(t, probe, servers, -1, ordinal)
		if result.err != nil {
			t.Fatalf("public worker calibration failed: %v", result.err)
		}
	}
	measurement := processThroughputTrial{}
	waitThroughputRevisions(t, probe, previous, -1, &measurement)
	requireThroughputWorkerEvidence(t, probe, servers, []int64{info.Size()})
	t.Logf("throughput instrument calibration pid=%d exact_property_bytes=1024 indexed_revisions=%d measured_population=false", server.command.Process.Pid, len(probe.workerIDs))
}

func warmupProcessThroughput(t *testing.T, corpus processThroughputCorpus, servers []*actualSearchServer) {
	t.Helper()
	for ordinal := range throughputClients {
		result := processThroughputSession(t, corpus, servers, ordinal)
		if result.err != nil {
			t.Fatalf("throughput warmup: %v", result.err)
		}
	}
}

func runProcessThroughputTrial(t *testing.T, corpus processThroughputCorpus, servers []*actualSearchServer, trial int) processThroughputTrial {
	t.Helper()
	previous := make(map[uuid.UUID]string, len(corpus.workerIDs))
	for _, id := range corpus.workerIDs {
		pages, err := readSearchPageSequence(t, corpus.fixture.Stores, id, runtimePageBytes)
		if err != nil || len(pages) == 0 {
			t.Fatalf("read prior worker revision: %v", err)
		}
		previous[id] = pages[0].Revision
	}
	offsets := make([]int64, len(servers))
	for ordinal, server := range servers {
		info, err := os.Stat(server.logPath)
		if err != nil {
			t.Fatal(err)
		}
		offsets[ordinal] = info.Size()
	}
	jobs := make(chan int, throughputSessions+throughputMutations)
	results := make(chan throughputRequestResult, throughputSessions+throughputMutations)
	for ordinal := range throughputSessions {
		jobs <- ordinal * 2
		jobs <- ordinal*2 + 1
	}
	close(jobs)
	var workers sync.WaitGroup
	started := clock.Now()
	for range throughputClients {
		workers.Go(func() {
			for job := range jobs {
				ordinal := job / 2
				if job%2 == 0 {
					results <- processThroughputSession(t, corpus, servers, ordinal)
				} else {
					results <- processThroughputMutation(t, corpus, servers, trial, ordinal)
				}
			}
		})
	}
	go func() { workers.Wait(); close(results) }()
	measurement := processThroughputTrial{}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
collect:
	for {
		select {
		case result, open := <-results:
			if !open {
				break collect
			}
			measurement.requests += len(result.latencies)
			measurement.latencies = append(measurement.latencies, result.latencies...)
			if result.err != nil {
				measurement.errors++
				t.Logf("throughput sanitized request failure: %v", result.err)
			} else if result.session {
				measurement.sessionsCompleted++
			}
		case <-ticker.C:
			backlog, age := throughputWorkAge(t, corpus.fixture.Config.FDBClusterFile)
			measurement.backlog = max(measurement.backlog, backlog)
			measurement.oldest = max(measurement.oldest, age)
		}
	}
	publicLatencies := slices.Clone(measurement.latencies)
	slices.Sort(publicLatencies)
	t.Logf("throughput public workload trial=%d completed_sessions=%d requests=%d errors=%d request_p50=%s request_p95=%s request_max=%s", trial, measurement.sessionsCompleted, measurement.requests, measurement.errors, throughputPercentile(publicLatencies, 50), throughputPercentile(publicLatencies, 95), throughputPercentile(publicLatencies, 100))
	if measurement.errors == 0 {
		waitThroughputRevisions(t, corpus, previous, trial, &measurement)
		measurement.mutationsIndexed = len(corpus.workerIDs)
	}
	measurement.seconds = clock.Since(started).Seconds()
	t.Logf("throughput completed workload trial=%d requests=%d errors=%d elapsed_seconds=%.6f", trial, measurement.requests, measurement.errors, measurement.seconds)
	if measurement.errors == 0 {
		requireThroughputWorkerEvidence(t, corpus, servers, offsets)
		recordThroughputResources(t, corpus)
	}
	return measurement
}

func processThroughputSession(t *testing.T, corpus processThroughputCorpus, servers []*actualSearchServer, ordinal int) throughputRequestResult {
	session := uuid.NewString()
	cursor := ""
	ids := make([]uuid.UUID, 0, len(corpus.readIDs))
	result := throughputRequestResult{session: true}
	for pageNumber := range 100 {
		arguments := searchArguments(corpus.fixture.Harness, "copper meadow", cursor)
		arguments["node_type"] = corpus.readKind.TypeKey
		call, err := actualProcessTool(t, servers[(ordinal+pageNumber)%len(servers)], corpus.fixture.Harness, session, "tack_search", arguments)
		result.latencies = append(result.latencies, call.Latency)
		if err != nil {
			result.err = err
			return result
		}
		page, err := parseSearchPage(call.Result.Text())
		if err != nil {
			result.err = fmt.Errorf("public search page did not parse")
			return result
		}
		ids = append(ids, page.IDs...)
		if page.Complete {
			expected := slices.Clone(corpus.readIDs)
			compare := func(left, right uuid.UUID) int { return bytes.Compare(left[:], right[:]) }
			slices.SortFunc(expected, compare)
			slices.SortFunc(ids, compare)
			if !slices.Equal(ids, expected) {
				result.err = fmt.Errorf("typed traversal returned %d IDs instead of the exact %d unchanged IDs", len(ids), len(expected))
			}
			return result
		}
		cursor = page.Cursor
	}
	result.err = fmt.Errorf("public throughput session exceeded 100 pages")
	return result
}

func processThroughputMutation(t *testing.T, corpus processThroughputCorpus, servers []*actualSearchServer, trial, ordinal int) throughputRequestResult {
	const propertyBytes = 1024
	prefix := fmt.Sprintf("worker trial %03d node %03d ", trial, ordinal)
	content := prefix + strings.Repeat("x", propertyBytes-len(prefix))
	arguments := map[string]any{"workspace_reference": corpus.fixture.Harness.Workspace, "node_id": corpus.workerIDs[ordinal].String(), "properties": map[string]any{corpus.workerKind.IncludedKey: content}}
	call, err := actualProcessTool(t, servers[ordinal%len(servers)], corpus.fixture.Harness, uuid.NewString(), "tack_update_"+corpus.workerKind.TypeKey, arguments)
	return throughputRequestResult{latencies: []time.Duration{call.Latency}, err: err}
}

func waitThroughputRevisions(t *testing.T, corpus processThroughputCorpus, previous map[uuid.UUID]string, trial int, measurement *processThroughputTrial) {
	t.Helper()
	pages := make(map[uuid.UUID][]node.ContentPage, len(corpus.workerIDs))
	for ordinal, id := range corpus.workerIDs {
		value, err := readSearchPageSequence(t, corpus.fixture.Stores, id, runtimePageBytes)
		if err != nil || len(value) == 0 {
			t.Fatalf("read mutated revision %s: %v", id, err)
		}
		prefix := fmt.Sprintf("worker trial %03d node %03d ", trial, ordinal)
		if value[0].Revision == previous[id] || !strings.Contains(uniqueSearchText(value), prefix+strings.Repeat("x", 1024-len(prefix))) {
			t.Fatalf("public update did not persist the exact new 1024-byte property for node %s", id)
		}
		pages[id] = value
	}
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		backlog, age := throughputWorkAge(t, corpus.fixture.Config.FDBClusterFile)
		measurement.backlog = max(measurement.backlog, backlog)
		measurement.oldest = max(measurement.oldest, age)
		documents := throughputIndexedPages(t, corpus)
		complete := true
		for _, id := range corpus.workerIDs {
			if !throughputPagesMatch(documents[id.String()], pages[id]) {
				complete = false
			}
		}
		if complete && backlog == 0 {
			t.Logf("throughput indexed_revision_completion=%d pending_work=0", len(pages))
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("throughput indexed revisions or empty backlog timed out")
		case <-t.Context().Done():
			t.Fatal("throughput revision verification canceled")
		}
	}
}

func throughputIndexedPages(t *testing.T, corpus processThroughputCorpus) map[string][]searchPageSource {
	t.Helper()
	if _, err := corpus.fixture.Client.Indices.Refresh(t.Context(), &opensearchapi.IndicesRefreshReq{Index: []string{corpus.fixture.Index}}); err != nil {
		t.Fatal(err)
	}
	query := fmt.Sprintf(`{"size":1000,"query":{"bool":{"filter":[{"term":{"node_type":%q}},{"term":{"retired":false}}]}},"sort":[{"page_ordinal":"asc"}]}`, corpus.workerKind.TypeKey)
	response, err := corpus.fixture.Client.Search(t.Context(), &opensearchapi.SearchReq{Indices: []string{corpus.fixture.Index}, Body: strings.NewReader(query)})
	if err != nil || response == nil {
		t.Fatalf("read measured worker documents: %v", err)
	}
	if len(response.Hits.Hits) == 1000 {
		t.Fatal("measured worker documents exceed the bounded readback")
	}
	documents := make(map[string][]searchPageSource)
	for _, hit := range response.Hits.Hits {
		var page searchPageSource
		if err := json.Unmarshal(hit.Source, &page); err != nil {
			t.Fatal(err)
		}
		documents[page.NodeID] = append(documents[page.NodeID], page)
	}
	return documents
}

func throughputPagesMatch(documents []searchPageSource, pages []node.ContentPage) bool {
	if !currentRevisionIndexed(documents, pages) {
		return false
	}
	for ordinal, document := range documents {
		if document.PageOrdinal != pages[ordinal].Ordinal || document.PageText == nil || *document.PageText != pages[ordinal].Text || document.ProjectionVersion != pages[ordinal].ProjectionVersion || document.NodeType != pages[ordinal].NodeType {
			return false
		}
	}
	return true
}

func throughputWorkAge(t *testing.T, cluster string) (int, time.Duration) {
	t.Helper()
	db, err := fdbadapter.Open(cluster, testTransactionTimeout)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := fdb.PrefixRange(tuple.Tuple{"search_age"}.Pack())
	if err != nil {
		t.Fatal(err)
	}
	value, err := db.ReadTransact(func(transaction fdb.ReadTransaction) (any, error) {
		return transaction.GetRange(prefix, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := value.([]fdb.KeyValue)
	oldest := time.Duration(0)
	for _, row := range rows {
		key, err := tuple.Unpack(row.Key)
		if err != nil || len(key) != 6 {
			t.Fatalf("unexpected pending work age key: %v", err)
		}
		enqueued, ok := key[3].(int64)
		if !ok {
			t.Fatal("pending work age timestamp has an unexpected type")
		}
		oldest = max(oldest, clock.Now().Sub(time.Unix(0, enqueued)))
	}
	return len(rows), oldest
}

func requireThroughputWorkerEvidence(t *testing.T, corpus processThroughputCorpus, servers []*actualSearchServer, offsets []int64) {
	t.Helper()
	nodes := make(map[string]bool, len(corpus.workerIDs))
	for _, id := range corpus.workerIDs {
		nodes[id.String()] = true
	}
	for ordinal, server := range servers {
		file, err := os.Open(server.logPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(offsets[ordinal], 0); err != nil {
			file.Close()
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 1048576)
		claims := 0
		for scanner.Scan() {
			var event struct {
				Message string `json:"msg"`
				Class   string `json:"class"`
				NodeID  string `json:"node_id"`
				Pages   int    `json:"pages"`
				Level   string `json:"level"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			if event.Level == "ERROR" && (nodes[event.NodeID] || strings.HasPrefix(event.Message, "search.")) {
				file.Close()
				t.Fatalf("production worker pid=%d logged an error for a measured node", server.command.Process.Pid)
			}
			if event.Message == "search.worker.slice_completed" && event.Class == "live" && nodes[event.NodeID] && event.Pages > 0 {
				claims++
			}
		}
		scanError := scanner.Err()
		closeError := file.Close()
		if scanError != nil || closeError != nil || claims == 0 {
			t.Fatalf("production worker pid=%d has no verified measured live slice: scan=%v close=%v", server.command.Process.Pid, scanError, closeError)
		}
		t.Logf("throughput production worker pid=%d measured_live_slices=%d", server.command.Process.Pid, claims)
	}
}

func recordThroughputResources(t *testing.T, corpus processThroughputCorpus) {
	t.Helper()
	info, err := corpus.fixture.Adapter.IndexInfo(t.Context(), corpus.fixture.Index)
	if err != nil || info != corpus.indexInfo {
		t.Fatalf("throughput mapping or model changed: %v", err)
	}
	settings, err := corpus.fixture.Adapter.IndexSettings(t.Context(), corpus.fixture.Index)
	if err != nil || settings != corpus.settings {
		t.Fatalf("throughput physical topology changed: %v", err)
	}
	engine := testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes)
	evidence, err := testenv.OpenSearchResourceEvidence(t.Context(), engine.Container)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("throughput cgroup resource evidence: %s", evidence)
	var stats struct {
		Nodes map[string]struct {
			FS struct {
				Total struct {
					Total     int64 `json:"total_in_bytes"`
					Available int64 `json:"available_in_bytes"`
				} `json:"total"`
			} `json:"fs"`
		} `json:"nodes"`
	}
	response, err := opensearch.Do(t.Context(), corpus.fixture.Client.Client, http.MethodGet, opensearchapi.NodesStatsReq{Metric: []string{"fs"}}, &stats)
	if err != nil || response == nil || response.IsError() || len(stats.Nodes) != 1 {
		t.Fatalf("read throughput filesystem resources: %v", err)
	}
	var indexStats struct {
		All struct {
			Primaries struct {
				Store struct {
					Bytes int64 `json:"size_in_bytes"`
				} `json:"store"`
			} `json:"primaries"`
		} `json:"_all"`
	}
	indexResponse, err := opensearch.Do(t.Context(), corpus.fixture.Client.Client, http.MethodGet, opensearchapi.IndicesStatsReq{Indices: []string{corpus.fixture.Index}, Metrics: []string{"store"}}, &indexStats)
	if err != nil || indexResponse == nil || indexResponse.IsError() || indexStats.All.Primaries.Store.Bytes <= 0 {
		t.Fatalf("read throughput primary index size: %v", err)
	}
	for id, value := range stats.Nodes {
		primary := indexStats.All.Primaries.Store.Bytes
		nonIndex := max(int64(0), value.FS.Total.Total-value.FS.Total.Available-primary)
		required := float64(nonIndex+3*primary) / 0.85
		t.Logf("throughput disk node=%s total_bytes=%d available_bytes=%d primary_index_bytes=%d non_index_bytes=%d required_bytes=%.0f", id, value.FS.Total.Total, value.FS.Total.Available, primary, nonIndex, required)
		if required > float64(value.FS.Total.Total) {
			t.Fatal("throughput disk does not satisfy the 85 percent disk gate")
		}
	}
}
