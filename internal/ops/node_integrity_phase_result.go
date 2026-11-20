package ops

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// resultPhase validates the complete result of an entry and calls
// [node.Mutator.ApplyMutation] to apply it. It completes the checkpoint in
// the same transaction. For an entry with a report, resultPhase first
// verifies the report comment again and requires the comment to remain
// unchanged during the transaction.
func (runner repairPhaseRunner) resultPhase(ctx context.Context, run repairPhaseRun) error {
	frozen, err := frozenRepairSource(ctx, run.entry)
	if err != nil {
		return err
	}

	snapshot, schema, err := readRepairSnapshot(ctx, runner.mutator, run.entry, frozen)
	if err != nil {
		return err
	}

	plan, err := repairResultPlan(ctx, run, frozen, snapshot)
	if err != nil {
		return err
	}

	references, err := validateRepairResult(
		ctx,
		runner.mutator,
		run.entry,
		schema,
		run.entry.NodeType,
		repairResultProps(snapshot.Node.Props, plan.Props),
		resultChildOf(snapshot, plan.Parent),
	)
	if err != nil {
		return err
	}

	plan.Graph.Records = mergeGraphRecords(plan.Graph.Records, references)
	if run.entry.Report != nil {
		record, err := runner.verifiedReportRecord(ctx, run, frozen)
		if err != nil {
			return err
		}

		plan.Graph.Records = mergeGraphRecords(plan.Graph.Records, []node.GraphRecord{record})
	}

	staged, err := stageRepairEvent(ctx, run, node.RepairPhaseCompleted)
	if err != nil {
		return err
	}

	receipt, err := runner.mutator.ApplyMutation(staged, plan)
	if err != nil {
		return repairMutationFailure(ctx, run.entry, "apply the repair result", err)
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"repair.result.applied",
		slog.String("repair_id", run.entry.RepairID),
		slog.String("node_id", run.entry.NodeID.String()),
		slog.String("result_digest", hex.EncodeToString(receipt.ResultDigest[:])),
	)

	return nil
}

// repairResultPlan builds the checked mutation of the combined result. When
// the result changes the parent or removes a child_of edge, the plan sets
// the resulting parent, and the mutation removes every other child_of edge.
// The manifest must list each of those removals.
func repairResultPlan(
	ctx context.Context,
	run repairPhaseRun,
	frozen repairFrozen,
	snapshot node.MutationSnapshot,
) (node.MutationPlan, error) {
	entry := run.entry
	parent, err := repairParentChange(ctx, entry, snapshot)
	if err != nil {
		return node.MutationPlan{}, err
	}

	removed, err := removedEdgeExpectations(ctx, entry, snapshot, parent)
	if err != nil {
		return node.MutationPlan{}, err
	}

	records, err := frozenGraphRecords(ctx, entry)
	if err != nil {
		return node.MutationPlan{}, err
	}

	if parent != nil &&
		!slices.ContainsFunc(
			records,
			func(record node.GraphRecord) bool { return record.NodeID == parent.TargetID },
		) {
		return node.MutationPlan{}, repairRefused(
			ctx,
			entry,
			fmt.Sprintf("the frozen source does not record the parent %s", parent.TargetID),
			domain.ErrConflict,
		)
	}

	checkpoint := run.checkpoint(node.RepairPhaseCompleted)

	return node.MutationPlan{
		OrgID:         frozen.orgID,
		NodeID:        entry.NodeID,
		NodeType:      entry.NodeType,
		ActorID:       run.operator.ID,
		Source:        node.SourceExpectation{Absent: false, PrimaryDigest: frozen.primaryDigest},
		MetadataEpoch: frozen.metadataEpoch,
		Graph: node.GraphExpectation{
			Records:       records,
			Relationships: removed,
			Incoming:      nil,
		},
		Name:          entry.Result.Name,
		Props:         entry.Result.Props,
		Parent:        parent,
		ReferenceKeys: nil,
		Relationships: repairRelationshipChanges(entry, frozen, run.operator),
		Idempotency:   nil,
		Checkpoint:    &checkpoint,
	}, nil
}
