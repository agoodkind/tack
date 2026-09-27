package service

import (
	"context"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rescanSlice processes one bounded step of an organization rescan. The
// metadata phase indexes at most 100 property definitions or node types.
// The node phase schedules the requested work for at most 100 nodes and
// checkpoints the scan cursor.
func (w *SearchWorker) rescanSlice(ctx context.Context, work searchdomain.Work) error {
	if work.Phase == searchdomain.PhaseMetadata {
		if err := w.ports.Store.IndexMetadata(ctx, work); err != nil {
			return w.settle(ctx, work, "index organization metadata for search", err)
		}
		return w.yield(ctx, work)
	}
	scan, err := w.ports.Reader.ScanSearch(ctx, work.OrgID, work.Cursor, searchBatchLimit)
	if err != nil {
		return w.settle(ctx, work, "scan organization nodes for search", err)
	}
	if err := w.ports.Store.CompleteRescan(ctx, work, scan); err != nil {
		return w.settle(ctx, work, "schedule rescanned search work", err)
	}
	return w.yield(ctx, work)
}
