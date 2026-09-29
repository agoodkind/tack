package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SubtreeDeleteJob is the durable progress record of one delete that removes
// a node and every hierarchy descendant of the node. Each step of the job is
// one bounded FoundationDB transaction that reads and rewrites this record,
// so a crashed runner leaves the record behind and another runner continues
// from it.
type SubtreeDeleteJob struct {
	// ID identifies the job. It is a UUIDv7.
	ID uuid.UUID `json:"id"`
	// OrgID is the organization of the deleted subtree.
	OrgID uuid.UUID `json:"org_id"`
	// RootID is the node the caller asked to delete.
	RootID uuid.UUID `json:"root_id"`
	// AuditTemplate is the ledger event staged for the root delete. The step
	// that deletes the root writes it unchanged. The step that deletes a
	// descendant writes a copy for that descendant. An empty template writes
	// no ledger event, because the caller records the delete itself.
	AuditTemplate json.RawMessage `json:"audit_template,omitempty"`
	// Stack lists the nodes on the path from the root to the node the next
	// step examines. The first entry is the root.
	Stack []uuid.UUID `json:"stack"`
	// Deleted counts the nodes the job deleted.
	Deleted int `json:"deleted"`
	// UpdatedAt is the commit time of the last step, read from the store
	// clock. A job without a recent step has no live runner.
	UpdatedAt time.Time `json:"updated_at"`
}

// DeletedNode identifies one node that a subtree delete step deleted.
type DeletedNode struct {
	ID       uuid.UUID
	NodeType string
	Name     string
}

// DeletionEventBuilder returns the ledger event for the delete of one
// descendant, built from the root's staged event.
type DeletionEventBuilder func(template json.RawMessage, deleted DeletedNode) (json.RawMessage, error)

// SubtreeDeleteProgress reports the job state after one step.
type SubtreeDeleteProgress struct {
	// Deleted counts the nodes the job deleted in every step so far.
	Deleted int
	// Done is true after the step that deleted the root, and for a job that
	// no longer exists.
	Done bool
}
