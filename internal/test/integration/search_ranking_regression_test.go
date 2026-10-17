package integration_test

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"

	searchdomain "goodkind.io/tack/internal/domain/search"
	appruntime "goodkind.io/tack/internal/runtime"
)

// rankingPageBytes matches the 4096-byte default that production uses.
// Configs does not render an OPENSEARCH_PAGE_BYTES value.
const rankingPageBytes = 4096

//go:embed testdata/search_ranking_regression.json
var rankingRegressionJSON []byte

//go:embed testdata/search_semantic_corpus.json
var rankingApprovedJSON []byte

// rankingRecord is one issue with a name and a Markdown description.
type rankingRecord struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// rankingRegression is one production query, the issue that the query must
// return on the first page, and production-shaped distractor issues.
type rankingRegression struct {
	Query       string          `json:"query"`
	Target      rankingRecord   `json:"target"`
	Distractors []rankingRecord `json:"distractors"`
}

type rankingApprovedNode struct {
	Identifier string `json:"identifier"`
	Text       string `json:"text"`
}

// TestSearchRankingProductionRegression requires the target of
// "restart containers one by one" on the first page of 25 results in three
// attempts before and after a full index replacement.
func TestSearchRankingProductionRegression(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_PAGE_BYTES", strconv.Itoa(rankingPageBytes))
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	graph, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build search graph: %v", err)
	}
	t.Cleanup(graph.Close)
	graph.StartSearchWorkers(t.Context())
	run := newRankingRun(t, cfg, graph)
	var regression rankingRegression
	if err := json.Unmarshal(rankingRegressionJSON, &regression); err != nil {
		t.Fatalf("decode ranking regression corpus: %v", err)
	}
	var approved []rankingApprovedNode
	if err := json.Unmarshal(rankingApprovedJSON, &approved); err != nil {
		t.Fatalf("decode approved semantic corpus: %v", err)
	}
	created := make([]uuid.UUID, 0, len(approved)+len(regression.Distractors)+1)
	for _, node := range approved {
		created = append(created, run.createIssue(t, node.Text, node.Text))
	}
	for _, record := range regression.Distractors {
		created = append(created, run.createIssue(t, record.Name, record.Description))
	}
	run.query = regression.Query
	run.target = run.createIssue(t, regression.Target.Name, regression.Target.Description)
	run.waitIndexed(t, append(created, run.target))
	run.require(t, "initial")
	rebuild := run.beginRebuild(t)
	run.waitRebuild(t, func(current searchdomain.Rebuild, found bool) bool {
		return !found || current.State == searchdomain.RebuildRetiring
	})
	if serving := run.servingIndex(t); serving != rebuild.TargetIndex {
		t.Fatalf("serving index after the replacement = %s, want %s", serving, rebuild.TargetIndex)
	}
	run.require(t, "rebuilt")
	run.waitRebuild(t, func(_ searchdomain.Rebuild, found bool) bool { return !found })
}
