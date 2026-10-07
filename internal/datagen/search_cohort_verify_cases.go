package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
)

// missingTargets returns the number of expected nodes that the complete
// traversal of their case does not return.
func (s cohortSession) missingTargets(ctx context.Context, cases []SearchManifestCase) int {
	missing := 0
	for _, testCase := range cases {
		results, err := s.traverse(ctx, testCase.Query, testCase.NodeType, 0)
		if err != nil {
			slog.ErrorContext(ctx, "qa.datagen.cohort_search_failed", slog.String("query", testCase.Query),
				slog.String("node_type", testCase.NodeType), slog.String("err", err.Error()))
			missing += len(testCase.ExpectedIDs)
			continue
		}
		for _, nodeID := range testCase.ExpectedIDs {
			if !slices.Contains(results, nodeID) {
				missing++
			}
		}
	}
	return missing
}

// checkCase applies the declared rule of one case.
// checkCase requires every expected node to rank from 1 through
// RankLimit for ranked_targets.
// checkCase requires the complete traversal to return exactly
// the expected nodes for exact.
func (s cohortSession) checkCase(ctx context.Context, testCase SearchManifestCase) SearchCohortCase {
	result := SearchCohortCase{
		Query: testCase.Query, NodeType: testCase.NodeType, Match: testCase.Match, RankLimit: testCase.RankLimit,
		Passed: false, Ranks: make([]int, len(testCase.ExpectedIDs)), Returned: 0, Unexpected: 0, Error: "",
	}
	stopAfter := 0
	if testCase.Match == matchRankedTargets {
		stopAfter = testCase.RankLimit
	}
	results, err := s.traverse(ctx, testCase.Query, testCase.NodeType, stopAfter)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Returned = len(results)
	for position, nodeID := range testCase.ExpectedIDs {
		result.Ranks[position] = slices.Index(results, nodeID) + 1
	}
	switch testCase.Match {
	case matchRankedTargets:
		result.Passed = testCase.RankLimit > 0 && allRanked(result.Ranks, testCase.RankLimit)
	case matchExact:
		result.Unexpected = countUnexpected(results, testCase.ExpectedIDs)
		result.Passed = allRanked(result.Ranks, len(results)) && result.Unexpected == 0
	default:
		result.Error = "unknown match rule " + testCase.Match
	}
	return result
}

// allRanked reports whether every rank is between 1 and limit.
func allRanked(ranks []int, limit int) bool {
	for _, rank := range ranks {
		if rank < 1 || rank > limit {
			return false
		}
	}
	return true
}

// countUnexpected returns the number of results outside expected.
func countUnexpected(results, expected []uuid.UUID) int {
	unexpected := 0
	for _, nodeID := range results {
		if !slices.Contains(expected, nodeID) {
			unexpected++
		}
	}
	return unexpected
}

// traverse follows continuation cursors for query limited to nodeType. It
// rejects a duplicate node, a repeated cursor, and a page above the public
// result or byte bound.
// traverse stops at a page boundary when stopAfter is positive and
// the total node count is at least stopAfter.
func (s cohortSession) traverse(ctx context.Context, query, nodeType string, stopAfter int) ([]uuid.UUID, error) {
	seen := make(map[uuid.UUID]struct{})
	cursors := make(map[string]struct{})
	ordered := make([]uuid.UUID, 0)
	cursor := ""
	for range maxSearchTraversalPages {
		page, err := callTypedSearch(ctx, s.driver, s.token, s.entry, query, nodeType, cursor)
		if err != nil {
			return nil, err
		}
		if len(page.IDs) > s.limits.MaxResults || page.ResponseBytes > s.limits.MaxResponseBytes {
			return nil, fmt.Errorf("qa datagen: search %q of type %s returned %d nodes in %d bytes, above %d nodes or %d bytes",
				query, nodeType, len(page.IDs), page.ResponseBytes, s.limits.MaxResults, s.limits.MaxResponseBytes)
		}
		for _, nodeID := range page.IDs {
			if _, duplicate := seen[nodeID]; duplicate {
				return nil, fmt.Errorf("qa datagen: search %q of type %s returned node %s twice", query, nodeType, nodeID)
			}
			seen[nodeID] = struct{}{}
			ordered = append(ordered, nodeID)
		}
		if page.Cursor == "" || (stopAfter > 0 && len(ordered) >= stopAfter) {
			return ordered, nil
		}
		if _, repeated := cursors[page.Cursor]; repeated {
			return nil, fmt.Errorf("qa datagen: search %q of type %s repeated a continuation cursor", query, nodeType)
		}
		cursors[page.Cursor] = struct{}{}
		cursor = page.Cursor
	}
	return nil, fmt.Errorf("qa datagen: search %q of type %s did not finish within %d pages", query, nodeType, maxSearchTraversalPages)
}
