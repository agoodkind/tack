package search

import (
	"context"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// ContentReader returns bounded pages and scans the nodes of one organization.
type ContentReader interface {
	Content(context.Context, ContentRequest) (node.ContentPage, error)
	ScanSearch(context.Context, uuid.UUID, string, int) (ScanResult, error)
}

// AccessReader compiles current access for a node. Its Dependents method
// reads one bounded page of the nodes that derive their access from that
// node under the recorded policy versions. For a deleted node, Dependents
// reads one bounded page of the node's recorded counterparts.
type AccessReader interface {
	Compile(context.Context, Work) (node.SearchAccess, error)
	Dependents(context.Context, Work, int) (node.IDPage, error)
}

// WorkLeases claims, yields, and releases durable work. Release records a
// failure and delays the next claim. It excludes the node instead when the
// counted failures of the work equal the attempt limit. Exclude retires the
// node's pages, records the exclusion, and clears the work.
type WorkLeases interface {
	Claim(context.Context, WorkClass, string, time.Duration) (Work, error)
	Yield(context.Context, Work) error
	Release(context.Context, Work, Failure) error
	Exclude(context.Context, Work, string) error
}

// ContentCheckpoints registers and checkpoints content pages.
type ContentCheckpoints interface {
	Register(context.Context, Work, node.ContentPage) (WriteIntent, error)
	CompletePage(context.Context, WriteIntent, string, bool) error
	CompleteRefresh(context.Context, Work) error
	Restart(context.Context, Work) error
}

// BatchCheckpoints reads issued documents and checkpoints bounded batches.
// BeginAccess records compiled access before the worker sends any page
// update.
type BatchCheckpoints interface {
	Documents(context.Context, Work, int) (DocumentBatch, error)
	CompleteRetirement(context.Context, Work, []IssuedDocument, bool) error
	BeginAccess(context.Context, Work, node.SearchAccess) (AccessPlan, error)
	CompleteAccess(context.Context, Work, AccessCheckpoint) error
	ScheduleDependents(context.Context, Work, node.IDPage) error
	IndexMetadata(context.Context, Work) error
	CompleteRescan(context.Context, Work, ScanResult) error
}

// WorkStore owns durable claims and checkpoints in FoundationDB.
type WorkStore interface {
	WorkLeases
	ContentCheckpoints
	BatchCheckpoints
}

// PageWriter writes, updates, or retires page documents in OpenSearch.
// Batch writes return the length of the accepted contiguous prefix.
type PageWriter interface {
	Put(context.Context, WriteIntent) (int, error)
	UpdateAccess(context.Context, AccessIntent) (int, error)
	Retire(context.Context, RetirementIntent) (int, error)
	Refresh(context.Context, string) error
}
