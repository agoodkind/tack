package integration_test

import (
	"encoding/json"
	"slices"
	"testing"
)

const rankingFirstPage = 25

// requireLexicalControl runs the lexical clause alone with the caller's
// access filters for each approved query. It requires at least one query
// with a target outside the first 25 results.
func (r *rankingRun) requireLexicalControl(t *testing.T, phase string, probes []rankingProbe) {
	t.Helper()
	page := r.search(t, probes[0].Query, "")
	if page.Cursor == "" {
		t.Fatalf("the first page of %q completed the session", probes[0].Query)
	}
	session := r.session(t, page.Cursor)
	filters := traceFilters(t, session.Query.Access)
	misses := 0
	for _, probe := range probes {
		ranked := r.clauseRanks(t, session.Query.Index, filters, []json.RawMessage{rankingLexicalClause(t, probe.Query)})
		top := make([]string, 0, rankingFirstPage)
		for _, ranking := range ranked[:min(rankingFirstPage, len(ranked))] {
			top = append(top, ranking.ID.String())
		}
		for _, target := range probe.Targets {
			if !slices.Contains(top, target.String()) {
				misses++
				break
			}
		}
	}
	t.Logf("ranking phase=%s lexical_control_misses=%d probes=%d", phase, misses, len(probes))
	if misses == 0 {
		t.Errorf("the lexical-only control returned every approved target in its first %d results", rankingFirstPage)
	}
	r.completeCursor(t, probes[0].Query, page.Cursor)
}
