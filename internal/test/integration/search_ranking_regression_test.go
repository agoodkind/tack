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
	probes   []rankingProbe
	control  []rankingProbe
	measured []rankingProbe
}

// TestSearchRankingApprovedQueries indexes only the approved semantic corpus of
// 150 distractors and 12 target variants as issues with 4096-byte pages.
// Before and after a full index replacement, it checks these requirements:
//   - Each of the six approved queries returns both targets on the first page
//     of 25 results in each of three attempts.
//   - Each attempt follows its cursor to the last page with the saved query
//     token and performs replay checks.
//   - The lexical-only control misses at least one approved target in each phase.
func TestSearchRankingApprovedQueries(t *testing.T) {
	run := startRankingRun(t)
	run.requireAcrossReplacement(t, run.createApprovedCorpus(t))
}

// TestSearchRankingProductionRegression indexes the approved semantic corpus,
// 34 production-shaped distractors, 40 synthetic common-word titles, and the
// regression target as issues with 4096-byte pages.
// Before and after a full index replacement, it checks these requirements:
//   - The query "restart containers one by one" returns the regression target
//     on the first page in each of three attempts.
//   - The exact title query returns the regression target at rank 1 in each of
//     three attempts.
//   - Each attempt follows its cursor to the last page with the saved query
//     token and performs replay checks.
//
// Before and after the full index replacement, the test logs the ranks of both
// targets of each of the six approved queries in the full corpus without
// asserting those ranks.
func TestSearchRankingProductionRegression(t *testing.T) {
	run := startRankingRun(t)
	run.requireAcrossReplacement(t, run.addRegressionCorpus(t, run.createApprovedCorpus(t)))
}

// requireAcrossReplacement waits until every created node is indexed, checks
// the corpus probes, and runs a full index replacement. It requires the
// replacement index to serve, checks the probes again, and waits until the
// replacement finishes.
func (r *rankingRun) requireAcrossReplacement(t *testing.T, corpus rankingCorpus) {
	t.Helper()
	r.waitIndexed(t, corpus.created)
	r.requireAll(t, "initial", corpus)
	rebuild := r.beginRebuild(t)
	r.waitRebuild(t, func(current searchdomain.Rebuild, found bool) bool {
		return !found || current.State == searchdomain.RebuildRetiring
	})
	if serving := r.servingIndex(t); serving != rebuild.TargetIndex {
		t.Fatalf("serving index after the replacement = %s, want %s", serving, rebuild.TargetIndex)
	}
	r.requireAll(t, "rebuilt", corpus)
	r.waitRebuild(t, func(_ searchdomain.Rebuild, found bool) bool { return !found })
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

// createApprovedCorpus creates one issue per approved corpus node and uses the
// node text as its name and description. It returns the six approved query
// probes with two targets each and also uses those probes as the lexical control.
func (r *rankingRun) createApprovedCorpus(t *testing.T) rankingCorpus {
	t.Helper()
	var approved []rankingApprovedNode
	if err := json.Unmarshal(rankingApprovedJSON, &approved); err != nil {
		t.Fatalf("decode approved semantic corpus: %v", err)
	}
	probes := make([]rankingProbe, len(rankingApprovedQueries))
	for position, query := range rankingApprovedQueries {
		probes[position] = rankingProbe{Query: query, Targets: nil, First: false}
	}
	corpus := rankingCorpus{created: nil, probes: probes, control: probes, measured: nil}
	for _, node := range approved {
		nodeID := r.createIssue(t, node.Text, node.Text)
		corpus.created = append(corpus.created, nodeID)
		if strings.HasPrefix(node.Identifier, "t-") {
			position := int(node.Identifier[2] - '0')
			probes[position].Targets = append(probes[position].Targets, nodeID)
		}
	}
	return corpus
}

// addRegressionCorpus creates the 34 distractors, 40 common-word titles, and
// regression target after the approved corpus. It returns all created nodes
// with the regression query probe and exact title probe, and does not return a
// lexical control. It also returns the six approved query probes as measured
// probes.
func (r *rankingRun) addRegressionCorpus(t *testing.T, approved rankingCorpus) rankingCorpus {
	t.Helper()
	var regression rankingRegression
	if err := json.Unmarshal(rankingRegressionJSON, &regression); err != nil {
		t.Fatalf("decode ranking regression corpus: %v", err)
	}
	var commonWordNames []string
	if err := json.Unmarshal(rankingCommonWordNamesJSON, &commonWordNames); err != nil {
		t.Fatalf("decode common word names: %v", err)
	}
	created := append([]uuid.UUID(nil), approved.created...)
	for _, record := range regression.Distractors {
		created = append(created, r.createIssue(t, record.Name, record.Description))
	}
	for _, name := range commonWordNames {
		created = append(created, r.createIssue(t, name, ""))
	}
	target := r.createIssue(t, regression.Target.Name, regression.Target.Description)
	return rankingCorpus{
		created: append(created, target),
		probes: []rankingProbe{
			{Query: regression.Query, Targets: []uuid.UUID{target}, First: false},
			{Query: regression.Target.Name, Targets: []uuid.UUID{target}, First: true},
		},
		control:  nil,
		measured: approved.probes,
	}
}

func (r *rankingRun) requireAll(t *testing.T, phase string, corpus rankingCorpus) {
	t.Helper()
	for _, probe := range corpus.probes {
		r.require(t, phase, probe)
	}
	if corpus.control != nil {
		r.requireLexicalControl(t, phase, corpus.control)
	}
	for _, probe := range corpus.measured {
		r.measure(t, phase, probe)
	}
}
