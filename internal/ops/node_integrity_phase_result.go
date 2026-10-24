package ops

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
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
	references, err := validateRepairResult(ctx, runner.mutator, run.entry, schema, run.entry.NodeType,
		repairResultProps(snapshot.Node.Props, plan.Props, plan.Parent), resultChildOf(snapshot, plan.Parent))
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
	telemetry.L(ctx).InfoContext(ctx, "repair.result.applied", slog.String("repair_id", run.entry.RepairID),
		slog.String("node_id", run.entry.NodeID.String()), slog.String("result_digest", hex.EncodeToString(receipt.ResultDigest[:])))
	return nil
}

// repairResultPlan builds the checked mutation of the combined result. When
// the result changes the parent or removes a child_of edge, the plan sets
// the resulting parent, and the mutation removes every other child_of edge.
// The manifest must list each of those removals.
func repairResultPlan(ctx context.Context, run repairPhaseRun, frozen repairFrozen, snapshot node.MutationSnapshot) (node.MutationPlan, error) {
	entry := run.entry
	parent, err := repairParentChange(ctx, entry, snapshot)
	if err != nil {
		return node.MutationPlan{}, err
	}
	props := maps.Clone(entry.Result.Props)
	if parent != nil {
		delete(props, repairParentProperty)
	}
	removed, err := removedEdgeExpectations(ctx, entry, snapshot, parent)
	if err != nil {
		return node.MutationPlan{}, err
	}
	records, err := frozenGraphRecords(ctx, entry)
	if err != nil {
		return node.MutationPlan{}, err
	}
	if parent != nil && !slices.ContainsFunc(records, func(record node.GraphRecord) bool { return record.NodeID == parent.TargetID }) {
		return node.MutationPlan{}, repairRefused(ctx, entry, fmt.Sprintf("the frozen source does not record the parent %s", parent.TargetID), domain.ErrConflict)
	}
	checkpoint := run.checkpoint(node.RepairPhaseCompleted)
	return node.MutationPlan{
		OrgID: frozen.orgID, NodeID: entry.NodeID, NodeType: entry.NodeType, ActorID: run.operator.ID,
		Source:        node.SourceExpectation{Absent: false, PrimaryDigest: frozen.primaryDigest},
		MetadataEpoch: frozen.metadataEpoch,
		Graph:         node.GraphExpectation{Records: records, Relationships: removed, Incoming: nil},
		Name:          entry.Result.Name, Props: props, Parent: parent, ReferenceKeys: nil,
		Relationships: repairRelationshipChanges(entry, frozen, run.operator),
		Idempotency:   nil, Checkpoint: &checkpoint,
	}, nil
}

// repairParentChange returns the resulting parent when the result sets
// parent_id or removes a child_of edge. A result that only removes edges
// keeps the current parent.
func repairParentChange(ctx context.Context, entry NodeIntegrityEntry, snapshot node.MutationSnapshot) (*node.ParentChange, error) {
	if _, declared := entry.Result.Props[repairParentProperty]; declared {
		parentID := resultParent(entry.Result)
		if parentID == uuid.Nil {
			return nil, repairRefused(ctx, entry, "the result parent_id is not a node ID", domain.ErrInvalidArgument)
		}
		return &node.ParentChange{TargetID: parentID}, nil
	}
	for _, edge := range entry.Result.RemoveEdges {
		if edge.RelationType != node.RelChildOf {
			continue
		}
		if snapshot.ParentID == uuid.Nil {
			return nil, repairRefused(ctx, entry, "the node has no parent to keep", domain.ErrConflict)
		}
		return &node.ParentChange{TargetID: snapshot.ParentID}, nil
	}
	return nil, nil
}

// removedEdgeExpectations returns the stored digests of the outgoing edges
// that the mutation removes. When parent has a value, the mutation removes
// every child_of edge that does not target the parent.
// removedEdgeExpectations refuses a removal that the manifest does not
// list. It also refuses a listed edge that the node does not have.
func removedEdgeExpectations(ctx context.Context, entry NodeIntegrityEntry, snapshot node.MutationSnapshot, parent *node.ParentChange) ([]node.RelationshipExpectation, error) {
	listed := make(map[RepairEdge]bool, len(entry.Result.RemoveEdges))
	for _, edge := range entry.Result.RemoveEdges {
		listed[edge] = true
	}
	expectations := []node.RelationshipExpectation{}
	for _, stored := range snapshot.Outgoing {
		edge := RepairEdge{RelationType: stored.Relationship.RelationType, TargetID: stored.Relationship.TargetID}
		derived := parent != nil && edge.RelationType == node.RelChildOf && edge.TargetID != parent.TargetID
		if derived && !listed[edge] {
			return nil, repairRefused(ctx, entry, fmt.Sprintf("the new parent removes the child_of edge to %s, which the manifest does not list", edge.TargetID), domain.ErrConflict)
		}
		if !listed[edge] {
			continue
		}
		if edge.RelationType == node.RelChildOf && !derived {
			return nil, repairRefused(ctx, entry, fmt.Sprintf("the manifest removes the child_of edge to the resulting parent %s", edge.TargetID), domain.ErrConflict)
		}
		delete(listed, edge)
		expectations = append(expectations, node.RelationshipExpectation{RelationType: edge.RelationType, TargetID: edge.TargetID, Digest: stored.Digest})
	}
	for _, edge := range entry.Result.RemoveEdges {
		if listed[edge] {
			return nil, repairRefused(ctx, entry, fmt.Sprintf("the node has no %s edge to %s", edge.RelationType, edge.TargetID), domain.ErrConflict)
		}
	}
	return expectations, nil
}

// repairRelationshipChanges returns the changes to edges other than
// child_of. The mutation derives child_of edges from the resulting parent.
func repairRelationshipChanges(entry NodeIntegrityEntry, frozen repairFrozen, operator audit.OperatorPrincipal) node.RelationshipChanges {
	changes := node.RelationshipChanges{Add: nil, Remove: nil}
	now := clock.Now().UTC()
	for _, edge := range entry.Result.AddEdges {
		changes.Add = append(changes.Add, &node.Relationship{
			OrgID: frozen.orgID, SourceID: entry.NodeID, RelationType: edge.RelationType, TargetID: edge.TargetID,
			CreatedBy: operator.ID, CreatedAt: now, Props: nil,
		})
	}
	for _, edge := range entry.Result.RemoveEdges {
		if edge.RelationType == node.RelChildOf {
			continue
		}
		changes.Remove = append(changes.Remove, &node.Relationship{
			OrgID: frozen.orgID, SourceID: entry.NodeID, RelationType: edge.RelationType, TargetID: edge.TargetID,
			CreatedBy: operator.ID, CreatedAt: now, Props: nil,
		})
	}
	return changes
}
