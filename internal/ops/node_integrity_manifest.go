package ops

import (
	"encoding/json"

	"github.com/google/uuid"
)

const nodeIntegrityManifestVersion = 1

const nodeIntegrityTicket = "TACK-582"

// RepairEntryStatus determines whether a manifest entry permits execution.
type RepairEntryStatus string

const (
	// RepairEntryExecutable permits execution of a reviewed entry.
	RepairEntryExecutable RepairEntryStatus = "executable"
	// RepairEntryUnresolved requires the command to report the entry without
	// applying it.
	RepairEntryUnresolved RepairEntryStatus = "unresolved"
)

// RepairEvidenceKind classifies evidence as an observed value, inferred
// intent, or approved decision.
type RepairEvidenceKind string

const (
	// RepairEvidenceObserved identifies a record value that the scan read.
	RepairEvidenceObserved RepairEvidenceKind = "observed"
	// RepairEvidenceInferred identifies intent that a rule inferred from records.
	RepairEvidenceInferred RepairEvidenceKind = "inferred"
	// RepairEvidenceDecision identifies a dated decision with approval.
	RepairEvidenceDecision RepairEvidenceKind = "decision"
)

// NodeIntegrityRequest contains reviewed repairs before the command freezes
// their current records in a [NodeIntegrityManifest].
type NodeIntegrityRequest struct {
	Version int                  `json:"version"`
	Ticket  string               `json:"ticket"`
	Entries []NodeIntegrityEntry `json:"entries"`
}

// NodeIntegrityManifest contains frozen repairs. Digest records the SHA-256
// digest of the manifest's JSON encoding with Digest empty.
type NodeIntegrityManifest struct {
	Version int                  `json:"version"`
	Ticket  string               `json:"ticket"`
	Digest  string               `json:"digest"`
	Entries []NodeIntegrityEntry `json:"entries"`
}

// NodeIntegrityEntry specifies one reviewed node repair. A request omits
// Source; a frozen manifest includes it.
type NodeIntegrityEntry struct {
	RepairID string            `json:"repair_id"`
	NodeID   uuid.UUID         `json:"node_id"`
	NodeType string            `json:"node_type"`
	Status   RepairEntryStatus `json:"status"`
	Classes  []string          `json:"classes"`
	Reason   string            `json:"reason,omitempty"   exhaustruct:"optional"`
	Evidence []RepairEvidence  `json:"evidence"`
	Result   RepairResult      `json:"result"`
	Report   *RepairReport     `json:"report,omitempty"   exhaustruct:"optional"`
	Delete   *RepairDelete     `json:"delete,omitempty"   exhaustruct:"optional"`
	Source   *RepairSource     `json:"source,omitempty"   exhaustruct:"optional"`
}

// RepairEvidence identifies and summarizes evidence supporting an entry.
type RepairEvidence struct {
	Kind      RepairEvidenceKind `json:"kind"`
	Reference string             `json:"reference"`
	Summary   string             `json:"summary"`
}

// RepairResult specifies an entry's combined changes. A null property value
// requests property removal. AddEdges and RemoveEdges specify changes to
// the node's outgoing relationships.
type RepairResult struct {
	Name        *string                    `json:"name,omitempty"         exhaustruct:"optional"`
	Props       map[string]json.RawMessage `json:"props,omitempty"        exhaustruct:"optional"`
	AddEdges    []RepairEdge               `json:"add_edges,omitempty"    exhaustruct:"optional"`
	RemoveEdges []RepairEdge               `json:"remove_edges,omitempty" exhaustruct:"optional"`
}

// RepairEdge identifies an outgoing relationship by its type and target.
type RepairEdge struct {
	RelationType string    `json:"relation_type"`
	TargetID     uuid.UUID `json:"target_id"`
}

// RepairReport specifies a child node that preserves SourceProperty before
// the repair removes that property. Props must include the source value's
// complete text.
type RepairReport struct {
	SourceProperty string                     `json:"source_property"`
	NodeType       string                     `json:"node_type"`
	Name           string                     `json:"name"`
	Props          map[string]json.RawMessage `json:"props"`
}

// RepairDelete requests deletion of the frozen node after export verification.
type RepairDelete struct {
	ExportFile string `json:"export_file"`
}

// RepairSource records an entry's frozen primary record and digest, edges
// in both directions, related node digests, parent_id chain, graph digest,
// and organization metadata epoch.
type RepairSource struct {
	Primary       json.RawMessage    `json:"primary"`
	PrimaryDigest string             `json:"primary_digest"`
	Edges         []RepairStoredEdge `json:"edges"`
	Related       []RepairRelated    `json:"related"`
	Chain         []uuid.UUID        `json:"chain"`
	GraphDigest   string             `json:"graph_digest"`
	MetadataEpoch string             `json:"metadata_epoch"`
}

// RepairStoredEdge records a stored relationship's source, type, and target.
type RepairStoredEdge struct {
	SourceID     uuid.UUID `json:"source_id"`
	RelationType string    `json:"relation_type"`
	TargetID     uuid.UUID `json:"target_id"`
}

// RepairRelated records a node that the repair depends on. An empty digest
// means the node is missing or outside the repaired node's organization.
type RepairRelated struct {
	NodeID        uuid.UUID `json:"node_id"`
	PrimaryDigest string    `json:"primary_digest"`
}
