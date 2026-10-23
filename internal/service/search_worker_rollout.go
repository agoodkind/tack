package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// rolloutSlice advances one bounded step of an authority's access policy
// rollout. A scan step schedules access work for at most 100 nodes. A
// verification step reads at most 100 exact page documents. No step reads
// page text or invokes the model.
func (w *SearchWorker) rolloutSlice(ctx context.Context, work searchdomain.Work) error {
	rollout, err := w.ports.Rollouts.Current(ctx, work.OrgID)
	if err != nil {
		return w.settle(ctx, work, "read access rollout", err)
	}
	switch {
	case rollout.Phase == searchdomain.AccessStable:
		return w.settle(ctx, work, "advance access rollout", errors.New("the authority has no rollout in progress"))
	case rollout.Phase == searchdomain.AccessBackfill,
		rollout.Phase == searchdomain.AccessRetiring && !rollout.ScanComplete:
		return w.rolloutScan(ctx, work, rollout)
	case rollout.Phase == searchdomain.AccessVerifying && rollout.Activated():
		return w.rolloutRetire(ctx, work, rollout)
	default:
		return w.rolloutVerify(ctx, work, rollout)
	}
}

// rolloutScan schedules access work for one page of the authority's nodes.
func (w *SearchWorker) rolloutScan(ctx context.Context, work searchdomain.Work, rollout searchdomain.AccessRollout) error {
	cursor := rollout.ScanCursor
	if rollout.Phase == searchdomain.AccessRetiring {
		cursor = rollout.RetireCursor
	}
	scan, err := w.ports.Reader.ScanSearch(ctx, work.OrgID, cursor, searchBatchLimit)
	if err != nil {
		return w.settle(ctx, work, "scan authority nodes for access rollout", err)
	}
	complete := func(ctx context.Context) error { return w.ports.Rollouts.CompleteScan(ctx, work, rollout, scan) }
	if scan.Done {
		complete = w.phaseStep(rollout, complete)
	}
	if err := complete(ctx); err != nil {
		return w.settle(ctx, work, "checkpoint access rollout scan", err)
	}
	return w.yield(ctx, work)
}

// rolloutRetire starts retirement once no session reads the previous
// version. Until then the rollout waits.
func (w *SearchWorker) rolloutRetire(ctx context.Context, work searchdomain.Work, rollout searchdomain.AccessRollout) error {
	active, err := w.ports.Sessions.HasActiveAccessVersion(ctx, work.OrgID, rollout.PreviousVersion)
	if err != nil {
		return w.settle(ctx, work, "read previous access version sessions", err)
	}
	if active {
		return w.waitRollout(ctx, work)
	}
	retire := w.phaseStep(rollout, func(ctx context.Context) error { return w.ports.Rollouts.BeginRetire(ctx, work, rollout) })
	if err := retire(ctx); err != nil {
		return w.settle(ctx, work, "begin access version retirement", err)
	}
	return w.yield(ctx, work)
}

// rolloutVerify reads one batch of exact page documents and checkpoints the
// verification.
func (w *SearchWorker) rolloutVerify(ctx context.Context, work searchdomain.Work, rollout searchdomain.AccessRollout) error {
	documents, done, err := w.ports.Rollouts.VerifyDocuments(ctx, work, rollout, searchBatchLimit)
	if err != nil {
		return w.settle(ctx, work, "read access rollout documents", err)
	}
	ids := make([]string, 0, len(documents))
	for _, document := range documents {
		ids = append(ids, document.Document.DocumentID)
	}
	operationContext, cancel := context.WithTimeout(ctx, w.settings.OperationTimeout)
	states, err := w.ports.Documents.DocumentAccess(operationContext, work.Target, ids)
	cancel()
	if err != nil {
		return w.settleEngine(ctx, work, "read access rollout page documents", err)
	}
	var step searchdomain.RolloutStep
	verify := func(ctx context.Context) error {
		var verifyErr error
		step, verifyErr = w.ports.Rollouts.CompleteVerify(ctx, work, rollout, documents, states, done)
		if verifyErr != nil {
			return structureFailure(ctx, "checkpoint the access rollout verification", verifyErr)
		}
		return nil
	}
	if done {
		verify = w.phaseStep(rollout, verify)
	}
	if err := verify(ctx); err != nil {
		return w.settle(ctx, work, "checkpoint access rollout verification", err)
	}
	switch step {
	case searchdomain.RolloutWait:
		return w.waitRollout(ctx, work)
	case searchdomain.RolloutComplete:
		telemetry.L(ctx).InfoContext(ctx, "search.rollout.completed", slog.String("authority_id", work.OrgID.String()),
			slog.String("active_version", rollout.ActiveVersion))
		return nil
	case searchdomain.RolloutContinue:
	}
	return w.yield(ctx, work)
}

func (w *SearchWorker) phaseStep(rollout searchdomain.AccessRollout, effect func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error { return w.recordRolloutPhase(ctx, rollout, effect) }
}

// waitRollout delays the next claim of the rollout without recording a
// failure.
func (w *SearchWorker) waitRollout(ctx context.Context, work searchdomain.Work) error {
	err := w.ports.Rollouts.Wait(ctx, work)
	if err == nil || errors.Is(err, searchdomain.ErrWorkChanged) {
		return nil
	}
	wrapped := fmt.Errorf("delay access rollout of authority %s: %w", work.OrgID, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.worker.rollout_wait_failed", slog.String("err", wrapped.Error()),
		slog.String("authority_id", work.OrgID.String()))
	return loggedWorkerError{err: wrapped}
}
