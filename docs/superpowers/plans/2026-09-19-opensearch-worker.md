# Durable page indexing implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Finish indexing committed changes after failures, edits, restarts, and arbitrarily large nodes without starving other work.

**Architecture:** Every source mutation records durable desired state in the same FoundationDB transaction. Workers execute bounded page or cleanup slices, persist progress, then yield. Retirement records reject delayed writes.

**Tech Stack:** Go, FoundationDB, official OpenSearch Go client v4.7.3, typed bulk API, and external versioning.

**Spec:** [Durable indexing and bounded work](../specs/2026-09-19-search-design.md#durable-indexing-and-bounded-work).

## Global constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints).
No page count cap or whole-node collection is permitted. One claim processes at
most 32 pages or 5 MiB of encoded requests. It starts no new remote operation after
its two-second slice deadline. One request can remain in flight until its ten-second
timeout. Cleanup processes at most 100 IDs. Every unfinished slice persists progress
and yields.

## Task 4: Record and claim durable work

Create:

```text
internal/domain/search/work.go                    work and checkpoint contracts
internal/adapters/foundationdb/keys.go             extend the central key catalog
internal/adapters/foundationdb/search_work.go      claims and checkpoints
internal/adapters/foundationdb/search_schedule.go  transactional scheduling
internal/adapters/foundationdb/search_scan.go      bounded metadata rescans
internal/test/integration/search_work_test.go      real recovery and lifecycle
```

Modify node create, set, update, and delete operations; standalone relationship
mutations; and metadata set and delete operations. Use a search outbox independent
of audit delivery.

```go
type Work struct {
    ID, Owner, Index, Revision, ProjectionVersion, Cursor, Phase string
    NodeID uuid.UUID
    Generation int64
    Deleted bool
}
type SliceLimit struct { Pages int; EncodedBytes int; Duration time.Duration }
type WriteIntent struct {
    WorkID, Owner, Index, DocumentID string
    Generation int64
    Page node.ContentPage
}
type WorkStore interface {
    InitializeIndex(context.Context, string) error
    Claim(context.Context, string, time.Duration) (Work, error)
    Register(context.Context, Work, node.ContentPage) (WriteIntent, error)
    CompletePage(context.Context, WriteIntent, string, bool) error
    Refreshed(context.Context, Work) error
    Restart(context.Context, Work) error
    Yield(context.Context, Work) error
    Release(context.Context, Work, string) error
    Retired(context.Context, Work, string, int) ([]WriteIntent, string, bool, error)
    CompleteCleanup(context.Context, Work, string, bool) error
}
```

`Claim` returns one node job. It never returns a whole organization scan. A bounded
scan step creates or refreshes node jobs before claim selection. Claims use separate
limits for live mutations, cleanup, rescans, and rebuild work so one class cannot
consume every worker.

- [ ] Add `TestSearchWorkSurvivesRestart` with real FoundationDB. Commit a node while
  OpenSearch is unavailable, reopen stores, claim it, then delete it. The stale claim
  must fail registration with `ErrWorkChanged`.
- [ ] Run `^TestSearchWorkSurvivesRestart$` and record the missing API failure.
- [ ] Add search key constants and tuple packers to the central `keys.go` catalog. Preserve its `withPrefix` and `stripPrefix` rules. Do not create an independent search key registry.
- [ ] Schedule search work inside the source mutation's existing FoundationDB transaction. Store one current desired generation per node and a versionstamped event. Metadata and ancestry changes create bounded organization scan jobs without filtering on seeded types. Do not open a nested transaction.
- [ ] Use the established `db.Transact` or bounded `CreateTransaction` and `OnError` pattern for claims, registrations, checkpoints, and yields. A scan completion cannot clear a newer event. Lease expiry cannot permit the old owner to register another page. Do not add a generic search retry package.
- [ ] Keep job headers, cursors, errors, and issued IDs in separate bounded keys.
  Replace obsolete desired generations instead of appending history.
- [ ] When a generation completes and retirement finishes, delete its claim, cursor,
  error, completed events, obsolete generations, and acknowledged issued-ID records.
  Preserve only current desired state and IDs needed to reject delayed writes.
- [ ] Repeat hundreds of edits and compare FDB key-family counts before and after
  convergence. Counts must depend on current pages and pending work, not edit history.
- [ ] Test node, relationship, metadata, deletion, failed source transaction, scan
  overlap, claim expiry, and restart paths independently.
- [ ] Run work tests and `make check`. Commit with subject
  `Record durable search work in FoundationDB mutations`.

## Task 5: Index and retire bounded slices

Create:

```text
internal/adapters/search/opensearch_pages.go        page mapping and IDs
internal/adapters/search/opensearch_bulk.go         byte bounds and item results
internal/service/search_worker.go                  bounded page slices
internal/service/search_cleanup.go                 bounded retirement slices
internal/test/integration/search_recovery_test.go  crashes and delayed writes
internal/test/integration/search_fairness_test.go  work-class progress
```

Produce:

```go
type PageWriter interface {
    Put(context.Context, WriteIntent) error
    Refresh(context.Context, string) error
    Retire(context.Context, []WriteIntent) error
}
type Worker struct {
    Reader node.ContentReader
    Work search.WorkStore
    Writer search.PageWriter
    PageBytes int
    ProjectionConfig string
    Slice SliceLimit
}
func (w *Worker) RunOne(context.Context, search.Work) error
```

`RunOne` performs exactly one bounded slice. Reading stops before the next page when
any slice limit would be exceeded. The worker checkpoints the last successful page,
calls `Yield`, and returns. It never loops through an entire large node in one claim.

Refreshing is its own resumable phase. Cleanup requests at most 100 issued IDs,
retires and checkpoints that batch, then yields unless completion is true. Deletion
jobs start in cleanup. A failed refresh or retirement never marks work complete.

- [ ] Add a real integration test that indexes more than 1,000 reader pages, shortens
  the node, then deletes it. Call `RunOne` repeatedly through actual claims. Require
  other live nodes to complete between slices of the large node.
- [ ] Run `^TestSearch(RevisionCleanup|WorkerFairness)$` and record the failure.
- [ ] Construct unambiguous document IDs from organization, node, revision,
  projection, and ordinal. Normal writes use external version 1 with `external_gte`.
  Identical retries must contain identical source.
- [ ] Retire an issued ID by replacing it with `{"retired":true}` at external version
  2. Keep that small record until the physical index is deleted. It contains no text
  or sparse fields. A delayed version-1 request must receive a version conflict.
- [ ] Count retired pages, measure the oldest retirement, and read physical index
  bytes. Crossing any configured threshold must schedule a replacement rebuild.
  Require validated disk reserves for serving, replacement, and retiring indexes.
  When the reserve is unavailable, leave new index work pending without rejecting
  the authoritative source mutation.
- [ ] Make generation replacement conflict with registration in FoundationDB.
  Cleanup must enumerate every issued ID, including unacknowledged writes. Revoke
  writes and disable automatic index recreation before deleting a physical index.
- [ ] Reject `page_text` above 4,096 UTF-8 bytes. Encode action and source lines before
  bulk admission. Flush before 500 documents or 5 MiB. Inspect every item and
  checkpoint only a contiguous successful prefix.
- [ ] Submit deterministic NDJSON with `opensearchapi.Client.Bulk`. Configure typed partial-error reporting and inspect every `BulkRespItem` against its work intent. Do not use `opensearchutil.BulkIndexer`; its asynchronous queues and callbacks cannot preserve one known durable slice and exact FoundationDB checkpoints.
- [ ] Pause a real worker after registration. Complete a newer edit or deletion with
  another worker, then resume the old HTTP request. Old text must not reappear.
  Repeat across process restart, partial bulk failure, refresh, and cleanup.
- [ ] Run live mutation, cleanup, rescan, and rebuild work together at fixed worker
  counts. Record start and completion times by class. Every class must make progress
  within its declared age threshold.
- [ ] Record page reads, writes, encoded bytes, slice duration, and peak memory.
  Require a write before the final read and constant memory at fixed concurrency.
- [ ] Record FoundationDB work with `telemetry.FDBOp`. Record OpenSearch operations with `telemetry.Op`, existing spans, the context logger, and selected official client metrics. Do not add a search metric registry or direct service-level `expvar` metrics.
- [ ] Run recovery and fairness tests plus `make check`. Commit with subject
  `Index node pages with bounded durable work slices`.

`WriteIntent.Page` exists only in memory. Persistent work stores identity, cursor,
phase, and status. No work record stores a full text page.
