package search

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrRolloutInProgress means the authority already runs another access
// policy transition.
var ErrRolloutInProgress = errors.New("search access rollout is already in progress")

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
	// After activation it waits until no session reads the previous version.
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
	Generation, PermissionEventVersion     int64
}

// Activated reports whether the candidate already serves new sessions.
func (r AccessRollout) Activated() bool {
	return r.CandidateVersion != "" && r.ActiveVersion == r.CandidateVersion
}

// BeginAccessRollout starts one candidate version for one authority.
// ExpectedGeneration must equal the stored rollout generation.
type BeginAccessRollout struct {
	AuthorityID        uuid.UUID
	CandidateVersion   string
	ExpectedGeneration int64
}

// RollbackAccessRollout requests rollback of one authority's access
// policy rollout. ExpectedGeneration must equal the stored rollout
// generation.
type RollbackAccessRollout struct {
	AuthorityID        uuid.UUID
	ExpectedGeneration int64
}

// RolloutDocument is one issued page document a verification step reads.
type RolloutDocument struct {
	NodeID   uuid.UUID
	Document IssuedDocument
}

// DocumentAccess is the stored state and access versions of one page
// document. Found is false when OpenSearch has no document with the ID.
// Generation is the access generation the page stores.
type DocumentAccess struct {
	Found, Retired bool
	Versions       []string
	Generation     int64
}

// RolloutStep is the outcome of one checkpointed rollout step.
type RolloutStep uint8

const (
	// RolloutContinue means the worker releases the claim with no retry
	// delay. The next claim of the rollout runs the next step.
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
