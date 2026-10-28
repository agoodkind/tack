package datagen

import (
	"context"
	"errors"
	"log/slog"
	"slices"
)

var errSearchMixedDeletedReturned = errors.New("qa datagen search-mixed: search returned a deleted issue after the serving index stopped storing its active pages")

func (m *searchMixed) probeDeletions(ctx context.Context, tick int) {
	m.probeMutex.Lock()
	pending := slices.Clone(m.deletions)
	m.probeMutex.Unlock()
	verified := make(map[*searchMixedNode]bool)
	for _, entry := range pending {
		texts, err := m.pages.SearchPageText(ctx, entry.node.id)
		if err != nil {
			m.stats.count(&m.stats.probeErrors)
			continue
		}
		if len(texts) > 0 {
			continue
		}
		tenant := m.tenants[entry.node.tenant]
		page, err := callSearch(ctx, m.driver(tick+entry.node.ordinal), m.titles, tenant.token(), tenant.entry(), entry.marker, "")
		if err != nil {
			m.stats.count(&m.stats.probeErrors)
			continue
		}
		if slices.Contains(page.IDs, entry.node.id) {
			slog.ErrorContext(ctx, "qa.datagen.search_mixed_deleted_node_returned",
				slog.String("err", errSearchMixedDeletedReturned.Error()),
				slog.String("node_id", entry.node.id.String()), slog.String("marker", entry.marker))
			m.stats.count(&m.stats.deletedReturned)
		}
		verified[entry.node] = true
	}
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.deletions = slices.DeleteFunc(m.deletions, func(entry searchMixedDeletion) bool { return verified[entry.node] })
}
