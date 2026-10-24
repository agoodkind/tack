package ops

import (
	"context"
	"fmt"
	"log/slog"
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
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
	references, err := validateRepairResult(ctx, runner.mutator, run.entry, schema, report.NodeType,
		repairResultProps(nil, plan.Props, plan.Parent), []uuid.UUID{run.entry.NodeID})
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
		return repairRefused(ctx, run.entry, fmt.Sprintf("the create returned comment %s, not the checkpoint comment %s", receipt.NodeID, commentID), domain.ErrConflict)
	}
	telemetry.L(ctx).InfoContext(ctx, "repair.report.created", slog.String("repair_id", run.entry.RepairID),
		slog.String("node_id", run.entry.NodeID.String()), slog.String("comment_id", commentID.String()))
	return nil
}

// reportCommentPlan builds the checked mutation that creates the report
// comment under the repaired node. If the report does not set scope_id,
// the mutation copies scope_id from the node to the comment. The plan
// requires the repaired node to keep its frozen primary digest and each
// ancestor to keep its current digest.
func (runner repairPhaseRunner) reportCommentPlan(ctx context.Context, run repairPhaseRun, frozen repairFrozen, snapshot node.MutationSnapshot, commentID uuid.UUID) (node.MutationPlan, error) {
	report := *run.entry.Report
	planned, err := reportContentDigest(ctx, report, report.NodeType, report.Name, report.Props)
	if err != nil {
		return node.MutationPlan{}, err
	}
	ancestors, err := currentChainRecords(ctx, runner.mutator, run.entry, frozen.orgID, snapshot.ParentID)
	if err != nil {
		return node.MutationPlan{}, err
	}
	props := maps.Clone(report.Props)
	if scope, scoped := snapshot.Node.Props[repairScopeProperty]; scoped {
		if _, declared := props[repairScopeProperty]; !declared {
			props[repairScopeProperty] = scope
		}
	}
	checkpoint := run.checkpoint(node.RepairPhaseCommentCreated)
	checkpoint.Next.CommentID = commentID
	name := report.Name
	records := []node.GraphRecord{{NodeID: run.entry.NodeID, PrimaryDigest: frozen.primaryDigest}}
	return node.MutationPlan{
		OrgID: frozen.orgID, NodeID: commentID, NodeType: report.NodeType, ActorID: run.operator.ID,
		Source:        node.SourceExpectation{Absent: true, PrimaryDigest: node.Digest{}},
		MetadataEpoch: frozen.metadataEpoch,
		Graph:         node.GraphExpectation{Records: mergeGraphRecords(records, ancestors), Relationships: nil, Incoming: nil},
		Name:          &name, Props: props, Parent: &node.ParentChange{TargetID: run.entry.NodeID},
		ReferenceKeys: nil, Relationships: node.RelationshipChanges{Add: nil, Remove: nil},
		Idempotency: &node.IdempotencyRecord{
			Key: repairReportIdempotencyKey(run.entry.RepairID), NodeID: commentID, Fingerprint: planned,
			CreatedAt: clock.Now().UTC(), Source: nodeIntegrityBackfillTool,
		},
		Checkpoint: &checkpoint,
	}, nil
}

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
	if err := runner.checkpoints.AdvanceRepairCheckpoint(ctx, checkpoint.Previous, checkpoint.Next); err != nil {
		return repairMutationFailure(ctx, run.entry, "record the verified report comment", err)
	}
	telemetry.L(ctx).InfoContext(ctx, "repair.report.verified", slog.String("repair_id", run.entry.RepairID),
		slog.String("comment_id", run.stored.CommentID.String()), slog.String("content_digest", digest))
	return nil
}

// verifiedReportRecord verifies the report comment again before the result
// removes the source property. It returns the comment primary digest, which
// the result transaction requires to stay unchanged.
func (runner repairPhaseRunner) verifiedReportRecord(ctx context.Context, run repairPhaseRun, frozen repairFrozen) (node.GraphRecord, error) {
	snapshot, digest, err := runner.readReportComment(ctx, run, frozen, node.RepairPhaseCommentVerified)
	if err != nil {
		return node.GraphRecord{}, err
	}
	if digest != run.stored.ContentDigest {
		return node.GraphRecord{}, repairRefused(ctx, run.entry, "the report comment changed after its verification", domain.ErrConflict)
	}
	return node.GraphRecord{NodeID: run.stored.CommentID, PrimaryDigest: snapshot.PrimaryDigest}, nil
}

// readReportComment reads the comment that the stored checkpoint records at
// phase. It requires the comment type, name, report props, parent_id, and
// child_of edge to match the manifest and returns the content digest.
func (runner repairPhaseRunner) readReportComment(ctx context.Context, run repairPhaseRun, frozen repairFrozen, phase node.RepairPhase) (node.MutationSnapshot, string, error) {
	report := *run.entry.Report
	if run.stored == nil || run.stored.Phase != phase || run.stored.CommentID == uuid.Nil {
		return node.MutationSnapshot{}, "", repairRefused(ctx, run.entry, "the checkpoint does not record a report comment", domain.ErrFailedPrecondition)
	}
	commentID := run.stored.CommentID
	snapshot, _, err := readNodeSnapshot(ctx, runner.mutator, run.entry, frozen.orgID, commentID)
	if err != nil {
		return snapshot, "", err
	}
	if !snapshot.Exists || snapshot.Node.NodeType != report.NodeType || snapshot.ParentID != run.entry.NodeID {
		return snapshot, "", repairRefused(ctx, run.entry, fmt.Sprintf("report comment %s is absent, has another type, or has another parent", commentID), domain.ErrConflict)
	}
	attached := false
	for _, edge := range snapshot.Outgoing {
		attached = attached || (edge.Relationship.RelationType == node.RelChildOf && edge.Relationship.TargetID == run.entry.NodeID)
	}
	if !attached {
		return snapshot, "", repairRefused(ctx, run.entry, fmt.Sprintf("report comment %s has no child_of edge to the node", commentID), domain.ErrConflict)
	}
	digest, err := reportContentDigest(ctx, report, snapshot.Node.NodeType, snapshot.Node.Name, snapshot.Node.Props)
	if err != nil {
		return snapshot, "", err
	}
	planned, err := reportContentDigest(ctx, report, report.NodeType, report.Name, report.Props)
	if err != nil {
		return snapshot, "", err
	}
	if digest != planned {
		return snapshot, "", repairRefused(ctx, run.entry, fmt.Sprintf("report comment %s content differs from the manifest", commentID), domain.ErrConflict)
	}
	return snapshot, digest, nil
}
