package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// verifyReportPhase reads the report comment back, verifies its content and
// attachment, and records the content digest in the comment_verified
// checkpoint.
func (runner repairPhaseRunner) verifyReportPhase(ctx context.Context, run repairPhaseRun) error {
	frozen, err := frozenRepairSource(ctx, run.entry)
	if err != nil {
		return err
	}

	_, digest, err := runner.readReportComment(ctx, run, frozen, node.RepairPhaseCommentCreated)
	if err != nil {
		return err
	}

	checkpoint := run.checkpoint(node.RepairPhaseCommentVerified)
	checkpoint.Next.ContentDigest = digest
	if err := runner.checkpoints.AdvanceRepairCheckpoint(
		ctx,
		checkpoint.Previous,
		checkpoint.Next,
	); err != nil {
		return repairMutationFailure(ctx, run.entry, "record the verified report comment", err)
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"repair.report.verified",
		slog.String("repair_id", run.entry.RepairID),
		slog.String("comment_id", run.stored.CommentID.String()),
		slog.String("content_digest", digest),
	)

	return nil
}

// verifiedReportRecord verifies the report comment again before the result
// removes the source property. It returns the comment primary digest, which
// the result transaction requires to stay unchanged.
func (runner repairPhaseRunner) verifiedReportRecord(
	ctx context.Context,
	run repairPhaseRun,
	frozen repairFrozen,
) (node.GraphRecord, error) {
	snapshot, digest, err := runner.readReportComment(
		ctx,
		run,
		frozen,
		node.RepairPhaseCommentVerified,
	)
	if err != nil {
		return node.GraphRecord{}, err
	}

	if digest != run.stored.ContentDigest {
		return node.GraphRecord{}, repairRefused(
			ctx,
			run.entry,
			"the report comment changed after its verification",
			domain.ErrConflict,
		)
	}

	return node.GraphRecord{
		NodeID:        run.stored.CommentID,
		PrimaryDigest: snapshot.PrimaryDigest,
	}, nil
}

// readReportComment reads the comment that the stored checkpoint records at
// phase. It requires the comment type, name, report props, parent_id, and
// child_of edge to match the manifest and returns the content digest.
func (runner repairPhaseRunner) readReportComment(
	ctx context.Context,
	run repairPhaseRun,
	frozen repairFrozen,
	phase node.RepairPhase,
) (node.MutationSnapshot, string, error) {
	report := *run.entry.Report
	if run.stored == nil || run.stored.Phase != phase || run.stored.CommentID == uuid.Nil {
		return node.MutationSnapshot{}, "", repairRefused(
			ctx,
			run.entry,
			"the checkpoint does not record a report comment",
			domain.ErrFailedPrecondition,
		)
	}

	commentID := run.stored.CommentID
	snapshot, _, err := readNodeSnapshot(ctx, runner.mutator, run.entry, frozen.orgID, commentID)
	if err != nil {
		return snapshot, "", err
	}

	if !snapshot.Exists || snapshot.Node.NodeType != report.NodeType ||
		snapshot.ParentID != run.entry.NodeID {
		return snapshot, "", repairRefused(
			ctx,
			run.entry,
			fmt.Sprintf(
				"report comment %s is absent, has another type, or has another parent",
				commentID,
			),
			domain.ErrConflict,
		)
	}

	attached := false

	for _, edge := range snapshot.Outgoing {
		attached = attached ||
			(edge.Relationship.RelationType == node.RelChildOf && edge.Relationship.TargetID == run.entry.NodeID)
	}

	if !attached {
		return snapshot, "", repairRefused(
			ctx,
			run.entry,
			fmt.Sprintf("report comment %s does not have a child_of edge to the node", commentID),
			domain.ErrConflict,
		)
	}

	digest, err := reportContentDigest(
		ctx,
		report,
		snapshot.Node.NodeType,
		snapshot.Node.Name,
		snapshot.Node.Props,
	)
	if err != nil {
		return snapshot, "", err
	}

	planned, err := reportContentDigest(ctx, report, report.NodeType, report.Name, report.Props)
	if err != nil {
		return snapshot, "", err
	}

	if digest != planned {
		return snapshot, "", repairRefused(
			ctx,
			run.entry,
			fmt.Sprintf("report comment %s content differs from the manifest", commentID),
			domain.ErrConflict,
		)
	}

	return snapshot, digest, nil
}
