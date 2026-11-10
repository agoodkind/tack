package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// pageBatchSize bounds each FDB range read so one transaction stays well
// under the 5 second and 10 MB limits.
const pageBatchSize = 256

type pageEntry struct {
	id   uuid.UUID
	view *node.NodeView
}

// ListPage reads batches in key order until it has q.Limit matching views
// plus one more, which proves another page exists.
func (s *ViewStore) ListPage(ctx context.Context, q node.NodeListQuery) (page node.Page, err error) {
	defer telemetry.FDBOp(ctx, "store.view.list_page")(&err)
	if q.Limit <= 0 {
		slog.ErrorContext(ctx, "store.view.list_page_failed", slog.String("err", "limit must be positive"))
		return node.Page{}, fmt.Errorf("list page %s: limit must be positive: %w", q.NodeType, domain.ErrInvalidArgument)
	}
	if q.BySourceRelation != nil || q.ByTargetRelation != nil {
		slog.ErrorContext(ctx, "store.view.list_page_failed", slog.String("err", "relation scans are not paged"))
		return node.Page{}, fmt.Errorf("list page %s: relation scans are not paged: %w", q.NodeType, domain.ErrInvalidArgument)
	}
	prefix := s.pagePrefix(q)
	begin, err := s.pageBegin(ctx, q, prefix)
	if err != nil {
		return node.Page{}, err
	}
	prefixRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		slog.ErrorContext(ctx, "store.view.list_page_failed", slog.String("err", err.Error()))
		return node.Page{}, fmt.Errorf("list page %s: %w", q.NodeType, err)
	}
	end := fdb.FirstGreaterOrEqual(prefixRange.End)
	matched := make([]*node.NodeView, 0, q.Limit+1)
	for len(matched) <= q.Limit {
		entries, lastKey, readErr := s.readPageBatch(ctx, q, fdb.SelectorRange{Begin: begin, End: end})
		if readErr != nil {
			return node.Page{}, readErr
		}
		for _, entry := range entries {
			if entry.view != nil && matchesPageFilters(entry.view, q) {
				matched = append(matched, entry.view)
			}
			if len(matched) > q.Limit {
				break
			}
		}
		if len(entries) < pageBatchSize {
			break
		}
		begin = fdb.FirstGreaterThan(lastKey)
	}
	if len(matched) <= q.Limit {
		return node.Page{Views: matched, NextCursor: ""}, nil
	}
	views := matched[:q.Limit]
	return node.Page{Views: views, NextCursor: node.EncodeCursor(views[len(views)-1].ID)}, nil
}

func (s *ViewStore) pagePrefix(q node.NodeListQuery) []byte {
	if q.ByProperty != nil {
		return nodeByPropertyValuePrefix(q.OrgID, q.NodeType, q.ByProperty.PropName, encodePropertyValue(q.ByProperty.Value))
	}
	return nodeViewPrefix(q.OrgID, q.NodeType)
}

func (s *ViewStore) pageBegin(ctx context.Context, q node.NodeListQuery, prefix []byte) (fdb.KeySelector, error) {
	if q.Cursor == "" {
		return fdb.FirstGreaterOrEqual(fdb.Key(prefix)), nil
	}
	lastID, err := node.DecodeCursor(q.Cursor)
	if err != nil {
		slog.ErrorContext(ctx, "store.view.list_page_failed", slog.String("err", err.Error()))
		return fdb.KeySelector{}, fmt.Errorf("list page %s: %w", q.NodeType, err)
	}
	if q.ByProperty != nil {
		key := nodeByPropertyKey(q.OrgID, q.NodeType, q.ByProperty.PropName, encodePropertyValue(q.ByProperty.Value), lastID)
		return fdb.FirstGreaterThan(fdb.Key(key)), nil
	}
	return fdb.FirstGreaterThan(fdb.Key(nodeViewKey(q.OrgID, q.NodeType, lastID))), nil
}

func matchesPageFilters(view *node.NodeView, q node.NodeListQuery) bool {
	if !matchPropFilters(view, q.PropFilters) {
		return false
	}
	if q.CreatedAfter != nil && view.CreatedAt.Before(*q.CreatedAfter) {
		return false
	}
	if q.CreatedBefore != nil && view.CreatedAt.After(*q.CreatedBefore) {
		return false
	}
	return true
}
