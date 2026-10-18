package foundationdb

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// addTargetContribution adds one record's contribution to target.Sum.
// It does not change the sum when target is nil.
func addTargetContribution(ctx context.Context, target *searchProjectionTarget, contribution []byte) error {
	if target == nil {
		return nil
	}
	sum, err := replaceContribution(ctx, target.Sum, nil, contribution)
	if err != nil {
		return err
	}
	target.Sum = sum
	return nil
}

// finishMetadataPass completes the metadata pass of state. If state has a
// projection target, it compares the organization's metadata epoch with
// the target's epoch. It restarts the metadata pass if the epoch changed.
// Otherwise, it activates the target. A renderer change selects content
// work for every node. After completing the metadata pass, it starts the
// node pass at Stop.
func finishMetadataPass(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, state *searchScanRecord) error {
	target := state.Projection
	if target != nil {
		epoch, err := readSearchCounter(ctx, tr, searchEpochKey(orgID))
		if err != nil {
			return err
		}
		if epoch != target.Epoch {
			*state = restartProjectionPass(*state, epoch)
			telemetry.L(ctx).InfoContext(ctx, "search.projection.recompute_restarted", slog.String("org_id", orgID.String()),
				slog.Int64("epoch", epoch))
			return nil
		}
		if err := activateProjection(ctx, tr, orgID, *target, state); err != nil {
			return err
		}
	}
	state.Phase, state.Cursor = scanNodes, state.Stop
	return nil
}

// activateProjection writes target as the organization's projection state.
// It records the current rescan generation as the activation number.
func activateProjection(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, target searchProjectionTarget, state *searchScanRecord) error {
	previous, _, err := readProjectionState(ctx, tr, orgID)
	if err != nil {
		return err
	}
	activation, err := readSearchCounter(ctx, tr, searchScanKey(orgID))
	if err != nil {
		return err
	}
	active := searchProjectionState{Renderer: target.Renderer, Format: target.Format, Digest: target.Sum, Activation: activation}
	if err := writeSearchRecord(ctx, tr, searchProjectionStateKey(orgID), active); err != nil {
		return err
	}
	state.Projection = nil
	if previous.Renderer != target.Renderer {
		*state = selectAllContent(*state)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.projection.activated", slog.String("org_id", orgID.String()),
		slog.Int("renderer", active.Renderer), slog.Int("format", active.Format), slog.Int64("activation", activation),
		slog.Int("previous_renderer", previous.Renderer))
	return nil
}

// projectionGate reports whether a claim must leave record pending.
// Live and copy work remain pending when this writer does not produce the
// organization's active projection. If this writer is newer, it requests
// activation unless a pending rescan already targets this projection or a
// newer one. A rescan that recomputes another writer's projection remains
// pending for that writer.
func projectionGate(ctx context.Context, tr fdb.Transaction, now time.Time, class searchdomain.WorkClass, record searchWorkRecord) (bool, error) {
	switch class {
	case searchdomain.WorkClassLive, searchdomain.WorkClassCopy:
		state, _, err := readProjectionState(ctx, tr, record.OrgID)
		if err != nil || state.writerProjection() {
			return false, err
		}
		if state.olderThanWriter() {
			return true, requestProjectionActivation(ctx, tr, now, record.OrgID)
		}
		return true, nil
	case searchdomain.WorkClassRescan:
		scan, found, err := readScanState(ctx, tr, record.OrgID)
		if err != nil || !found || scan.Projection == nil {
			return false, err
		}
		return !writerTarget(*scan.Projection), nil
	case searchdomain.WorkClassAccess, searchdomain.WorkClassCleanup, searchdomain.WorkClassRollout, searchdomain.WorkClassRebuild:
	}
	return false, nil
}

// requestProjectionActivation requests a rescan that activates this
// writer's projection unless a pending rescan already targets this writer's
// projection or a newer one.
func requestProjectionActivation(ctx context.Context, tr fdb.Transaction, now time.Time, orgID uuid.UUID) error {
	scan, found, err := readScanState(ctx, tr, orgID)
	if err != nil {
		return err
	}
	if found && scan.Projection != nil && !versionBefore(scan.Projection.Renderer, scan.Projection.Format) {
		return nil
	}
	telemetry.L(ctx).InfoContext(ctx, "search.projection.activation_requested", slog.String("org_id", orgID.String()),
		slog.Int("renderer", node.SearchRendererVersion), slog.Int("format", searchProjectionFormat))
	return requestSearchRescan(ctx, tr, now, orgID, searchRescanRequest{
		AllContent: false, Names: nil, TypeKeys: nil, Features: nil, Access: false, Projection: true,
	})
}

// writerTarget reports whether target uses this writer's renderer and format.
func writerTarget(target searchProjectionTarget) bool {
	return target.Renderer == node.SearchRendererVersion && target.Format == searchProjectionFormat
}

// restartObsoleteProjection schedules a new content revision for work's
// node when projection differs from the organization's active identity.
// It clears work's claim and reports whether it scheduled the revision.
func restartObsoleteProjection(ctx context.Context, tr fdb.Transaction, now time.Time, work searchdomain.Work, projection string) (bool, error) {
	err := requireActiveProjection(ctx, tr, work.OrgID, projection)
	if !errors.Is(err, node.ErrContentChanged) {
		return false, err
	}
	tr.Clear(fdb.Key(searchClaimKey(string(work.Class), searchBucket(work.OrgID, work.NodeID), work.OrgID, work.NodeID)))
	if _, err := scheduleSearchChange(ctx, tr, now, work.OrgID, work.NodeID, searchChangeContent); err != nil {
		return true, err
	}
	telemetry.L(ctx).InfoContext(ctx, "search.work.projection_changed", slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.Int64("generation", work.Generation))
	return true, nil
}
