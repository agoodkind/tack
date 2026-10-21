package integration_test

import (
	_ "embed"
	"encoding/json"
	"slices"
	"testing"
)

//go:embed testdata/search_ranking_doc_v3_gte_idf.json
var rankingIDFJSON []byte

// TestSearchRankingQueryTokenExperiment creates and indexes issues with
// 4096-byte pages. The corpus includes 150 approved semantic distractors,
// 12 target variants, 34 production-shaped distractors, 40 synthetic
// common-word titles, and the synthetic regression target
// "Validate sequential Docker restarts on staging hosts".
//
// The test runs three repeats for each of the six approved queries and
// the regression query "restart containers one by one". Each repeat logs
// the public first-page ranks of the targets.
//
// Each repeat measures three sparse clauses:
//   - The first clause uses the saved query map from the document model.
//   - The second clause uses neural_sparse query_text with the
//     bert-uncased analyzer.
//   - The third clause uses bert-uncased tokens weighted by the model IDF
//     file testdata/search_ranking_doc_v3_gte_idf.json.
//
// For each sparse clause, the test compares lexical multi_match operators
// or and and. It logs each target's combined, lexical, and sparse ranks
// and scores across the whole access-filtered corpus.
// The test does not assert ranks.
func TestSearchRankingQueryTokenExperiment(t *testing.T) {
	run := startRankingRun(t)
	var idf map[string]float64
	if err := json.Unmarshal(rankingIDFJSON, &idf); err != nil {
		t.Fatalf("decode model IDF weights: %v", err)
	}
	corpus := run.addRegressionCorpus(t, run.createApprovedCorpus(t))
	run.waitIndexed(t, corpus.created)
	index := run.servingIndex(t)
	probes := append(slices.Clone(corpus.measured), corpus.probes[0])
	for _, probe := range probes {
		for repeat := 1; repeat <= 3; repeat++ {
			run.experiment(t, index, repeat, probe, idf)
		}
	}
}
