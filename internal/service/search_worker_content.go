package service

import (
	"context"
	"errors"
	"log/slog"

	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// A missed mirror write must not delay the serving cursor.
// CompletePage commits the serving checkpoint and owed copy work together.
// Claim verification needs the original Mirror. Clear it only in the request copy.
// After a new mirror failure, later pages in the slice skip the mirror because
// the queued copy includes the missed page and all later pages.
// Cancellation or claim loss can prevent checkpointing after the serving write.
// Preserve the original mirror deadline before settling that failure.
func (w *SearchWorker) contentSlice(ctx context.Context, work searchdomain.Work, budget *sliceBudget) error {
	mirrorSkipped := false
	for work.Phase != searchdomain.PhaseRefresh {
		if budget.spent() {
			return w.yield(ctx, work)
		}
		page, err := w.ports.Reader.Content(ctx, searchdomain.ContentRequest{
			NodeID: work.NodeID, Cursor: work.Cursor, ProjectionConfig: work.Projection,
			AccessVersions: nil, MaxBytes: w.settings.PageBytes, SearchGeneration: work.Generation,
		})
		if errors.Is(err, node.ErrContentChanged) {
			return w.restart(ctx, work)
		}
		if err != nil {
			return w.settle(ctx, work, "read search page", err)
		}
		intent, err := w.ports.Store.Register(ctx, work, page)
		if errors.Is(err, node.ErrContentChanged) {
			return w.restart(ctx, work)
		}
		if err != nil {
			return w.settle(ctx, work, "register search page", err)
		}
		putIntent := intent
		if mirrorSkipped || intent.MirrorMissed != nil {
			putIntent.Work.Mirror = ""
		}
		operationContext, cancel := context.WithTimeout(ctx, w.settings.OperationTimeout)
		written, err := w.ports.Writer.Put(operationContext, putIntent)
		timedOut := errors.Is(operationContext.Err(), context.DeadlineExceeded)
		abandoned := operationContext.Err() != nil
		cancel()
		budget.record(1, written)
		if missed := w.mirrorMiss(ctx, intent, err, abandoned); missed != nil {
			intent.MirrorMissed, err, mirrorSkipped = missed, nil, true
		}
		if errors.Is(err, searchdomain.ErrPageWritePending) {
			return w.awaitPendingWrite(ctx, work, intent)
		}
		if err != nil && timedOut {
			return w.settleAbandonedWrite(ctx, work, intent, err)
		}
		if err != nil {
			return w.settleEngine(ctx, work, pageWriteOperation, err)
		}
		w.logStoredPendingWrite(ctx, work, intent)
		if err := w.ports.Store.CompletePage(ctx, intent, page.NextCursor, page.Done); err != nil {
			w.keepMirrorMiss(ctx, intent)
			return w.settle(ctx, work, "checkpoint search page", err)
		}
		work.Cursor = page.NextCursor
		work.Ordinal = page.Ordinal + 1
		work.Projection = page.ProjectionVersion
		if page.Done {
			work.Phase = searchdomain.PhaseRefresh
		}
	}
	if budget.spent() {
		return w.yield(ctx, work)
	}
	operationContext, cancel := context.WithTimeout(ctx, w.settings.OperationTimeout)
	err := w.ports.Writer.Refresh(operationContext, work.Target)
	cancel()
	if err != nil {
		return w.settleEngine(ctx, work, "refresh search index", err)
	}
	if err := w.ports.Store.CompleteRefresh(ctx, work); err != nil {
		return w.settle(ctx, work, "complete refreshed search work", err)
	}
	return nil
}

// restart schedules a new content revision after the projected text changed
// under the claim's cursor, or after an earlier owner registered the same
// page of this revision under another projection.
func (w *SearchWorker) restart(ctx context.Context, work searchdomain.Work) error {
	if err := w.ports.Store.Restart(ctx, work); err != nil && !errors.Is(err, searchdomain.ErrWorkChanged) {
		return w.settle(ctx, work, "restart changed search content", err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.worker.content_restarted", slog.String("node_id", work.NodeID.String()),
		slog.Int64("generation", work.Generation))
	return nil
}
