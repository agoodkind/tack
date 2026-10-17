package node

import (
	"time"

	"github.com/google/uuid"
)

// RepairPhase represents the last committed phase of a reviewed repair.
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
func (phase RepairPhase) Valid() bool {
	_, declared := repairPhaseRank[phase]
	return declared
}

// Follows reports whether phase is valid and ranks higher than previous.
// An empty previous permits any valid phase as the first checkpoint.
func (phase RepairPhase) Follows(previous RepairPhase) bool {
	if !phase.Valid() {
		return false
	}
	if previous == "" {
		return true
	}
	return repairPhaseRank[phase] > repairPhaseRank[previous]
}

// RepairCheckpoint records a reviewed repair's last committed phase.
// The repair ID stays stable across retries and later manifests.
// Each phase commits its changes and checkpoint in the same transaction.
type RepairCheckpoint struct {
	// RepairID is the stable repair ID from the manifest.
	RepairID string `json:"repair_id"`
	// ManifestDigest is the digest of the manifest used to commit this phase.
	ManifestDigest string `json:"manifest_digest"`
	// NodeID is the ID of the node under repair.
	NodeID uuid.UUID `json:"node_id"`
	// SourceDigest is the node's primary record digest captured in the manifest.
	SourceDigest string `json:"source_digest"`
	// Phase records the last committed repair phase.
	Phase RepairPhase `json:"phase"`
	// CommentID is the ID of the report comment created by the repair.
	CommentID uuid.UUID `json:"comment_id,omitzero" exhaustruct:"optional"`
	// ContentDigest is the digest computed from the report comment after readback.
	ContentDigest string `json:"content_digest,omitempty" exhaustruct:"optional"`
	// ExportPath locates the verified export file.
	ExportPath string `json:"export_path,omitempty" exhaustruct:"optional"`
	// ExportDigest is the digest computed from the export file after readback.
	ExportDigest string `json:"export_digest,omitempty" exhaustruct:"optional"`
	// ResultDigest is the primary record digest after the final change.
	// A deletion leaves this field empty.
	ResultDigest string `json:"result_digest,omitempty" exhaustruct:"optional"`
	// ActorID is the ID of the operator who committed this phase.
	ActorID uuid.UUID `json:"actor_id"`
	// UpdatedAt records when this phase committed.
	UpdatedAt time.Time `json:"updated_at"`
}
