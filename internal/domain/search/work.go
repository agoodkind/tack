// Package search defines durable indexing work and its production boundaries.
package search

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

var (
	// ErrWorkChanged means the claimed generation no longer matches source state.
	ErrWorkChanged = errors.New("search work changed")
	// ErrObsoleteWrite means OpenSearch rejected an older external version.
	ErrObsoleteWrite = errors.New("search write is obsolete")
	// ErrNoWork means no item of the requested class is currently claimable.
	ErrNoWork = errors.New("no search work is claimable")
	// ErrNoServingIndex means FoundationDB records no serving physical index yet.
	ErrNoServingIndex = errors.New("no serving search index is recorded")
)

// WorkClass separates live writes from maintenance activity.
type WorkClass string

const (
	// WorkClassLive indexes current content mutations.
	WorkClassLive WorkClass = "live"
	// WorkClassAccess refreshes opaque access metadata only.
	WorkClassAccess WorkClass = "access"
	// WorkClassCleanup retires obsolete page documents.
	WorkClassCleanup WorkClass = "cleanup"
	// WorkClassRescan schedules content work for every node of one organization.
	WorkClassRescan WorkClass = "rescan"
	// WorkClassRollout advances one authority's access policy rollout.
	WorkClassRollout WorkClass = "rollout"
	// WorkClassRebuild advances the one index replacement.
	WorkClassRebuild WorkClass = "rebuild"
	// WorkClassCopy writes one node's current pages to the replacement index.
	WorkClassCopy WorkClass = "copy"
)

// ScheduledWorkClasses returns every class a search worker rotates through,
// in the order that breaks weight ties.
func ScheduledWorkClasses() []WorkClass {
	return []WorkClass{
		WorkClassLive, WorkClassAccess, WorkClassCleanup, WorkClassRescan,
		WorkClassRollout, WorkClassRebuild, WorkClassCopy,
	}
}

// WorkPhase is the durable step of one claimed work item.
type WorkPhase string

const (
	// PhasePages writes pages or reads bounded batches.
	PhasePages WorkPhase = "pages"
	// PhaseRefresh refreshes the serving index after the final content page.
	PhaseRefresh WorkPhase = "refresh"
	// PhaseDependents schedules access work for policy dependents.
	PhaseDependents WorkPhase = "dependents"
	// PhaseMetadata indexes one bounded page of organization metadata before
	// a rescan reads nodes.
	PhaseMetadata WorkPhase = "metadata"
)

// Work is a leased durable indexing claim.
type Work struct {
	OrgID      uuid.UUID
	NodeID     uuid.UUID
	Generation int64
	// Revision is the content revision this work indexes or retires before.
	Revision   string
	Projection string
	Cursor     string
	// Ordinal is the next page ordinal the claim must register.
	Ordinal    uint64
	Phase      WorkPhase
	Deleted    bool
	EnqueuedAt time.Time
	Owner      string
	LeaseUntil time.Time
	Class      WorkClass
	// Target is the physical index this work writes: the serving index, or
	// the replacement index for copy work.
	Target string
	// Mirror is the replacement index that the worker also writes while a
	// replacement copies, verifies, or switches. Mirror is empty in every
	// other state.
	Mirror string
}

// WriteIntent binds one page write to its claimed generation and target.
type WriteIntent struct {
	Work       Work
	Page       node.ContentPage
	DocumentID string
}

// IssuedDocument identifies one registered page document.
type IssuedDocument struct {
	DocumentID string
	Revision   int64
	Ordinal    uint64
	Projection string
}

// DocumentBatch is one bounded read of issued documents.
type DocumentBatch struct {
	Documents []IssuedDocument
	Done      bool
}

// AccessIntent updates only the indexed generation and opaque access values.
type AccessIntent struct {
	Work      Work
	Documents []IssuedDocument
	Access    node.SearchAccess
}

// RetirementIntent replaces obsolete documents with text-free retired records.
type RetirementIntent struct {
	Work      Work
	Documents []IssuedDocument
}

// AccessPlan is the next step of one access work item after FoundationDB
// recorded its compiled access. Finished means no step remains.
type AccessPlan struct {
	Work     Work
	Finished bool
}

// AccessCheckpoint records the accepted prefix of one access-only batch.
type AccessCheckpoint struct {
	Access    node.SearchAccess
	Accepted  []IssuedDocument
	PagesDone bool
}

// ScanResult is one bounded FoundationDB scan page.
type ScanResult struct {
	Nodes      []uuid.UUID
	NextCursor string
	Done       bool
}

// ContentRequest selects one revision-bound projected page.
type ContentRequest struct {
	NodeID           uuid.UUID
	Cursor           string
	ProjectionConfig string
	AccessVersions   []string
	MaxBytes         int
	SearchGeneration int64
}
