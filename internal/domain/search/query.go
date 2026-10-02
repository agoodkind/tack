package search

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

var (
	// ErrSnapshotLost means OpenSearch no longer recognizes the point in time.
	// The caller must start a new search.
	ErrSnapshotLost = errors.New("search snapshot is no longer available")
	// ErrInvalidQuery means Tack rejected the query text or filters before
	// inference, rejected the token weights that inference returned, or
	// rejected a session record above its byte bound.
	ErrInvalidQuery = errors.New("search query is invalid")
	// ErrEngineUnavailable means OpenSearch kept rejecting query inference
	// with its memory circuit breaker after every bounded retry.
	ErrEngineUnavailable = errors.New("search engine is temporarily unavailable")
)

// Query is one normalized search bound to a physical index, caller access,
// and the restore epoch in Generation. A restored index replacement
// increments the restore epoch. Every earlier session then fails its binding
// check.
type Query struct {
	Text, Index, NodeType string
	Access                AccessFilter
	Generation            int64
}

// GenerationReader reads the current restore epoch.
type GenerationReader interface {
	SearchRestoreEpoch(context.Context) (int64, error)
}

// Snapshot is one stable OpenSearch view. QueryTokens is the exact opaque
// token-weight JSON the pinned model returned for the query text.
type Snapshot struct {
	PITID, Index string
	QueryTokens  json.RawMessage
}

// RankHit is one raw page match with its exact original sort values.
type RankHit struct {
	NodeID uuid.UUID
	Sort   json.RawMessage
}

// RankBatch is one raw engine batch and the replacement point-in-time ID.
type RankBatch struct {
	Hits  []RankHit
	PITID string
}

// Ranker opens, reads, and closes ranked search snapshots.
type Ranker interface {
	Open(context.Context, Query) (Snapshot, error)
	Read(context.Context, Query, Snapshot, json.RawMessage) (RankBatch, error)
	Close(context.Context, Snapshot) error
}

// SummaryReader loads current summaries for one bounded batch of node IDs in
// input order. The int bounds each summary's display bytes.
type SummaryReader interface {
	Summaries(context.Context, []uuid.UUID, int) ([]node.SummaryResult, error)
}

// ServingIndexReader reads the serving physical index recorded in FoundationDB.
type ServingIndexReader interface {
	ServingSearchIndex(context.Context) (string, error)
}
