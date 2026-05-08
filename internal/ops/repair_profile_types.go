package ops

import "github.com/google/uuid"

// RepairRelationshipAction names one generic relationship mutation.
type RepairRelationshipAction string

const (
	// RepairRelationshipAdd records a planned relationship addition.
	RepairRelationshipAdd RepairRelationshipAction = "add"
	// RepairRelationshipRemove records a planned relationship removal.
	RepairRelationshipRemove RepairRelationshipAction = "remove"
)

// RepairRelationshipChange describes one relationship edge mutation in a preview.
type RepairRelationshipChange struct {
	Action       RepairRelationshipAction `json:"action"`
	SourceID     uuid.UUID                `json:"source_id"`
	RelationType string                   `json:"relation_type"`
	TargetID     uuid.UUID                `json:"target_id"`
}

// RepairRenameField describes one generic property rename operation.
type RepairRenameField struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// RepairAppendTextField appends text from one property into another text property.
type RepairAppendTextField struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Heading   string `json:"heading,omitempty"`
	Separator string `json:"separator,omitempty"`
}
