package node

import (
	"time"

	"github.com/google/uuid"
)

// RepairPhase records the last committed phase of a reviewed repair.
type RepairPhase string

const (
	// RepairPhaseCommentCreated records that the repair created its report comment.
	RepairPhaseCommentCreated RepairPhase = "comment_created"
	// RepairPhaseCommentVerified records that the repair read back its report
	// comment and verified its text and parent against the manifest.
	RepairPhaseCommentVerified RepairPhase = "comment_verified"
	// RepairPhaseExported records that the repair exported the node, read back
	// the export, and verified its digest against the current source.
	RepairPhaseExported RepairPhase = "exported"
	// RepairPhaseCompleted records that the repair committed its final change.
	RepairPhaseCompleted RepairPhase = "completed"
)

// repairPhaseRank assigns each phase a rank for checkpoint advancement.
// A checkpoint can advance only to a phase with a higher rank.
var repairPhaseRank = map[RepairPhase]int{
	RepairPhaseExported:        1,
	RepairPhaseCommentCreated:  2,
	RepairPhaseCommentVerified: 3,
	RepairPhaseCompleted:       4,
}

// Valid reports whether phase appears in [repairPhaseRank].
//
// Valid does not compare checkpoint order; Follows performs that check.
//
//   - An empty phase is invalid.
func (phase RepairPhase) Valid() bool {
	_, declared := repairPhaseRank[phase]
	return declared
}

// Follows reports whether phase is valid and ranks higher than previous.
//
// An empty previous permits any valid phase as the first checkpoint.
//
//   - A nonempty previous must also be valid.
func (phase RepairPhase) Follows(previous RepairPhase) bool {
	if !phase.Valid() {
		return false
	}
	if previous == "" {
		return true
	}
	return previous.Valid() && repairPhaseRank[phase] > repairPhaseRank[previous]
}

// RepairCheckpoint records a reviewed repair's last committed phase.
// The repair ID stays stable across retries and later manifests.
// Each phase commits its changes and checkpoint in the same transaction.
type RepairCheckpoint struct {
	// RepairID contains the stable repair ID from the manifest.
	RepairID string `json:"repair_id"`
	// ManifestDigest records the digest of the manifest for this phase's commit.
	ManifestDigest string `json:"manifest_digest"`
	// NodeID identifies the node under repair.
	NodeID uuid.UUID `json:"node_id"`
	// SourceDigest records the node's primary record digest from the manifest.
	SourceDigest string `json:"source_digest"`
	// Phase records the last committed repair phase.
	Phase RepairPhase `json:"phase"`
	// CommentID identifies the report comment that the repair created.
	CommentID uuid.UUID `json:"comment_id,omitzero" exhaustruct:"optional"`
	// ContentDigest records the report comment digest after the repair reads it.
	ContentDigest string `json:"content_digest,omitempty" exhaustruct:"optional"`
	// ExportPath identifies the verified export file.
	ExportPath string `json:"export_path,omitempty" exhaustruct:"optional"`
	// ExportDigest records the export file digest after the repair reads it.
	ExportDigest string `json:"export_digest,omitempty" exhaustruct:"optional"`
	// ResultDigest records the primary record digest after the final change.
	// A deletion leaves ResultDigest empty.
	ResultDigest string `json:"result_digest,omitempty" exhaustruct:"optional"`
	// ActorID identifies the operator who committed this phase.
	ActorID uuid.UUID `json:"actor_id"`
	// UpdatedAt records when this phase committed.
	UpdatedAt time.Time `json:"updated_at"`
}
