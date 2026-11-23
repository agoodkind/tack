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

// repairScopeProperty identifies the property for the ancestor that has
// the scope.
const repairScopeProperty = "scope_id"

// repairReportIdempotencyKey returns the idempotency key for creating the
// report comment of repairID. A retry with this key returns the existing
// comment and does not create a second comment.
func repairReportIdempotencyKey(repairID string) string {
	return "repair:" + repairID + ":report"
}

// createReportPhase creates the report comment under the repaired node
// through [node.Mutator.ApplyMutation]. The same transaction records the
// comment ID in the comment_created checkpoint.
func (runner repairPhaseRunner) createReportPhase(ctx context.Context, run repairPhaseRun) error {
	report := *run.entry.Report
	frozen, err := frozenRepairSource(ctx, run.entry)
	if err != nil {
		return err
	}

	snapshot, schema, err := readRepairSnapshot(ctx, runner.mutator, run.entry, frozen)
	if err != nil {
		return err
	}

	if err := reportPreservesSource(ctx, run.entry); err != nil {
		return err
	}

	commentID, err := uuid.NewV7()
	if err != nil {
		return repairMutationFailure(ctx, run.entry, "create the report comment ID", err)
	}

	plan, err := runner.reportCommentPlan(ctx, run, frozen, snapshot, commentID)
	if err != nil {
		return err
	}

	references, err := validateRepairResult(
		ctx,
		runner.mutator,
		run.entry,
		schema,
		report.NodeType,
		repairResultProps(nil, plan.Props),
		[]uuid.UUID{run.entry.NodeID},
	)
	if err != nil {
		return err
	}

	plan.Graph.Records = mergeGraphRecords(plan.Graph.Records, references)
	staged, err := stageRepairEvent(ctx, run, node.RepairPhaseCommentCreated)
	if err != nil {
		return err
	}

	receipt, err := runner.mutator.ApplyMutation(staged, plan)
	if err != nil {
		return repairMutationFailure(ctx, run.entry, "create the report comment", err)
	}

	if receipt.NodeID != commentID {
		return repairRefused(
			ctx,
			run.entry,
			fmt.Sprintf(
				"the create returned comment %s, not the checkpoint comment %s",
				receipt.NodeID,
				commentID,
			),
			domain.ErrConflict,
		)
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"repair.report.created",
		slog.String("repair_id", run.entry.RepairID),
		slog.String("node_id", run.entry.NodeID.String()),
		slog.String("comment_id", commentID.String()),
	)

	return nil
}
