package audit

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// LastChangeStatus records whether the ledger rows establish the latest
// change of one field path or relationship.
type LastChangeStatus string

const (
	// LastChangeKnown means the reported change is the latest evidence for the
	// entry in the rows read.
	LastChangeKnown LastChangeStatus = "known"
	// LastChangeUnknown means a legacy row cannot identify its changed fields
	// and is not older than the latest known change. It also applies when
	// legacy rows exist without a known change.
	LastChangeUnknown LastChangeStatus = "unknown"
	// LastChangeAmbiguousOrder means at least two successful changes lack a
	// known order because at least one lacks a FoundationDB commit version.
	LastChangeAmbiguousOrder LastChangeStatus = "ambiguous_order"
	// LastChangePartial means the read does not establish the latest change.
	// The read either omits part of the node's history without finding the entry
	// or stops at its row limit before reading the complete window.
	LastChangePartial LastChangeStatus = "partial"
)

// LastChangeEffect states whether a change wrote or removed the value.
type LastChangeEffect string

const (
	// LastChangeSet means the change wrote the value.
	LastChangeSet LastChangeEffect = "set"
	// LastChangeRemoved means the value existed before the change and does not
	// exist after it.
	LastChangeRemoved LastChangeEffect = "removed"
)

// LastChange is one successful change row of a field path or a relationship.
type LastChange struct {
	EventID   uuid.UUID `json:"event_id"`
	Action    string    `json:"action"`
	Outcome   Outcome   `json:"outcome"`
	ActorID   uuid.UUID `json:"actor_id"`
	ActorKind int16     `json:"actor_kind"`
	EventTime time.Time `json:"event_time"`
	// CommitVersion is empty when the row did not come from the FoundationDB
	// outbox.
	CommitVersion string           `json:"commit_version,omitempty"`
	Effect        LastChangeEffect `json:"effect"`
}

// LastChangeEntry reports one field path, or one relationship identity when
// Path is empty. Change is nil when the rows read do not establish one change.
type LastChangeEntry struct {
	Path              string           `json:"path,omitempty"`
	SourceID          uuid.UUID        `json:"source_id,omitzero"`
	RelationType      string           `json:"relation_type,omitempty"`
	TargetID          uuid.UUID        `json:"target_id,omitzero"`
	Status            LastChangeStatus `json:"status"`
	Change            *LastChange      `json:"change,omitempty"`
	CandidateEventIDs []uuid.UUID      `json:"candidate_event_ids,omitempty"`
	LegacyEventIDs    []uuid.UUID      `json:"legacy_event_ids,omitempty"`
}

// LastChangeReport lists the latest change of each field path and each
// relationship of one node. HistoryStartInWindow is true when the rows read
// include the node's creation.
type LastChangeReport struct {
	NodeID               uuid.UUID `json:"node_id"`
	HistoryStartInWindow bool      `json:"history_start_in_window"`
	// LegacyRowCount counts the legacy change rows of the node in the rows
	// read: node rows and relationship rows with the node as source.
	LegacyRowCount int `json:"legacy_row_count"`
	// LegacyRelationshipEventIDs lists the legacy relationship rows with the
	// node as source. These rows store the source node and do not store the
	// relation type or the target. Each one applies to every relationship
	// entry with the node as source. The ledger cannot find legacy
	// relationship rows with the node as target, because those rows store
	// only the source node.
	LegacyRelationshipEventIDs []uuid.UUID       `json:"legacy_relationship_event_ids"`
	Fields                     []LastChangeEntry `json:"fields"`
	Relationships              []LastChangeEntry `json:"relationships"`
}

// ValidateFieldPath validates one nonempty RFC 6901 JSON pointer.
//
// A pointer starts with a slash, and each tilde starts the escape ~0 or ~1.
// The function returns an error for an invalid prefix or escape.
//
// A caller can validate a property path as follows:
//
//	fmt.Println(ValidateFieldPath("/props/state_id"))
//	// Output: <nil>
func ValidateFieldPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("field path %q must start with /", path)
	}
	for position := range len(path) {
		if path[position] != '~' {
			continue
		}
		if position+1 >= len(path) || (path[position+1] != '0' && path[position+1] != '1') {
			return fmt.Errorf("field path %q has a ~ that does not start ~0 or ~1", path)
		}
	}
	return nil
}

// errHistoryCursor reports a cursor that does not match the format from
// marshalHistoryCursor.
var errHistoryCursor = errors.New("cursor is not a next_cursor from audit last-change")
