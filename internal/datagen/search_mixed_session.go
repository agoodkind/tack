package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
)

// searchMixedSession represents a public search session for one tenant actor.
// The run requests page p with the driver at offset+p.
type searchMixedSession struct {
	tenant int
	token  string
	entry  string
	query  string
	offset int
}

// searchMixedTally counts the pages and violations of one session check.
type searchMixedTally struct {
	pages, replays, continuationErrors, replayMismatches, crossTenant, deleted int
}

func (m *searchMixed) sessionFor(tenant, actor int, query string, offset int) searchMixedSession {
	workspace := m.tenants[tenant].workspace
	return searchMixedSession{tenant: tenant, token: workspace.Actors[actor].Token, entry: workspace.Slug, query: query, offset: offset}
}

func (m *searchMixed) page(ctx context.Context, session searchMixedSession, number int, cursor string, tally *searchMixedTally) (searchPage, error) {
	requested := clock.Now()
	page, err := callSearch(ctx, m.driver(session.offset+number), session.token, session.entry, session.query, cursor)
	if err != nil {
		return searchPage{}, err
	}
	tally.pages++
	if len(page.IDs) > m.limits.MaxResults || page.ResponseBytes > m.limits.MaxResponseBytes {
		tally.continuationErrors++
	}
	return page, m.inspect(ctx, session.tenant, page.IDs, requested, tally)
}

// inspect counts issues that this run deleted before the search request started and issues
// from another organization.
func (m *searchMixed) inspect(ctx context.Context, tenant int, nodeIDs []uuid.UUID, requested time.Time, tally *searchMixedTally) error {
	orgID := m.tenants[tenant].workspace.OrgID
	for _, nodeID := range nodeIDs {
		if m.deletedBefore(nodeID, requested) {
			tally.deleted++
			continue
		}
		resolved, err := m.stores.Views.Resolve(ctx, nodeID)
		if errors.Is(err, domain.ErrNotFound) {
			// This run can delete an issue after the server reads it but before this check resolves it.
			if !m.deletedBefore(nodeID, clock.Now()) {
				tally.deleted++
			}
			continue
		}
		if err != nil {
			return loggedError(ctx, "qa datagen search-mixed: the command could not resolve search result "+nodeID.String(), err)
		}
		if resolved.OrgID != orgID {
			tally.crossTenant++
		}
	}
	return nil
}

// traverse requests the remaining session pages, starting with cursor.
// It replays each continuation page with the next driver and returns issues in response order.
func (m *searchMixed) traverse(ctx context.Context, session searchMixedSession, first int, cursor string, tally *searchMixedTally) ([]uuid.UUID, error) {
	seen := make(map[uuid.UUID]struct{})
	cursors := make(map[string]struct{})
	ordered := make([]uuid.UUID, 0)
	for number := first; number < first+maxSearchTraversalPages; number++ {
		page, err := m.page(ctx, session, number, cursor, tally)
		if err != nil {
			return ordered, err
		}
		if cursor != "" {
			if err := m.replay(ctx, session, number+1, cursor, page, tally); err != nil {
				return ordered, err
			}
		}
		for _, nodeID := range page.IDs {
			if _, duplicate := seen[nodeID]; duplicate {
				tally.continuationErrors++
			}
			seen[nodeID] = struct{}{}
			ordered = append(ordered, nodeID)
		}
		if page.Cursor == "" {
			return ordered, nil
		}
		if _, repeated := cursors[page.Cursor]; repeated {
			tally.continuationErrors++
			return ordered, nil
		}
		cursors[page.Cursor], cursor = struct{}{}, page.Cursor
	}
	tally.continuationErrors++
	return ordered, nil
}

// replay requests the page for cursor again and checks the issue order and next cursor.
// It allows the replay to omit issues that this run deleted before the replay finished.
func (m *searchMixed) replay(ctx context.Context, session searchMixedSession, number int, cursor string, original searchPage, tally *searchMixedTally) error {
	replayed, err := m.page(ctx, session, number, cursor, tally)
	if err != nil {
		return err
	}
	tally.replays++
	finished := clock.Now()
	expected := slices.DeleteFunc(slices.Clone(original.IDs), func(nodeID uuid.UUID) bool {
		return m.deletedBefore(nodeID, finished) && !slices.Contains(replayed.IDs, nodeID)
	})
	if !slices.Equal(expected, replayed.IDs) || replayed.Cursor != original.Cursor {
		tally.replayMismatches++
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_replay_mismatch", slog.String("err", fmt.Sprintf("search replay returned issues %v instead of the expected issues %v", replayed.IDs, expected)))
	}
	return nil
}

// checkSessions reads every page of each tenant's read word session and checks that the
// session returns every read issue for that tenant.
func (m *searchMixed) checkSessions(ctx context.Context) {
	expected := [][]uuid.UUID{m.corpus.read, m.corpus.foreignRead}
	for tenant, nodes := range expected {
		tally := searchMixedTally{}
		returned, err := m.traverse(ctx, m.sessionFor(tenant, 0, m.readWord, tenant), 0, "", &tally)
		if err != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_mixed_session_failed", slog.String("err", err.Error()))
			tally.continuationErrors++
		}
		for _, nodeID := range nodes {
			if !slices.Contains(returned, nodeID) {
				tally.continuationErrors++
			}
		}
		m.recordSessions(tally)
	}
}

func (m *searchMixed) recordSessions(tally searchMixedTally) {
	m.phases.update(func(phases *searchMixedPhaseStats) {
		sessions := &phases.sessions
		sessions.Checks++
		sessions.Endpoints = len(m.drivers)
		sessions.Pages += tally.pages
		sessions.Replays += tally.replays
		sessions.ContinuationErrors += tally.continuationErrors
		sessions.ReplayMismatches += tally.replayMismatches
		sessions.CrossTenantResults += tally.crossTenant
		sessions.DeletedNodesReturned += tally.deleted
	})
}
