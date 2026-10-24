package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// exportPhase exports the node, reads the export back, and records its
// digest in the exported checkpoint.
func (runner repairPhaseRunner) exportPhase(ctx context.Context, run repairPhaseRun) error {
	checkpoint := run.checkpoint(node.RepairPhaseExported)
	digest, err := exportRepairNode(ctx, runner.exports, run.entry, run.manifestDigest, run.operator.ID)
	if err != nil {
		return err
	}
	checkpoint.Next.ExportPath, checkpoint.Next.ExportDigest = run.entry.Delete.ExportFile, digest
	if err := runner.checkpoints.AdvanceRepairCheckpoint(ctx, checkpoint.Previous, checkpoint.Next); err != nil {
		return repairMutationFailure(ctx, run.entry, "record the verified export", err)
	}
	return nil
}

// deletePhase reads the export back, requires its digest and relationships
// to match the exported checkpoint and the current node, and deletes exactly
// the node through [node.Mutator.DeleteNode]. The same transaction
// completes the checkpoint. A read after the commit must not find the node.
func (runner repairPhaseRunner) deletePhase(ctx context.Context, run repairPhaseRun) error {
	frozen, err := frozenRepairSource(ctx, run.entry)
	if err != nil {
		return err
	}
	exported, err := readVerifiedExport(ctx, run)
	if err != nil {
		return err
	}
	snapshot, _, err := readRepairSnapshot(ctx, runner.mutator, run.entry, frozen)
	if err != nil {
		return err
	}
	outgoing, incoming, err := exportedEdgeExpectations(ctx, run.entry, exported, snapshot)
	if err != nil {
		return err
	}
	checkpoint := run.checkpoint(node.RepairPhaseCompleted)
	plan := node.DeletionPlan{
		OrgID: frozen.orgID, NodeID: run.entry.NodeID, ActorID: run.operator.ID,
		Source:     node.SourceExpectation{Absent: false, PrimaryDigest: frozen.primaryDigest},
		Graph:      node.GraphExpectation{Records: nil, Relationships: outgoing, Incoming: incoming},
		Checkpoint: &checkpoint,
	}
	staged, err := stageRepairEvent(ctx, run, node.RepairPhaseCompleted)
	if err != nil {
		return err
	}
	if _, err := runner.mutator.DeleteNode(staged, plan); err != nil {
		return repairMutationFailure(ctx, run.entry, "delete the exported node", err)
	}
	after, _, err := readNodeSnapshot(ctx, runner.mutator, run.entry, frozen.orgID, run.entry.NodeID)
	if err != nil {
		return err
	}
	if after.Exists {
		return repairRefused(ctx, run.entry, "a read after the delete still finds the node", domain.ErrConflict)
	}
	telemetry.L(ctx).InfoContext(ctx, "repair.node.deleted", slog.String("repair_id", run.entry.RepairID),
		slog.String("node_id", run.entry.NodeID.String()), slog.String("export_path", run.stored.ExportPath))
	return nil
}

// readVerifiedExport verifies the export file against the frozen source,
// requires its digest to equal the exported checkpoint digest, and returns
// the decoded document.
func readVerifiedExport(ctx context.Context, run repairPhaseRun) (repairExportDocument, error) {
	var document repairExportDocument
	if run.stored == nil || run.stored.Phase != node.RepairPhaseExported || run.stored.ExportDigest == "" {
		return document, repairExportRefused(ctx, run.entry, "the repair does not have a verified export", domain.ErrFailedPrecondition)
	}
	if _, err := verifyRepairExport(ctx, run.entry, run.manifestDigest); err != nil {
		return document, err
	}
	encoded, err := os.ReadFile(run.entry.Delete.ExportFile)
	if err != nil {
		return document, repairExportFailure(ctx, run.entry, err)
	}
	sum := sha256.Sum256(encoded)
	if hex.EncodeToString(sum[:]) != run.stored.ExportDigest {
		return document, repairExportRefused(ctx, run.entry, "the export file changed after its verification", domain.ErrConflict)
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		return document, repairExportFailure(ctx, run.entry, err)
	}
	return document, nil
}

// exportedEdgeExpectations requires the node's current outgoing and incoming
// relationships to match the export in both identity and payload digest.
// It returns the exported digests as outgoing and incoming expectations for
// DeleteNode. It returns an error wrapping domain.ErrConflict if either
// side lacks an edge or a payload digest differs. It returns an error
// wrapping domain.ErrFailedPrecondition if the snapshot has more incoming
// edges than one snapshot reads.
func exportedEdgeExpectations(ctx context.Context, entry NodeIntegrityEntry, exported repairExportDocument, snapshot node.MutationSnapshot) ([]node.RelationshipExpectation, []node.IncomingRelationshipExpectation, error) {
	if snapshot.IncomingOverflow {
		return nil, nil, repairExportRefused(ctx, entry, "the node has more incoming edges than one snapshot reads", domain.ErrFailedPrecondition)
	}
	remaining := make(map[RepairStoredEdge]json.RawMessage, len(exported.Relationships))
	for _, edge := range exported.Relationships {
		remaining[RepairStoredEdge{SourceID: edge.SourceID, RelationType: edge.RelationType, TargetID: edge.TargetID}] = edge.Value
	}
	outgoing := make([]node.RelationshipExpectation, 0, len(snapshot.Outgoing))
	for _, stored := range snapshot.Outgoing {
		edge := RepairStoredEdge{SourceID: entry.NodeID, RelationType: stored.Relationship.RelationType, TargetID: stored.Relationship.TargetID}
		digest, err := takeExportedDigest(ctx, entry, remaining, edge, stored)
		if err != nil {
			return nil, nil, err
		}
		outgoing = append(outgoing, node.RelationshipExpectation{RelationType: edge.RelationType, TargetID: edge.TargetID, Digest: digest})
	}
	incoming := make([]node.IncomingRelationshipExpectation, 0, len(snapshot.Incoming))
	for _, stored := range snapshot.Incoming {
		edge := RepairStoredEdge{SourceID: stored.Relationship.SourceID, RelationType: stored.Relationship.RelationType, TargetID: entry.NodeID}
		digest, err := takeExportedDigest(ctx, entry, remaining, edge, stored)
		if err != nil {
			return nil, nil, err
		}
		incoming = append(incoming, node.IncomingRelationshipExpectation{SourceID: edge.SourceID, RelationType: edge.RelationType, Digest: digest})
	}
	for _, exportedEdge := range exported.Relationships {
		edge := RepairStoredEdge{SourceID: exportedEdge.SourceID, RelationType: exportedEdge.RelationType, TargetID: exportedEdge.TargetID}
		if _, left := remaining[edge]; left {
			return nil, nil, exportEdgeChanged(ctx, entry, edge)
		}
	}
	return outgoing, incoming, nil
}

// takeExportedDigest removes edge from remaining, compacts its exported
// value, and returns node.RelationshipDigest of the compact bytes.
// It compacts the indented exported value to match the store's compact JSON.
// It returns an error wrapping domain.ErrConflict if the export lacks edge,
// the value is empty or invalid JSON, or the digest differs from the digest
// of the relationship in stored.
func takeExportedDigest(ctx context.Context, entry NodeIntegrityEntry, remaining map[RepairStoredEdge]json.RawMessage, edge RepairStoredEdge, stored node.RelationshipSnapshot) (node.Digest, error) {
	value, found := remaining[edge]
	if !found {
		return node.Digest{}, exportEdgeChanged(ctx, entry, edge)
	}
	delete(remaining, edge)
	var compact bytes.Buffer
	if len(value) == 0 || json.Compact(&compact, value) != nil {
		return node.Digest{}, exportEdgeChanged(ctx, entry, edge)
	}
	digest := node.RelationshipDigest(compact.Bytes())
	if digest != stored.Digest {
		return node.Digest{}, exportEdgeChanged(ctx, entry, edge)
	}
	return digest, nil
}

func exportEdgeChanged(ctx context.Context, entry NodeIntegrityEntry, edge RepairStoredEdge) error {
	reason := fmt.Sprintf("the %s edge from %s to %s differs from the export", edge.RelationType, edge.SourceID, edge.TargetID)
	return repairExportRefused(ctx, entry, reason, domain.ErrConflict)
}
