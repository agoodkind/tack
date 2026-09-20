# Durable Page Indexing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish indexing committed changes after failures, edits, and restarts.

**Architecture:** Every source mutation records durable work in its own FDB transaction. Workers register each page write before sending it, persist successful progress, and retire obsolete page IDs with persistent OpenSearch records that reject delayed writes.

**Tech Stack:** Go, FoundationDB, OpenSearch bulk API and external versioning.

**Spec:** [Durable indexing](../specs/2026-09-19-search-design.md#durable-indexing-and-recovery).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). No page count cap or whole-node collection is permitted. Requests remain below 500 documents and 5 MiB including encoded metadata.

## Review Focus

Test failed bulk items, a crash after indexing but before checkpointing, a shorter edit, source replacement during a read, and a delayed write after deletion.

---

## Task 4: Record and claim durable indexing work

Create these files:

```text
internal/domain/search/work.go                    work and checkpoint interfaces
internal/adapters/foundationdb/search_keys.go      search key families
internal/adapters/foundationdb/search_work.go      claims and atomic checkpoints
internal/adapters/foundationdb/search_schedule.go  transaction scheduling
internal/adapters/foundationdb/search_scan.go      resumable metadata rescan
internal/test/integration/search_work_test.go      real transactional recovery
```

Modify the existing node store's `Set`, `CreateAtomic`, `UpdateAtomic`, and
`Delete`; both standalone relationship mutations and `applyRelationshipChanges`;
and both metadata stores' `Set` and `Delete`. Add the store to `foundationdb.Stores`.
Use the existing versionstamped outbox pattern, but keep search work independent
of audit delivery. Search must not require an audit consumer to run.

Interfaces produced in `domain/search`:

```go
type Work struct {
    ID, Owner, Index, Revision, ProjectionVersion, Cursor, Phase string
    NodeID uuid.UUID
    Generation int64
    Deleted bool
}
type WriteIntent struct { WorkID, Owner, Index, DocumentID string; Generation int64; Page node.ContentPage }
type WorkStore interface {
    InitializeIndex(context.Context, string) error
    Claim(context.Context, string, time.Duration) (Work, error)
    Register(context.Context, Work, node.ContentPage) (WriteIntent, error)
    CompletePage(context.Context, WriteIntent, string, bool) error
    Refreshed(context.Context, Work) error
    Restart(context.Context, Work) error
    Release(context.Context, Work, string) error
    Retired(context.Context, Work, string, int) ([]WriteIntent, string, bool, error)
    CompleteCleanup(context.Context, Work, string, bool) error
}
var ErrNoWork = errors.New("no search work ready")
var ErrWorkChanged = errors.New("search work ownership or revision changed")
```

`Claim` returns `ErrNoWork` when no item is ready. `Register` checks current
revision, projection, owner, and generation transactionally before recording the
issued document ID. `CompletePage` advances only the matching generation.
`Restart` retires every issued ID and schedules the latest readable revision.
`Retired` returns a bounded page of issued IDs with an explicit end marker.
`Release` retains work and its last error for retry. Lease expiry permits a new
owner; it does not grant an old owner permission to register more writes.
InitializeIndex sets the first target only if no target exists; it cannot switch
an existing target. Claim also advances one bounded batch of pending organization
rescans before selecting a node job. It never returns an organization scan as a node.

- [ ] Add this real-store test before the runtime integration exists. The final runtime test repeats the outage through MCP.

```go
func TestSearchWorkSurvivesRestart(t *testing.T) {
    stores := newSearchStore(t)
    if err := stores.SearchWork.InitializeIndex(t.Context(), "test-pages"); err != nil { t.Fatal(err) }
    id := putSearchText(t, stores, "committed content")
    reopened, err := foundationdb.NewStores(testenv.FoundationDB(t), nil)
    if err != nil { t.Fatal(err) }
    work, err := reopened.SearchWork.Claim(t.Context(), "restarted-worker", 30*time.Second)
    if err != nil { t.Fatal(err) }
    if work.NodeID != id || work.Index != "test-pages" { t.Fatal("committed work lost") }
    page, err := reopened.Views.Content(t.Context(), node.ContentRequest{NodeID:id, MaxBytes:128})
    if err != nil { t.Fatal(err) }
    if err := reopened.Nodes.Delete(t.Context(), id, id); err != nil { t.Fatal(err) }
    if _, err := reopened.SearchWork.Register(t.Context(), work, page); !errors.Is(err, search.ErrWorkChanged) { t.Fatalf("obsolete registration: %v", err) }
}
```

- [ ] Run `^TestSearchWorkSurvivesRestart$`; expect the new store API to be missing.
- [ ] Add transaction-local scheduling at every mutation boundary. Store a versionstamped event with node ID and operation, and a current desired generation. For metadata or ancestry changes, also create a resumable organization scan job. Reindexing all nodes in that organization is acceptable; filtering by a seeded type is not. The scan uses bounded global node resolution reads and skips other organizations.
- [ ] Implement claims, registrations, and checkpoints using explicit FDB transaction retry loops. A mutation after a scan cursor passes still creates its own node event. A scan completion must not clear newer events. Bound job records and issued-ID reads; store progress in separate keys instead of an ever-growing array.
- [ ] Test `Set`, creation, update, deletion, metadata writes, and standalone relationship changes independently. Verify committed source changes retain pending work even when the search engine is unavailable. Verify failed source transactions leave no event.
- [ ] Run the work tests and `make check`; commit with subject `Record durable search work in FoundationDB mutations`.

## Task 5: Index pages and retire obsolete writes

Create:

```text
internal/adapters/search/opensearch_pages.go        typed page mapping and IDs
internal/adapters/search/opensearch_bulk.go         encoded byte bounds and item results
internal/service/search_worker.go                  page loop and error handling
internal/service/search_cleanup.go                 refresh and resumable retirement
internal/test/integration/search_recovery_test.go  crash and delayed-request tests
```

Consume `node.ContentReader`, `search.WorkStore`, and the native task's OpenSearchClient.
Produce PageWriter in `domain/search` and implement it on OpenSearchClient.
Produce Worker in `internal/service`:

```go
type PageWriter interface {
    Put(context.Context, WriteIntent) error
    Refresh(context.Context, string) error
    Retire(context.Context, []WriteIntent) error
}
type Worker struct { Reader node.ContentReader; Work search.WorkStore; Writer search.PageWriter; PageBytes int; ProjectionConfig string }
func (w *Worker) RunOne(ctx context.Context, work search.Work) error
```

- [ ] Add an integration test using real store mutations, WorkStore.Claim, and Worker.RunOne against the native engine. Index a node with more than 1,000 reader pages, shorten it, and delete it. Inspect the index after each exported operation. Require only the current revision's searchable text after convergence and no page text or embeddings after deletion. The runtime task repeats this through MCP.
- [ ] Run `^TestSearchRevisionCleanup$` and expect failure before the worker exists.
- [ ] Implement the worker loop with the following ordering. Add context and node ID to returned errors at the service boundary.

```go
func (w *Worker) RunOne(ctx context.Context, work search.Work) error {
    if work.Deleted || work.Phase == "cleaning" { return w.cleanup(ctx, work) }
    if work.Phase == "refreshing" { return w.finish(ctx, work) }
    request := node.ContentRequest{NodeID: work.NodeID, Cursor: work.Cursor, ProjectionConfig:w.ProjectionConfig, MaxBytes: w.PageBytes}
    for {
        page, err := w.Reader.Content(ctx, request)
        if errors.Is(err, node.ErrContentChanged) || errors.Is(err, domain.ErrNotFound) {
            return w.Work.Restart(ctx, work)
        }
        if err != nil { return err }
        intent, err := w.Work.Register(ctx, work, page)
        if err != nil { return err }
        if err := w.Writer.Put(ctx, intent); err != nil { return err }
        if err := w.Work.CompletePage(ctx, intent, page.NextCursor, page.Done); err != nil { return err }
        if page.Done { return w.finish(ctx, work) }
        request.Cursor = page.NextCursor
    }
}
func (w *Worker) finish(ctx context.Context, work search.Work) error {
    if err := w.Writer.Refresh(ctx, work.Index); err != nil { return err }
    if err := w.Work.Refreshed(ctx, work); err != nil { return err }
    return w.cleanup(ctx, work)
}
func (w *Worker) cleanup(ctx context.Context, work search.Work) error {
    cursor := ""
    for {
        intents, next, done, err := w.Work.Retired(ctx, work, cursor, 100)
        if err != nil { return err }
        if err := w.Writer.Retire(ctx, intents); err != nil { return err }
        if err := w.Work.CompleteCleanup(ctx, work, next, done); err != nil { return err }
        if done { return nil }
        cursor = next
    }
}
```

Deletion jobs skip Content and perform retirement. The coordinator runs cleanup
only after the new revision's final successful write and refresh. Persist separate
states for reading, refreshing, cleaning, and complete; a crash between them repeats
the unfinished state. A failed refresh cannot make a job complete.

- [ ] Construct document IDs from organization UUID, node UUID, revision, projection version, and ordinal with an unambiguous encoding. Include the fixed mapping fields from the spec and a Boolean `retired` field. Normal page writes use `retired:false` and external version 1 with `external_gte`; identical retries have identical content.
- [ ] Retire an issued ID by replacing its body with `{"retired":true}` at external version 2. The query filters `retired:false`. Keep this small record until its entire physical index is retired. It contains no source text or vectors. A delayed version-1 request must receive a version conflict even after normal delete-version retention would expire. An ownership check immediately before HTTP is insufficient by itself.
- [ ] Ensure registration and generation replacement conflict in FDB. Cleanup enumerates every registered ID, including writes that were issued but never acknowledged. No writer can register a new ID after its generation is retired. Disable automatic recreation of retired indexes and remove their write privileges before deletion.
- [ ] Reject page text over 4,096 UTF-8 bytes before indexing. Encode action and source lines before adding a document to a bulk request. Flush before either bulk bound would be exceeded. Reject an oversized encoded document explicitly and retain its work. Inspect every bulk item; checkpoint only a contiguous successful prefix. A version conflict is successful retirement only after FDB proves that exact intent obsolete.
- [ ] Pause a real worker after registration, complete a newer edit or deletion with another worker, and resume the old request against OpenSearch. Use process/container suspension and real HTTP, not a mocked writer. Assert the old text cannot reappear. Repeat after restarting both workers, and during partial bulk failure and cleanup.
- [ ] Run the [delayed-write test](2026-09-19-opensearch-fixtures.md#delayed-write-test-for-task-5) against the actual engine. Generation replacement invalidates the old claim immediately; a stale Release must not release the newer claim.
- [ ] Record peak in-flight bytes and page-read/write events. Assert a write precedes the final read and memory does not increase with page count at fixed concurrency. Run `^TestSearch(RevisionCleanup|DelayedWriter|PartialBulk|WorkSurvivesRestart)$` and `make check`; commit with subject `Index node pages with durable retries and retirement`.

Retirement records are an implementation detail required to reject delayed
writes. Add this field and retention rule to the indexed-page specification in
the implementation commit; preserve its requirement to remove obsolete text.

WriteIntent.Page exists only in memory. Persist the document identity, source
identity, cursor, and status, never a full text page in a work record. Retired
omits IDs already acknowledged by CompleteCleanup, including after restart.
