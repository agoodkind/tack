package search

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrRolloutInProgress means the authority already runs another access
// policy transition.
var ErrRolloutInProgress = errors.New("search access rollout is already in progress")

const accessRolloutTargetPrefix = "foundationdb/search/access-rollout/"

// AccessRolloutTargetReference returns an authority's rollout audit target.
//
// The reference identifies durable FoundationDB access rollout state.
//
//   - The reference appends authority to the access rollout target prefix.
func AccessRolloutTargetReference(authority string) string {
	return accessRolloutTargetPrefix + authority
}

// ErrNoRolloutInProgress means the authority does not have an access
// policy rollout to roll back.
var ErrNoRolloutInProgress = errors.New("authority does not have a rollout in progress")

// ErrRetirementStarted means retirement has started and the authority
// writes one version. Rollback cannot restore the other version.
var ErrRetirementStarted = errors.New("cannot roll back after retirement of one version has started")

// ErrRolloutChanged means another operator changed the rollout after the
// caller read it.
var ErrRolloutChanged = errors.New("rollout changed after the caller read it")

// AccessPhase is the durable step of one authority's access policy rollout.
type AccessPhase string

const (
	// AccessStable writes and queries one active version.
	AccessStable AccessPhase = "stable"
	// AccessBackfill schedules access work that writes the candidate beside
	// the active version on every current page.
	AccessBackfill AccessPhase = "backfill"
	// AccessVerifying reads every current page and requires both versions.
	// After activation, it waits until sessions stop reading the previous version.
	AccessVerifying AccessPhase = "verifying"
	// AccessRetiring removes the previous version from every current page.
	AccessRetiring AccessPhase = "retiring"
)

// AccessRollout is the durable access policy state of one permission
// authority. Generation guards rollout checkpoints. PermissionEventVersion is
// the permission event boundary the current verification started from.
type AccessRollout struct {
	AuthorityID                            uuid.UUID
	ActiveVersion, CandidateVersion        string
	PreviousVersion                        string
	WriteVersions                          []string
	Phase                                  AccessPhase
	ScanCursor, VerifyCursor, RetireCursor string
	ScanComplete                           bool
	// AffectedPass reports that the full verification pass has completed and
	// the rollout now verifies nodes in the affected set before activation.
	AffectedPass                       bool
	Generation, PermissionEventVersion int64
	// Initiation identifies the operation that began the current rollout.
	Initiation Initiation `exhaustruct:"optional"`
}

// Activated reports whether the candidate serves new sessions.
//
// The method compares the candidate with the active version.
//
//   - A nonempty CandidateVersion equal to ActiveVersion returns true.
//   - An empty or different CandidateVersion returns false.
func (r AccessRollout) Activated() bool {
	return r.CandidateVersion != "" && r.ActiveVersion == r.CandidateVersion
}

// BeginAccessRollout starts one candidate version for one authority.
// ExpectedGeneration must equal the stored rollout generation.
type BeginAccessRollout struct {
	AuthorityID        uuid.UUID
	CandidateVersion   string
	ExpectedGeneration int64
	// Initiation identifies the operation that requests the rollout.
	Initiation Initiation `exhaustruct:"optional"`
}

// RollbackAccessRollout requests rollback of one authority's access
// policy rollout. ExpectedGeneration must equal the stored rollout
// generation.
type RollbackAccessRollout struct {
	AuthorityID        uuid.UUID
	ExpectedGeneration int64
	// Initiation identifies the operation that requests the rollback.
	Initiation Initiation `exhaustruct:"optional"`
}

// AffectedNodes contains the recording counter and affected set entries for
// the current rollout generation. Recorded counts recordings rather than
// distinct nodes.
type AffectedNodes struct {
	Recorded int64
	Nodes    []uuid.UUID
}

// RolloutDocument is one issued page document a verification step reads.
type RolloutDocument struct {
	NodeID   uuid.UUID
	Document IssuedDocument
}

// DocumentAccess stores one page document's state and access versions.
// Found is false when OpenSearch does not contain the document ID.
// Generation records the page's access generation.
type DocumentAccess struct {
	Found, Retired bool
	Versions       []string
	Generation     int64
}

// RolloutStep is the outcome of one checkpointed rollout step.
type RolloutStep uint8

const (
	// RolloutContinue releases the claim without a retry delay. The next claim
	// runs the next rollout step.
	RolloutContinue RolloutStep = iota + 1
	// RolloutWait means the step waits for pending page work or sessions.
	RolloutWait
	// RolloutComplete means the authority returned to the stable phase.
	RolloutComplete
)

// AccessRolloutStore persists rollouts and checkpoints each bounded step.
type AccessRolloutStore interface {
	Current(context.Context, uuid.UUID) (AccessRollout, error)
	Begin(context.Context, BeginAccessRollout) (AccessRollout, error)
	Rollback(context.Context, RollbackAccessRollout) (AccessRollout, error)
	CompleteScan(context.Context, Work, AccessRollout, ScanResult) error
	VerifyDocuments(context.Context, Work, AccessRollout, int) ([]RolloutDocument, bool, error)
	CompleteVerify(context.Context, Work, AccessRollout, []RolloutDocument, map[string]DocumentAccess, bool) (RolloutStep, error)
	BeginRetire(context.Context, Work, AccessRollout) error
	Wait(context.Context, Work) error
}

// AccessStateReader reads the active access version of one authority.
type AccessStateReader interface {
	ActiveAccessVersion(context.Context, uuid.UUID) (string, error)
}

// AccessVersionSessions reports whether a session still reads an access
// version under one authority.
type AccessVersionSessions interface {
	HasActiveAccessVersion(context.Context, uuid.UUID, string) (bool, error)
}

// DocumentAccessReader reads the stored access of exact page documents in
// one physical index.
type DocumentAccessReader interface {
	DocumentAccess(context.Context, string, []string) (map[string]DocumentAccess, error)
}
