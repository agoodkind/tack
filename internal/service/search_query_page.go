package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// summaryNameBytes bounds the name of one returned summary.
	summaryNameBytes = 256
	// resultOverheadBytes bounds the rendered bytes of one result beyond its
	// name and node type, including its node ID.
	resultOverheadBytes = 64
	// responseReserveBytes bounds the rendered header and continuation cursor.
	responseReserveBytes = 512
)

// pageState is the bounded progress of one public page.
type pageState struct {
	results   []node.Summary
	resultIDs []uuid.UUID
	visited   []uuid.UUID
	seen      map[uuid.UUID]bool
	bytes     int
	position  json.RawMessage
	full      bool
}

// ResultBytes is the rendered byte bound of one result.
func ResultBytes(summary node.Summary) int {
	return len(summary.Name) + len(summary.NodeType) + resultOverheadBytes
}

// advance reads at most MaxBatches raw batches after the session position,
// authorizes each new node from current FoundationDB data, and commits the
// consumed position before it returns.
func (s *SearchQueryService) advance(ctx context.Context, session searchdomain.Session, filter searchdomain.AccessFilter) (SearchPage, error) {
	page := pageState{
		results: nil, resultIDs: nil, visited: nil, seen: map[uuid.UUID]bool{},
		bytes: responseReserveBytes, position: session.Sort, full: false,
	}
	snapshot := session.Snapshot
	complete := false
	for batch := 0; batch < s.settings.MaxBatches && !page.full; batch++ {
		raw, err := s.ports.Ranker.Read(ctx, session.Query, snapshot, page.position)
		if err != nil {
			if errors.Is(err, searchdomain.ErrSnapshotLost) {
				s.closeSession(ctx, session)
			}
			return SearchPage{}, queryFailure(ctx, "read ranked batch", session.ID, err)
		}
		snapshot.PITID = raw.PITID
		if len(raw.Hits) == 0 {
			complete = true
			break
		}
		if err := s.consumeBatch(ctx, session, filter, raw.Hits, &page); err != nil {
			return SearchPage{}, err
		}
	}
	commit := searchdomain.PageCommit{
		Sort: page.position, PITID: snapshot.PITID, Visited: page.visited, ResultIDs: page.resultIDs, Complete: complete,
	}
	committed, err := s.ports.Sessions.CommitPage(ctx, session.ID, session.Version, commit)
	if errors.Is(err, searchdomain.ErrSessionChanged) {
		return s.replay(ctx, session, session.Version, filter)
	}
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "commit search page", session.ID, err)
	}
	if complete {
		if closeErr := s.ports.Ranker.Close(ctx, snapshot); closeErr != nil {
			telemetry.L(ctx).ErrorContext(ctx, "search.snapshot.close_failed", slog.String("err", closeErr.Error()), slog.String("session_id", session.ID.String()))
		}
	}
	telemetry.L(ctx).InfoContext(ctx, "search.page.returned", slog.String("session_id", session.ID.String()),
		slog.Uint64("version", committed.Version), slog.Int("results", len(page.results)), slog.Bool("complete", complete))
	return SearchPage{SessionID: session.ID, NextVersion: committed.Version, Results: page.results, Complete: complete}, nil
}

// consumeBatch loads one raw batch through one visited read and one summary
// operation. The first accepted node that admit cannot place stays
// unconsumed, and the page stops before it.
func (s *SearchQueryService) consumeBatch(ctx context.Context, session searchdomain.Session, filter searchdomain.AccessFilter, hits []searchdomain.RankHit, page *pageState) error {
	candidates := make([]uuid.UUID, 0, len(hits))
	for _, hit := range hits {
		if !page.seen[hit.NodeID] && !slices.Contains(candidates, hit.NodeID) {
			candidates = append(candidates, hit.NodeID)
		}
	}
	visited, err := s.ports.Sessions.HasVisited(ctx, session.ID, candidates)
	if err != nil {
		return queryFailure(ctx, "read visited nodes", session.ID, err)
	}
	fresh := make([]uuid.UUID, 0, len(candidates))
	for _, candidate := range candidates {
		if !visited[candidate] {
			fresh = append(fresh, candidate)
		}
	}
	summaries, err := s.ports.Summaries.Summaries(ctx, fresh, summaryNameBytes)
	if err != nil {
		return queryFailure(ctx, "read node summaries", session.ID, err)
	}
	byID := make(map[uuid.UUID]node.SummaryResult, len(summaries))
	for _, summary := range summaries {
		byID[summary.NodeID] = summary
	}
	for _, hit := range hits {
		if page.seen[hit.NodeID] || visited[hit.NodeID] {
			page.position = hit.Sort
			continue
		}
		result := byID[hit.NodeID]
		if accepted(result, filter, session.Query.NodeType) && !s.admit(ctx, session.ID, page, result.Summary) {
			page.full = true
			return nil
		}
		page.seen[hit.NodeID] = true
		page.visited = append(page.visited, hit.NodeID)
		page.position = hit.Sort
		if len(page.results) >= s.settings.MaxResults {
			page.full = true
			return nil
		}
	}
	return nil
}

// admit adds one accepted summary to the page and reports whether the node
// is consumed. It returns false when the result count or the remaining byte
// budget cannot take the summary; the next page starts with that node. A
// summary larger than the budget of an empty page fits on no page, so admit
// withholds it and returns true.
func (s *SearchQueryService) admit(ctx context.Context, sessionID uuid.UUID, page *pageState, summary node.Summary) bool {
	size := ResultBytes(summary)
	if responseReserveBytes+size > s.settings.MaxResponseBytes {
		telemetry.L(ctx).ErrorContext(ctx, "search.result.withheld", slog.String("session_id", sessionID.String()),
			slog.String("node_id", summary.ID.String()), slog.Int("bytes", size), slog.Int("max_response_bytes", s.settings.MaxResponseBytes))
		return true
	}
	if len(page.results) >= s.settings.MaxResults || page.bytes+size > s.settings.MaxResponseBytes {
		return false
	}
	page.results = append(page.results, summary)
	page.resultIDs = append(page.resultIDs, summary.ID)
	page.bytes += size
	return true
}

// accepted requires a current node, a current opaque key that the caller
// keys permit, and the requested node type.
func accepted(result node.SummaryResult, filter searchdomain.AccessFilter, nodeType string) bool {
	if result.Status != node.SummaryFound || !filter.Permits(result.AccessKeys) {
		return false
	}
	return nodeType == "" || result.Summary.NodeType == nodeType
}
