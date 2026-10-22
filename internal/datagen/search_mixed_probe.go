package datagen

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

var errSearchMixedDeletedReturned = errors.New("qa datagen search-mixed: search returned a deleted issue after indexing processed the deletion")

func (m *searchMixed) awaitMarker(ctx context.Context, node *searchMixedNode, marker string, committed time.Time) {
	for {
		page, err := callSearch(ctx, m.load.driver, m.load.token, m.load.entry, marker, "")
		if err == nil && slices.Contains(page.IDs, node.id) {
			m.stats.markerFound(clock.Now().Sub(committed))
			m.settle(node, marker)
			return
		}
		if !waitForSearchMixedPoll(ctx) {
			m.stats.count(&m.stats.markersNotFound)
			return
		}
	}
}

func (m *searchMixed) awaitDeletion(ctx context.Context, nodeID uuid.UUID, marker string) {
	for {
		texts, readErr := m.pages.SearchPageText(ctx, nodeID)
		processed := readErr == nil && len(texts) == 0
		page, searchErr := callSearch(ctx, m.load.driver, m.load.token, m.load.entry, marker, "")
		if processed && searchErr == nil {
			if slices.Contains(page.IDs, nodeID) {
				slog.ErrorContext(ctx, "qa.datagen.search_mixed_deleted_node_returned",
					slog.String("err", errSearchMixedDeletedReturned.Error()),
					slog.String("node_id", nodeID.String()), slog.String("marker", marker))
				m.stats.count(&m.stats.deletedReturned)
			}
			return
		}
		if !waitForSearchMixedPoll(ctx) {
			m.stats.count(&m.stats.deletesUnverified)
			return
		}
	}
}
