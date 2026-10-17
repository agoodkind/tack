package integration_test

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
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

//go:embed testdata/search_ranking_common_word_names.json
var rankingCommonWordNamesJSON []byte

var rankingApprovedQueries = []string{"db", "signin", "lag", "invoice", "crash", "remove user"}

type rankingRecord struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type rankingRegression struct {
	Query       string          `json:"query"`
	Target      rankingRecord   `json:"target"`
	Distractors []rankingRecord `json:"distractors"`
}

type rankingApprovedNode struct {
	Identifier string `json:"identifier"`
	Text       string `json:"text"`
}

type rankingCorpus struct {
	created  []uuid.UUID
	approved []rankingProbe
	probes   []rankingProbe
}

// TestSearchRankingProductionRegression verifies search rankings before and
// after a full index replacement. It indexes the approved semantic corpus
// of 150 distractors and 12 target variants, 34 production-shaped
// distractors, 40 synthetic common-word titles, and the regression target
// as issues with 4096-byte pages.
//
// It requires three times each, before and after replacement:
//   - Both targets of each of the six approved queries appear on the first
//     page of 25 results.
//   - The regression target appears on the first page for
//     "restart containers one by one".
//   - The regression target appears at rank 1 for its exact title.
//
// Each attempt follows its cursor to the last page with the saved query
// token and replay checks.
func TestSearchRankingProductionRegression(t *testing.T) {
	run := startRankingRun(t)
	corpus := run.createRankingCorpus(t)
	run.waitIndexed(t, corpus.created)
	run.requireAll(t, "initial", corpus)
	rebuild := run.beginRebuild(t)
	run.waitRebuild(t, func(current searchdomain.Rebuild, found bool) bool {
		return !found || current.State == searchdomain.RebuildRetiring
	})
	if serving := run.servingIndex(t); serving != rebuild.TargetIndex {
		t.Fatalf("serving index after the replacement = %s, want %s", serving, rebuild.TargetIndex)
	}
	run.requireAll(t, "rebuilt", corpus)
	run.waitRebuild(t, func(_ searchdomain.Rebuild, found bool) bool { return !found })
}

func startRankingRun(t *testing.T) *rankingRun {
	t.Helper()
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_PAGE_BYTES", strconv.Itoa(rankingPageBytes))
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	graph, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build search graph: %v", err)
	}
	t.Cleanup(graph.Close)
	graph.StartSearchWorkers(t.Context())
	return newRankingRun(t, cfg, graph)
}

func (r *rankingRun) createRankingCorpus(t *testing.T) rankingCorpus {
	t.Helper()
	var regression rankingRegression
	if err := json.Unmarshal(rankingRegressionJSON, &regression); err != nil {
		t.Fatalf("decode ranking regression corpus: %v", err)
	}
	var approved []rankingApprovedNode
	if err := json.Unmarshal(rankingApprovedJSON, &approved); err != nil {
		t.Fatalf("decode approved semantic corpus: %v", err)
	}
	var commonWordNames []string
	if err := json.Unmarshal(rankingCommonWordNamesJSON, &commonWordNames); err != nil {
		t.Fatalf("decode common word names: %v", err)
	}
	corpus := rankingCorpus{created: nil, approved: make([]rankingProbe, len(rankingApprovedQueries)), probes: nil}
	for position, query := range rankingApprovedQueries {
		corpus.approved[position] = rankingProbe{Query: query, Targets: nil, First: false}
	}
	for _, node := range approved {
		nodeID := r.createIssue(t, node.Text, node.Text)
		corpus.created = append(corpus.created, nodeID)
		if strings.HasPrefix(node.Identifier, "t-") {
			position := int(node.Identifier[2] - '0')
			corpus.approved[position].Targets = append(corpus.approved[position].Targets, nodeID)
		}
	}
	for _, record := range regression.Distractors {
		corpus.created = append(corpus.created, r.createIssue(t, record.Name, record.Description))
	}
	for _, name := range commonWordNames {
		corpus.created = append(corpus.created, r.createIssue(t, name, ""))
	}
	target := r.createIssue(t, regression.Target.Name, regression.Target.Description)
	corpus.created = append(corpus.created, target)
	corpus.probes = append(append([]rankingProbe(nil), corpus.approved...),
		rankingProbe{Query: regression.Query, Targets: []uuid.UUID{target}, First: false},
		rankingProbe{Query: regression.Target.Name, Targets: []uuid.UUID{target}, First: true},
	)
	return corpus
}

func (r *rankingRun) requireAll(t *testing.T, phase string, corpus rankingCorpus) {
	t.Helper()
	for _, probe := range corpus.probes {
		r.require(t, phase, probe)
	}
	r.requireLexicalControl(t, phase, corpus.approved)
}
