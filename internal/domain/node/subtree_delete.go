package node

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SubtreeDeleteJob stores durable progress for a node deletion. Each direct
// child moves to the deleted node's parent when NodeType metadata allows
// that parent. Otherwise, the job deletes the child and its descendants.
// Each step reads and rewrites this record in one bounded FoundationDB
// transaction. Another runner can continue from the record after a crash.
type SubtreeDeleteJob struct {
	// ID identifies the job. It is a UUIDv7.
	ID uuid.UUID `json:"id"`
	// OrgID is the organization of the deleted subtree.
	OrgID uuid.UUID `json:"org_id"`
	// RootID is the node the caller asked to delete.
	RootID uuid.UUID `json:"root_id"`
	// RootParentID identifies the root's hierarchy parent when the job started.
	// It is uuid.Nil when the root does not have a parent. Eligible direct
	// children of the root move to this parent.
	RootParentID uuid.UUID `json:"root_parent_id" exhaustruct:"optional"`
	// ActorID identifies the user or operator who requested the deletion.
	// A node that the job moves records ActorID as its last updater.
	ActorID uuid.UUID `json:"actor_id" exhaustruct:"optional"`
	// Operation stores the encoded audit operation of the initiating request.
	// Every step records node and relationship changes with the actor and
	// operation ID from Operation, including steps that the resume loop runs.
	Operation json.RawMessage `json:"operation,omitempty" exhaustruct:"optional"`
	// AuditTemplate stores the root delete audit template in older job records
	// that lack Operation. When Operation is empty, each step builds the audit
	// operation from AuditTemplate with
	// [goodkind.io/tack/internal/audit.LegacyJobOperation]. New jobs leave
	// AuditTemplate empty.
	//
	// Remove AuditTemplate and LegacyJobOperation after job records with
	// audit_template no longer exist in any environment.
	AuditTemplate json.RawMessage `json:"audit_template,omitempty" exhaustruct:"optional"`
	// Stack lists the nodes on the path from the root to the node the next
	// step examines. The first entry is the root.
	Stack []uuid.UUID `json:"stack"`
	// Deleted counts the nodes the job deleted.
	Deleted int `json:"deleted"`
	// Moved counts the direct children of the root that the job moved to the
	// root's parent.
	Moved int `json:"moved" exhaustruct:"optional"`
	// UpdatedAt records the store clock's commit time for the last step.
	// The resume loop uses the age of this value to identify stale jobs.
	UpdatedAt time.Time `json:"updated_at"`
	// FinishedAt records the commit time of the step that deleted the root.
	// It remains zero while the job runs. A finished record remains readable
	// until the resume loop clears it.
	FinishedAt time.Time `json:"finished_at,omitzero" exhaustruct:"optional"`
}

// Finished reports whether the job deleted its root.
//
// The method checks FinishedAt rather than the progress counters.
//
//   - A nonzero FinishedAt returns true.
//   - A zero FinishedAt returns false.
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

// State returns the job's deletion state.
//
// The method checks [SubtreeDeleteJob.Finished] to select the state.
//
//   - A finished job returns [SubtreeDeleteFinished].
//   - An unfinished job returns [SubtreeDeleteRunning].
func (j *SubtreeDeleteJob) State() SubtreeDeleteState {
	if j.Finished() {
		return SubtreeDeleteFinished
	}

	return SubtreeDeleteRunning
}
