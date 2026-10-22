package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SubtreeDeleteJob is the durable progress record of one delete that removes
// a node. Each direct child of the node moves to the node's own parent when
// NodeType metadata allows the child there, and is deleted with its
// descendants otherwise. Each step of the job is one bounded FoundationDB
// transaction that reads and rewrites this record. A crashed runner leaves
// the record behind, and another runner continues from it.
type SubtreeDeleteJob struct {
	// ID identifies the job. It is a UUIDv7.
	ID uuid.UUID `json:"id"`
	// OrgID is the organization of the deleted subtree.
	OrgID uuid.UUID `json:"org_id"`
	// RootID is the node the caller asked to delete.
	RootID uuid.UUID `json:"root_id"`
	// RootParentID is the hierarchy parent of the root when the job started,
	// or uuid.Nil for a root without one. Direct children of the root move to
	// it.
	RootParentID uuid.UUID `json:"root_parent_id" exhaustruct:"optional"`
	// ActorID is the user or operator who asked for the delete. A moved node
	// records it as its last updater.
	ActorID uuid.UUID `json:"actor_id" exhaustruct:"optional"`
	// Operation is the encoded audit operation from the request that started the job.
	// Every step records node and relationship changes with the actor and operation ID
	// from Operation, including steps that the resume loop runs.
	Operation json.RawMessage `json:"operation,omitempty" exhaustruct:"optional"`
	// Stack lists the nodes on the path from the root to the node the next
	// step examines. The first entry is the root.
	Stack []uuid.UUID `json:"stack"`
	// Deleted counts the nodes the job deleted.
	Deleted int `json:"deleted"`
	// Moved counts the direct children of the root that the job moved to the
	// root's parent.
	Moved int `json:"moved" exhaustruct:"optional"`
	// UpdatedAt is the commit time of the last step, read from the store
	// clock. A running job without a recent step has no live runner.
	UpdatedAt time.Time `json:"updated_at"`
	// FinishedAt is the commit time of the step that deleted the root. It is
	// zero while the job runs. A finished record stays readable until the
	// resume loop clears it.
	FinishedAt time.Time `json:"finished_at,omitzero" exhaustruct:"optional"`
}

// Finished reports whether the job deleted its root.
func (j *SubtreeDeleteJob) Finished() bool {
	return !j.FinishedAt.IsZero()
}

// SubtreeDeleteProgress reports the job state after one step.
type SubtreeDeleteProgress struct {
	// Deleted counts the nodes the job deleted in every step so far.
	Deleted int
	// Moved counts the nodes the job moved in every step so far.
	Moved int
	// Done is true after the step that deleted the root, and for a job that
	// no longer exists.
	Done bool
}

// SubtreeDeleteState is the state of one subtree delete job.
type SubtreeDeleteState string

const (
	// SubtreeDeleteRunning means the job has not deleted its root yet.
	SubtreeDeleteRunning SubtreeDeleteState = "running"
	// SubtreeDeleteFinished means the job deleted its root and every
	// descendant it did not move.
	SubtreeDeleteFinished SubtreeDeleteState = "finished"
)

// State returns the job's state.
func (j *SubtreeDeleteJob) State() SubtreeDeleteState {
	if j.Finished() {
		return SubtreeDeleteFinished
	}
	return SubtreeDeleteRunning
}
