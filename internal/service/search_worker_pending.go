package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// pageWriteOperation is the operation name of a failed page write.
const pageWriteOperation = "write search page"

// settleAbandonedWrite ends a slice after a page write times out at
// OperationTimeout. It releases the work with an uncounted failure and
// RetryAfter: 0. It uses the failed physical index from PageWriteError.
// When intent.Pending already records that index, the deadline stays
// unchanged. Otherwise, Release stores a pending write in its release
// transaction with a deadline of abandon time plus WriteCompletionHorizon.
// The store applies its minimum retry delay.
func (w *SearchWorker) settleAbandonedWrite(ctx context.Context, work searchdomain.Work, intent searchdomain.WriteIntent, err error) error {
	index := work.Target
	var pageErr *searchdomain.PageWriteError
	if errors.As(err, &pageErr) {
		index = pageErr.Index
	}
	failure := searchdomain.Failure{Message: "", Counted: false, RetryAfter: 0, Pending: nil}
	if intent.Pending == nil || intent.Pending.Index != index {
		abandonedAt := w.clock.Now()
		failure.Pending = &searchdomain.PendingWrite{
			DocumentID: intent.DocumentID, Index: index, Ordinal: intent.Page.Ordinal, Generation: work.Generation,
			AbandonedAt: abandonedAt, Deadline: abandonedAt.Add(w.settings.WriteCompletionHorizon),
		}
	}
	return w.fail(ctx, work, pageWriteOperation, err, failure)
}

// awaitPendingWrite logs a pending page's absence at Info and releases the
// work with an uncounted failure and RetryAfter: 0. The store applies its
// minimum retry delay. A successful release or changed claim ends the slice
// with nil.
func (w *SearchWorker) awaitPendingWrite(ctx context.Context, work searchdomain.Work, intent searchdomain.WriteIntent) error {
	pending := intent.Pending
	telemetry.L(ctx).InfoContext(ctx, "search.worker.pending_write_waiting", slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.Int64("generation", work.Generation),
		slog.Int64("abandoned_generation", pending.Generation), slog.String("document_id", pending.DocumentID),
		slog.String("index", pending.Index), slog.Uint64("ordinal", pending.Ordinal),
		slog.Duration("since_abandoned", w.clock.Since(pending.AbandonedAt)),
		slog.Duration("until_deadline", pending.Deadline.Sub(w.clock.Now())))
	failure := searchdomain.Failure{
		Message: "wait for the pending write of search page " + pending.DocumentID + " in " + pending.Index,
		Counted: false, RetryAfter: 0, Pending: nil,
	}
	err := w.ports.Store.Release(ctx, work, failure)
	if err == nil || errors.Is(err, searchdomain.ErrWorkChanged) {
		return nil
	}
	wrapped := fmt.Errorf("release search work of node %s to wait for a pending page write: %w", work.NodeID, err)
	slog.ErrorContext(ctx, "search.worker.release_failed", slog.String("err", wrapped.Error()),
		slog.String("node_id", work.NodeID.String()), slog.String("class", string(work.Class)))
	return loggedWorkerError{err: wrapped}
}

// logStoredPendingWrite logs since_abandoned at Info after Put succeeds with
// intent.Pending set. The value measures the time from abandonment until
// this claim finds the page stored. Operators can use these observations
// to choose WriteCompletionHorizon.
func (w *SearchWorker) logStoredPendingWrite(ctx context.Context, work searchdomain.Work, intent searchdomain.WriteIntent) {
	pending := intent.Pending
	if pending == nil {
		return
	}
	telemetry.L(ctx).InfoContext(ctx, "search.worker.pending_write_stored", slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.Int64("generation", work.Generation),
		slog.Int64("abandoned_generation", pending.Generation), slog.String("document_id", pending.DocumentID),
		slog.String("index", pending.Index), slog.Uint64("ordinal", pending.Ordinal),
		slog.Duration("since_abandoned", w.clock.Since(pending.AbandonedAt)))
}

// mirrorMiss returns a PendingWrite for a failed mirror write after the serving index
// accepts the page. mirrorMiss returns nil for ErrObsoleteWrite, an error from another
// index, or a claim without a mirror.
//
// abandoned reports that a deadline or cancellation ended the operation context before
// the mirror request finished. OpenSearch can still complete that request.
// When abandoned is true, mirrorMiss sets Deadline to now plus WriteCompletionHorizon.
// Otherwise, the engine returned a definite error and mirrorMiss sets Deadline to now.
//
// mirrorMiss logs one Info event when it returns a PendingWrite.
func (w *SearchWorker) mirrorMiss(ctx context.Context, intent searchdomain.WriteIntent, err error, abandoned bool) *searchdomain.PendingWrite {
	work := intent.Work
	var pageErr *searchdomain.PageWriteError
	if work.Mirror == "" || !errors.As(err, &pageErr) || pageErr.Index != work.Mirror || errors.Is(err, searchdomain.ErrObsoleteWrite) {
		return nil
	}
	now := w.clock.Now()
	deadline := now
	if abandoned {
		deadline = now.Add(w.settings.WriteCompletionHorizon)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.worker.mirror_missed", slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.Int64("generation", work.Generation),
		slog.String("document_id", intent.DocumentID), slog.String("index", work.Mirror),
		slog.Uint64("ordinal", intent.Page.Ordinal), slog.Bool("abandoned", abandoned))
	return &searchdomain.PendingWrite{
		DocumentID: intent.DocumentID, Index: work.Mirror, Ordinal: intent.Page.Ordinal,
		Generation: work.Generation, AbandonedAt: now, Deadline: deadline,
	}
}

// keepMirrorMiss returns immediately when intent.MirrorMissed is nil.
// Otherwise, keepMirrorMiss calls RecordMirrorMiss with a context that preserves
// the values of ctx and ignores its cancellation and deadline. The context expires
// after the worker OperationTimeout. RecordMirrorMiss runs even when the slice
// context has expired or the worker has stopped.
//
// RecordMirrorMiss does not store a record when the node revision has changed,
// live work is no longer pending, or the replacement no longer uses the missed index.
// keepMirrorMiss logs a RecordMirrorMiss failure at Error level and does not return
// a value. The caller settles the CompletePage failure afterward.
func (w *SearchWorker) keepMirrorMiss(ctx context.Context, intent searchdomain.WriteIntent) {
	if intent.MirrorMissed == nil {
		return
	}
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.settings.OperationTimeout)
	defer cancel()
	if err := w.ports.Store.RecordMirrorMiss(recordContext, intent); err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.worker.mirror_miss_record_failed", slog.String("err", err.Error()),
			slog.String("node_id", intent.Work.NodeID.String()), slog.String("document_id", intent.DocumentID))
	}
}
