package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
)

// probeNode stores the name and text of a target or distractor in the relevance probe.
type probeNode struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// probePhrases stores the target, distractors, and queries from the relevance probe JSON.
type probePhrases struct {
	Target      probeNode   `json:"target"`
	Distractors []probeNode `json:"distractors"`
	Queries     []string    `json:"queries"`
}

// SearchRelevanceProbe reports the target's search positions for its exact name and
// each probe query, with the distractor count and the number of nodes each query returns.
type SearchRelevanceProbe struct {
	TargetID    uuid.UUID              `json:"target_id"`
	Distractors int                    `json:"distractors"`
	TitleRank   int                    `json:"title_rank"`
	Queries     []SearchRelevanceQuery `json:"queries"`
}

// SearchRelevanceQuery reports a query, the target's position starting at 1, and the
// number of returned nodes. A rank of 0 means the search did not return the target.
type SearchRelevanceQuery struct {
	Query    string `json:"query"`
	Rank     int    `json:"rank"`
	Returned int    `json:"returned"`
}

// probeRelevance creates the target and distractors, waits for searches by name to
// return them, and records target ranks across all pages without requiring a minimum rank.
func (r searchRun) probeRelevance(ctx context.Context) (SearchRelevanceProbe, error) {
	phrases := r.phrases.Relevance
	probe := SearchRelevanceProbe{
		TargetID: uuid.Nil, Distractors: len(phrases.Distractors), TitleRank: 0,
		Queries: make([]SearchRelevanceQuery, 0, len(phrases.Queries)),
	}
	items := slices.Concat([]probeNode{phrases.Target}, phrases.Distractors)
	written := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		nodeID, err := r.fixture.put(ctx, item.Name, item.Text, "")
		if err != nil {
			return probe, err
		}
		written = append(written, nodeID)
	}
	probe.TargetID = written[0]
	for position, item := range items {
		if err := r.eventually(ctx, "find relevance probe node "+item.Name, func(ctx context.Context) error {
			return r.includes(ctx, item.Name, written[position])
		}); err != nil {
			return probe, err
		}
	}
	titleRank, _, err := r.rank(ctx, phrases.Target.Name, probe.TargetID)
	if err != nil {
		return probe, err
	}
	probe.TitleRank = titleRank
	for _, query := range phrases.Queries {
		rank, returned, err := r.rank(ctx, query, probe.TargetID)
		if err != nil {
			return probe, err
		}
		probe.Queries = append(probe.Queries, SearchRelevanceQuery{Query: query, Rank: rank, Returned: returned})
	}
	slog.InfoContext(ctx, "qa.datagen.search_relevance_probed", slog.String("target_id", probe.TargetID.String()),
		slog.Int("title_rank", probe.TitleRank), slog.String("queries", fmt.Sprintf("%+v", probe.Queries)))
	return probe, nil
}

// rank resolves every result for query and returns nodeID's position starting
// at 1 and the total node count. It returns position 0 when the search does not
// return nodeID.
func (r searchRun) rank(ctx context.Context, query string, nodeID uuid.UUID) (int, int, error) {
	results, err := traverseSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, r.limits)
	if err != nil {
		return 0, 0, err
	}
	return slices.Index(results, nodeID) + 1, len(results), nil
}
