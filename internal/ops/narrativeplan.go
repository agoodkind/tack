package ops

import (
	"context"
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

// reportCommentPlan builds the checked mutation that creates the report
// comment under the repaired node. If the report does not set scope_id,
// the mutation copies scope_id from the node to the comment. The plan
// requires the repaired node to keep its frozen primary digest and each
// ancestor to keep its current digest.
func (runner repairPhaseRunner) reportCommentPlan(
	ctx context.Context,
	run repairPhaseRun,
	frozen repairFrozen,
	snapshot node.MutationSnapshot,
	commentID uuid.UUID,
) (node.MutationPlan, error) {
	report := *run.entry.Report
	planned, err := reportContentDigest(ctx, report, report.NodeType, report.Name, report.Props)
	if err != nil {
		return node.MutationPlan{}, err
	}

	ancestors, err := currentChainRecords(
		ctx,
		runner.mutator,
		run.entry,
		frozen.orgID,
		snapshot.ParentID,
	)
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
		OrgID:         frozen.orgID,
		NodeID:        commentID,
		NodeType:      report.NodeType,
		ActorID:       run.operator.ID,
		Source:        node.SourceExpectation{Absent: true, PrimaryDigest: node.Digest{}},
		MetadataEpoch: frozen.metadataEpoch,
		Graph: node.GraphExpectation{
			Records:       mergeGraphRecords(records, ancestors),
			Relationships: nil,
			Incoming:      nil,
		},
		Name:          &name,
		Props:         props,
		Parent:        &node.ParentChange{TargetID: run.entry.NodeID},
		ReferenceKeys: nil,
		Relationships: node.RelationshipChanges{Add: nil, Remove: nil},
		Idempotency: &node.IdempotencyRecord{
			Key:         repairReportIdempotencyKey(run.entry.RepairID),
			NodeID:      commentID,
			Fingerprint: planned,
			CreatedAt:   clock.Now().UTC(),
			Source:      nodeIntegrityBackfillTool,
		},
		Checkpoint: &checkpoint,
	}, nil
}
