# Bounded Page Indexing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Index and retire every node page through bounded recoverable worker slices.

**Architecture:** One worker claim reads and writes a bounded page prefix, checkpoints the last successful page, then yields. Retirement replaces every issued page ID with a higher-version text-free record so delayed writes cannot restore deleted text.

**Tech Stack:** Go, FoundationDB, official OpenSearch Go client v4.7.3, typed bulk API.

**Spec:** [Durable indexing and bounded work](../specs/2026-09-19-search-design.md#durable-indexing-and-bounded-work).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). One claim processes at most 32 pages, 5 MiB of encoded requests, or two seconds before starting another remote call. One request may remain in flight until its ten-second timeout. Cleanup processes at most 100 IDs.

## Review Focus

Test delayed writes, partial bulk failures, shortened nodes, deleted nodes, final-page writes, long nodes, work-class fairness, and constant memory.

---

### Task 5: Index and retire bounded slices

**Files:**

- Create: `internal/adapters/search/opensearch_pages.go`
- Create: `internal/adapters/search/opensearch_bulk.go`
- Create: `internal/domain/search/writer.go`
- Create: `internal/service/search_worker.go`
- Create: `internal/service/search_cleanup.go`
- Test: `internal/test/integration/search_recovery_test.go`
- Test: `internal/test/integration/search_fairness_test.go`

**Interfaces:**

- Consumes: Task 1 `Adapter`, Task 2 `ContentReader`, Task 4 `WorkStore`.
- Produces: `PageWriter` and `Worker` for runtime and rebuild tasks.

```go
type PageWriter interface {
    Put(context.Context, search.WriteIntent) error
    Refresh(context.Context, string) error
    Retire(context.Context, []search.WriteIntent) error
}
type Worker struct {
    Reader node.ContentReader
    Work search.WorkStore
    Writer search.PageWriter
    PageBytes int
    ProjectionConfig string
    Slice search.SliceLimit
}
func (w *Worker) RunOne(context.Context, search.Work) error
```

- [ ] **Step 1: Add the failing delayed-writer and fairness tests.**

Pause a registered version-1 request. Complete deletion with another worker. Send the paused request and require a version conflict. Add a node with more than 1,000 pages and require small live nodes to finish between its slices.

```go
if err := client.Put(ctx, oldIntent); !errors.Is(err, search.ErrObsoleteWrite) {
    t.Fatalf("Put error = %v, want ErrObsoleteWrite", err)
}
document, err := client.GetDocument(ctx, oldIntent.Index, oldIntent.DocumentID)
if err != nil { t.Fatal(err) }
if !document.Retired || document.PageText != "" { t.Fatalf("stale text restored: %#v", document) }
```

- [ ] **Step 2: Run the tests and record the missing-worker failure.**

Run: `go test ./internal/test/integration -run '^TestSearch(DelayedWriter|RevisionCleanup|WorkerFairness)$' -count=1`

Expected: FAIL because `PageWriter` and `Worker.RunOne` do not exist.

- [ ] **Step 3: Define deterministic page documents and IDs.**

Construct the document ID from organization, node, revision, projection version, and page ordinal through length-prefixed bytes and base64url encoding. Validate `ContentPage.Access.Version` against the index configuration. Copy its declared fields into `Access` without naming permission fields. The worker must not derive access rules.

```go
type pageDocument struct {
    NodeID string `json:"node_id"`
    NodeType string `json:"node_type"`
    Revision string `json:"revision"`
    ProjectionVersion string `json:"projection_version"`
    Name string `json:"name"`
    PageText string `json:"page_text,omitempty"`
    Access map[string][]string `json:"access"`
    Ordinal uint64 `json:"ordinal"`
    Retired bool `json:"retired"`
}
```

Build `Access` from `SearchAccess.Fields`. Reject empty names, duplicate names, empty value lists, and a version that differs from `Adapter.IndexInfo`. Let the strict OpenSearch mapping reject undeclared fields. This keeps the page writer independent from the permission model.

- [ ] **Step 4: Encode bounded typed bulk requests.**

Reject `page_text` above 4,096 UTF-8 bytes. Encode the action and source lines before admission. Flush before 500 documents or 5 MiB. Submit deterministic NDJSON through `opensearchapi.Client.Bulk`. Inspect every `BulkRespItem`. Return the contiguous successful prefix and the first item error. Do not use `opensearchutil.BulkIndexer`.

- [ ] **Step 5: Implement one bounded worker slice.**

Read the next page. Register its intent before sending it. Write it, then checkpoint its cursor. Stop before the next read when any page, byte, or time limit would be exceeded. Require a successful page write before reading the following page. When `Done` is true, refresh in a separate resumable phase.

```go
for pages < w.Slice.Pages && encodedBytes < w.Slice.EncodedBytes {
    if pages > 0 && time.Since(started) >= w.Slice.Duration { break }
    page, err := w.Reader.Content(ctx, request)
    if err != nil { return err }
    intent, err := w.Work.Register(ctx, work, page)
    if err != nil { return err }
    if err := w.Writer.Put(ctx, intent); err != nil { return err }
    if err := w.Work.CompletePage(ctx, intent, page.NextCursor, page.Done); err != nil { return err }
    pages++
    if page.Done { return w.refresh(ctx, work) }
    request.Cursor = page.NextCursor
}
return w.Work.Yield(ctx, work)
```

- [ ] **Step 6: Retire issued IDs with external versioning.**

Write active pages with external version 1 and `external_gte`. Replace issued IDs with `{"retired":true}` at external version 2. Keep retirement records until physical-index deletion. Cleanup reads at most 100 IDs, checkpoints the batch, then yields. Deletion work starts in cleanup.

- [ ] **Step 7: Handle generation replacement and partial failures.**

Make replacement conflict with registration in FoundationDB. Enumerate acknowledged and unacknowledged issued IDs. Checkpoint only the contiguous successful bulk prefix. Disable automatic index recreation and revoke writes before physical-index deletion.

- [ ] **Step 8: Measure fairness and bounded resources.**

Run live mutation, cleanup, rescan, and rebuild work together. Record page reads, writes, encoded bytes, slice duration, oldest work age, and peak memory. Require every class to progress within its configured age and memory to remain constant at fixed concurrency.

- [ ] **Step 9: Run the complete task checks.**

Run: `go test ./internal/test/integration -run '^TestSearch(DelayedWriter|RevisionCleanup|WorkerFairness|Recovery)$' -count=1`

Run: `make check`

Expected: PASS with the delayed request rejected by OpenSearch versioning.

- [ ] **Step 10: Commit the task.**

```sh
git add internal/domain/search/writer.go internal/adapters/search/opensearch_pages.go internal/adapters/search/opensearch_bulk.go internal/service/search_worker.go internal/service/search_cleanup.go internal/test/integration/search_recovery_test.go internal/test/integration/search_fairness_test.go
git commit -S -m "Index node pages with bounded durable work slices" -m "Co-authored-by: Codex <noreply@openai.com>"
```
