package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrRebuildInProgress means another index replacement already runs or its
// old index still waits for retirement.
var ErrRebuildInProgress = errors.New("an index replacement is already in progress")

// ErrIndexNotFound means the physical index of an index operation does not
// exist.
var ErrIndexNotFound = errors.New("the search index does not exist")

// ErrIndexNotReady means an index health wait ended before the index health
// was green.
var ErrIndexNotReady = errors.New("the search index is not green")

// ReplacementMode selects how the replacement index receives its documents.
type ReplacementMode string

const (
	// ReplacementFull builds an empty index from FoundationDB pages.
	ReplacementFull ReplacementMode = "full"
	// ReplacementSplit splits the serving index into more primary shards and
	// keeps every stored embedding.
	ReplacementSplit ReplacementMode = "split"
)

// RebuildState is the durable step of the one index replacement.
type RebuildState string

const (
	// RebuildCreating creates the empty target, or pauses claims, blocks
	// source writes, and splits the source.
	RebuildCreating RebuildState = "creating"
	// RebuildCopying schedules one copy item for every FoundationDB node.
	RebuildCopying RebuildState = "copying"
	// RebuildVerifying reads every current page from the target.
	RebuildVerifying RebuildState = "verifying"
	// RebuildSwitching pauses claims and switches the public alias.
	RebuildSwitching RebuildState = "switching"
	// RebuildRetiring deletes the old index after its sessions end.
	RebuildRetiring RebuildState = "retiring"
	// RebuildFailed restores source writes and deletes the failed target.
	RebuildFailed RebuildState = "failed"
)

// Rebuild is the durable state of the one index replacement. From the
// copying state through the switch, every search write also writes the
// target. The switch replays no journal.
type Rebuild struct {
	ID                                     uuid.UUID
	Mode                                   ReplacementMode
	State                                  RebuildState
	SourceIndex, TargetIndex               string
	ScanCursor, VerifyCursor, Failure      string
	PrimaryShards, RoutingShards, Replicas int
	ScanComplete, Paused, Restored         bool
	PausedAt                               time.Time
	Generation                             int64
}

// Mirror returns the replacement index in the copying, verifying, and
// switching states. Every search write in those states also writes that
// index. Mirror returns an empty string in every other state.
func (r Rebuild) Mirror() string {
	switch r.State {
	case RebuildCopying, RebuildVerifying, RebuildSwitching:
		return r.TargetIndex
	case RebuildCreating, RebuildRetiring, RebuildFailed:
	}
	return ""
}

// BeginRebuild requests one replacement with its target topology.
type BeginRebuild struct {
	Mode                                   ReplacementMode
	PrimaryShards, RoutingShards, Replicas int
	Restored                               bool
	Reason                                 string
}

// Validate rejects a mode other than full or split, an invalid shard
// topology, and a restored split.
func (r BeginRebuild) Validate() error {
	if r.Mode != ReplacementFull && r.Mode != ReplacementSplit {
		return fmt.Errorf("replacement mode %q is not full or split", r.Mode)
	}
	if r.PrimaryShards < 1 || r.RoutingShards < r.PrimaryShards || r.Replicas < 0 {
		return fmt.Errorf("replacement topology %d/%d/%d is invalid", r.PrimaryShards, r.RoutingShards, r.Replicas)
	}
	if r.RoutingShards%r.PrimaryShards != 0 {
		return fmt.Errorf("replacement routing shards %d must be divisible by primaries %d", r.RoutingShards, r.PrimaryShards)
	}
	if r.Restored && r.Mode != ReplacementFull {
		return errors.New("a restored replacement must rebuild from FoundationDB")
	}
	return nil
}

// RebuildDocument is one current page the verification step reads from the
// target, with the write versions its authority requires.
type RebuildDocument struct {
	OrgID, NodeID uuid.UUID
	Document      IssuedDocument
	Versions      []string
}

// RebuildBatch is one bounded verification read. NextCursor marks the
// position after the last issued document the read examined. The read
// examines documents of older revisions that cleanup still retires.
type RebuildBatch struct {
	Documents  []RebuildDocument
	NextCursor string
	Done       bool
}

// RebuildLifecycle begins, advances, switches, and finishes the replacement.
type RebuildLifecycle interface {
	CurrentRebuild(context.Context) (Rebuild, bool, error)
	BeginRebuild(context.Context, BeginRebuild) (Rebuild, error)
	AdvanceRebuild(context.Context, Work, Rebuild, Rebuild) error
	CompleteSwitch(context.Context, Work, Rebuild) error
	FinishRebuild(context.Context, Work, Rebuild) error
	WaitRebuild(context.Context, Work) error
}

// RebuildProgress schedules copies, verifies the target, and retires the
// sessions of the old index in bounded steps.
type RebuildProgress interface {
	CopyNextNodes(context.Context, Work, Rebuild) error
	CopiesPending(context.Context) (bool, error)
	VerifyRebuildDocuments(context.Context, Work, Rebuild, int) (RebuildBatch, error)
	CompleteRebuildVerify(context.Context, Work, Rebuild, RebuildBatch, map[string]DocumentAccess) (bool, error)
	RetireIndexSessions(context.Context, string, int) (bool, error)
}

// RebuildStore persists the replacement in FoundationDB.
type RebuildStore interface {
	RebuildLifecycle
	RebuildProgress
}

// IndexReplacer performs the physical index operations of a replacement
// through the official client. SetWriteBlock returns [ErrIndexNotFound]
// when the index does not exist. WaitGreen waits at most the given duration
// and returns [ErrIndexNotReady] when the index is not green by then.
type IndexReplacer interface {
	CreateReplacement(context.Context, string, int, int, int) error
	SplitIndex(context.Context, string, string, int, int) error
	SetWriteBlock(context.Context, string, bool) error
	PublicAliasTarget(context.Context) (string, error)
	SwitchPublicAlias(context.Context, string, string) error
	WaitGreen(context.Context, string, time.Duration) error
	DeleteIndex(context.Context, string) error
}

// RetentionStats reports the retired pages and primary bytes of one index.
type RetentionStats struct {
	RetiredPages, PrimaryBytes int64
}

// RetentionReader reads the retention statistics of one physical index.
type RetentionReader interface {
	IndexRetention(context.Context, string) (RetentionStats, error)
}
