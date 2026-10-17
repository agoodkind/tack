package integration_test

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	appruntime "goodkind.io/tack/internal/runtime"
)

//go:embed testdata/search_ranking_doc_v3_gte_idf.json
var rankingIDFJSON []byte

// rankingExperimentQueries lists the six approved semantic queries: db,
// signin, lag, invoice, crash, and remove user. Each query's slice index
// equals the digit after t- in its approved corpus target identifiers.
var rankingExperimentQueries = []string{"db", "signin", "lag", "invoice", "crash", "remove user"}

// TestSearchRankingQueryTokenExperiment creates the approved corpus, the
// 34 regression distractors, and the TACK-578 copy as issues and indexes
// them with 4096-byte pages. It runs three repeats for each of the six
// approved queries and the regression query restart containers one by one.
//
// Each repeat logs the public first-page ranks and each target's combined,
// lexical, and sparse ranks and scores for three sparse clauses.
// The clauses use the saved query map from the document model,
// neural_sparse query_text with the bert-uncased analyzer, and bert-uncased
// tokens weighted by the model IDF file
// testdata/search_ranking_doc_v3_gte_idf.json.
// The test does not assert ranks.
func TestSearchRankingQueryTokenExperiment(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_PAGE_BYTES", strconv.Itoa(rankingPageBytes))
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
	var idf map[string]float64
	if err := json.Unmarshal(rankingIDFJSON, &idf); err != nil {
		t.Fatalf("decode model IDF weights: %v", err)
	}
	targets := map[string][]uuid.UUID{}
	created := make([]uuid.UUID, 0, len(approved)+len(regression.Distractors)+1)
	for _, node := range approved {
		nodeID := run.createIssue(t, node.Text, node.Text)
		created = append(created, nodeID)
		if !strings.HasPrefix(node.Identifier, "t-") {
			continue
		}
		position := int(node.Identifier[2] - '0')
		targets[rankingExperimentQueries[position]] = append(targets[rankingExperimentQueries[position]], nodeID)
	}
	for _, record := range regression.Distractors {
		created = append(created, run.createIssue(t, record.Name, record.Description))
	}
	created = append(created, run.createCommonWordNames(t)...)
	target := run.createIssue(t, regression.Target.Name, regression.Target.Description)
	targets[regression.Query] = []uuid.UUID{target}
	run.waitIndexed(t, append(created, target))
	index := run.servingIndex(t)
	for _, query := range append(append([]string(nil), rankingExperimentQueries...), regression.Query) {
		run.query = query
		for repeat := 1; repeat <= 3; repeat++ {
			run.experiment(t, index, repeat, targets[query], idf)
		}
	}
}
