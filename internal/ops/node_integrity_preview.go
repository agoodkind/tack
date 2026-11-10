package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// RepairPreviewStatus identifies an entry's state after comparison with
// current records and checkpoints.
type RepairPreviewStatus string

const (
	// RepairPreviewPlanned indicates that current evidence matches the
	// frozen source and the repair does not have a checkpoint.
	RepairPreviewPlanned RepairPreviewStatus = "planned"
	// RepairPreviewInProgress indicates that a checkpoint records an earlier
	// phase and current evidence permits the next phase.
	RepairPreviewInProgress RepairPreviewStatus = "in_progress"
	// RepairPreviewCompleted indicates that the checkpoint records the final phase.
	RepairPreviewCompleted RepairPreviewStatus = "completed"
	// RepairPreviewConflicted indicates a conflict in current evidence or
	// the stored checkpoint.
	RepairPreviewConflicted RepairPreviewStatus = "conflicted"
	// RepairPreviewUnresolved identifies an entry with unresolved manifest status.
	RepairPreviewUnresolved RepairPreviewStatus = "unresolved"
)

// RepairCheckpointReader reads a repair checkpoint by repair ID.
type RepairCheckpointReader interface {
	RepairCheckpoint(ctx context.Context, repairID string) (*node.RepairCheckpoint, error)
}

// NodeIntegrityEntryPreview records an entry's current status and next phase.
type NodeIntegrityEntryPreview struct {
	RepairID  string              `json:"repair_id"`
	NodeID    uuid.UUID           `json:"node_id"`
	NodeType  string              `json:"node_type"`
	Classes   []string            `json:"classes"`
	Status    RepairPreviewStatus `json:"status"`
	Phase     node.RepairPhase    `json:"phase,omitempty"`
	NextPhase node.RepairPhase    `json:"next_phase,omitempty"`
	Reason    string              `json:"reason,omitempty"`
	Result    RepairResult        `json:"result"`
	Report    *RepairReport       `json:"report,omitempty"`
	Delete    *RepairDelete       `json:"delete,omitempty"`
}

// NodeIntegrityPreview contains entry previews and status counts for a frozen manifest.
type NodeIntegrityPreview struct {
	ManifestDigest string                      `json:"manifest_digest"`
	Counts         map[RepairPreviewStatus]int `json:"counts"`
	Entries        []NodeIntegrityEntryPreview `json:"entries"`
}

type repairStateReader interface {
	RepairEvidenceReader
	RepairCheckpointReader
}

func previewNodeIntegrityManifest(ctx context.Context, reader repairStateReader, manifest NodeIntegrityManifest) (NodeIntegrityPreview, error) {
	preview := NodeIntegrityPreview{
		ManifestDigest: manifest.Digest, Counts: map[RepairPreviewStatus]int{},
		Entries: make([]NodeIntegrityEntryPreview, 0, len(manifest.Entries)),
	}
	for _, entry := range manifest.Entries {
		entryPreview, err := previewRepairEntry(ctx, reader, entry)
		if err != nil {
			return preview, err
		}
		preview.Counts[entryPreview.Status]++
		preview.Entries = append(preview.Entries, entryPreview)
	}
	telemetry.L(ctx).InfoContext(ctx, "repair.manifest.previewed", slog.String("manifest_digest", manifest.Digest),
		slog.Int("planned", preview.Counts[RepairPreviewPlanned]), slog.Int("in_progress", preview.Counts[RepairPreviewInProgress]),
		slog.Int("completed", preview.Counts[RepairPreviewCompleted]), slog.Int("conflicted", preview.Counts[RepairPreviewConflicted]),
		slog.Int("unresolved", preview.Counts[RepairPreviewUnresolved]))
	return preview, nil
}

func previewRepairEntry(ctx context.Context, reader repairStateReader, entry NodeIntegrityEntry) (NodeIntegrityEntryPreview, error) {
	preview := NodeIntegrityEntryPreview{
		RepairID: entry.RepairID, NodeID: entry.NodeID, NodeType: entry.NodeType, Classes: entry.Classes,
		Status: RepairPreviewUnresolved, Phase: "", NextPhase: "", Reason: entry.Reason,
		Result: entry.Result, Report: entry.Report, Delete: entry.Delete,
	}
	if entry.Status == RepairEntryUnresolved {
		return preview, nil
	}
	checkpoint, err := reader.RepairCheckpoint(ctx, entry.RepairID)
	if err != nil {
		wrapped := fmt.Errorf("read checkpoint of repair %s: %w", entry.RepairID, err)
		slog.ErrorContext(ctx, "repair.checkpoint.read_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", entry.RepairID))
		return preview, wrapped
	}
	current := node.RepairPhase("")
	if checkpoint != nil {
		current = checkpoint.Phase
		preview.Phase = current
	}
	next, known := nextRepairPhase(entry, current)
	preview.NextPhase = next
	if checkpoint != nil && (!known || checkpoint.SourceDigest != entry.Source.PrimaryDigest || checkpoint.NodeID != entry.NodeID) {
		preview.Status, preview.Reason = RepairPreviewConflicted, "the stored checkpoint belongs to another source or phase plan"
		return preview, nil
	}
	if checkpoint != nil && next == "" {
		preview.Status, preview.Reason = RepairPreviewCompleted, ""
		return preview, nil
	}
	evidence, err := readRepairEvidence(ctx, reader, entry)
	if err != nil {
		return preview, err
	}
	if checkpoint != nil {
		evidence = withoutReportComment(evidence, checkpoint.CommentID)
	}
	reason, err := staleEvidence(ctx, entry, evidence)
	if err != nil {
		return preview, err
	}
	switch {
	case reason != "":
		preview.Status, preview.Reason = RepairPreviewConflicted, reason
	case checkpoint != nil:
		preview.Status, preview.Reason = RepairPreviewInProgress, ""
	default:
		preview.Status, preview.Reason = RepairPreviewPlanned, ""
	}
	return preview, nil
}

func staleEvidence(ctx context.Context, entry NodeIntegrityEntry, evidence fdbadapter.RepairEvidence) (string, error) {
	if !evidence.Node.Found {
		return "stale primary record: the node does not exist", nil
	}
	current, err := sourceFromEvidence(ctx, evidence)
	if err != nil {
		return "", err
	}
	frozen := entry.Source
	switch {
	case current.PrimaryDigest != frozen.PrimaryDigest:
		return "stale primary record: the primary digest changed", nil
	case current.GraphDigest != frozen.GraphDigest:
		return "stale graph: an edge or a related record changed", nil
	case current.MetadataEpoch != frozen.MetadataEpoch:
		return "stale metadata: the metadata epoch changed", nil
	}
	return graphProblem(entry, evidence), nil
}
