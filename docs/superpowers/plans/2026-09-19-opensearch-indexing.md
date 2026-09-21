# Bounded Page Indexing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Index, update access on, and retire every node page through bounded recoverable worker slices.

**Architecture:** One content claim reads and writes a bounded page prefix, checkpoints the last successful page, then yields. One access claim partially updates the generic access object on bounded issued page IDs without submitting text. All operations use one monotonic FoundationDB generation, so delayed content, access, and retirement writes cannot replace newer state.

**Tech Stack:** Go, FoundationDB, official OpenSearch Go client v4.7.3, typed bulk API.

**Spec:** [Durable indexing and bounded work](../specs/2026-09-19-search-design.md#durable-indexing-and-bounded-work).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). One claim processes at most 32 pages, 5 MiB of encoded requests, or two seconds before starting another remote call. One request may remain in flight until its ten-second timeout. Cleanup processes at most 100 IDs.

## Review Focus

Test delayed content and access writes, partial bulk failures, shortened nodes,
deleted nodes, final-page writes, access changes with the model unavailable, long
nodes, work-class fairness, and constant memory.

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

- Consumes: Task 1 `Adapter`, Task 3 `ContentReader`, Task 4 `WorkStore`.
- Produces: `PageWriter` and `Worker` for runtime and rebuild tasks.

```go
type PageWriter interface {
    Put(context.Context, search.WriteIntent) error
    UpdateAccess(context.Context, search.AccessIntent) error
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

```go
type AccessIntent struct {
    WorkID, Owner, Index, DocumentID string
    Generation int64
    Access node.SearchAccess
}
```

- [ ] **Step 1: Add the failing delayed-writer and fairness tests.**

Pause a registered generation-1 content request. Complete a generation-2 access
update with another worker. Send the paused content request and require a version
conflict. Repeat with a paused access request and newer content request. Complete
deletion with another worker and require every older request to conflict. Add a
node with more than 1,000 pages and require small live nodes to finish between
its slices.

```go
if err := client.Put(ctx, oldIntent); !errors.Is(err, search.ErrObsoleteWrite) {
    t.Fatalf("Put error = %v, want ErrObsoleteWrite", err)
}
document, err := client.GetDocument(ctx, oldIntent.Index, oldIntent.DocumentID)
if err != nil { t.Fatal(err) }
if !document.Retired || document.PageText != "" { t.Fatalf("stale text restored: %#v", document) }
```

- [ ] **Step 2: Record the deferred failure contract.**

Task 13 runs `^TestSearch(DelayedWriter|RevisionCleanup|WorkerFairness)$` against the completed branch. The tests must fail when stale writes, retirement, checkpoints, or bounded fairness is broken. Do not start OpenSearch or FoundationDB during this coding task.

- [ ] **Step 3: Define deterministic page documents and IDs.**

Construct the document ID from organization, node, revision, text projection
version, and page ordinal through length-prefixed bytes and base64url encoding.
Permission versions do not participate in the document ID. Validate nonempty,
sorted, unique `ContentPage.Access.Versions` and `Keys` plus a positive generation.
The worker must not derive or decode access rules.

```go
type pageDocument struct {
    NodeID string `json:"node_id"`
    NodeType string `json:"node_type"`
    Revision string `json:"revision"`
    ProjectionVersion string `json:"projection_version"`
    Name string `json:"name"`
    PageText string `json:"page_text,omitempty"`
    SearchGeneration int64 `json:"search_generation"`
    Access accessDocument `json:"access"`
    Ordinal uint64 `json:"ordinal"`
    Retired bool `json:"retired"`
}
type accessDocument struct {
    Versions []string `json:"versions"`
    Keys []string `json:"keys"`
    Generation int64 `json:"generation"`
}
```

Copy `Access` from `SearchAccess` without interpreting key contents. Reject empty
versions, empty keys, duplicates, unsorted values, or a generation that differs
from `Work.Generation`. Let the strict OpenSearch mapping reject every other access field. This keeps the page writer independent from the permission model.

- [ ] **Step 4: Encode bounded typed bulk requests.**

Reject `page_text` above 4,096 UTF-8 bytes. Encode the action and source lines before admission. Flush before 500 documents or 5 MiB. Submit deterministic NDJSON through `opensearchapi.Client.Bulk`. Set each index, update, and retirement action to `version:Work.Generation` and `version_type:external_gte`. Inspect every `BulkRespItem`. Return the contiguous successful prefix and the first item error. Treat a lower-generation conflict as `ErrObsoleteWrite`. Do not use `opensearchutil.BulkIndexer`.

- [ ] **Step 5: Implement one bounded worker slice.**

For `WorkContent`, read the next page. Register its intent before sending it.
Write it, then checkpoint its cursor. Stop before the next read when any page,
byte, or time limit would be exceeded. Require a successful page write before
reading the following page. When `Done` is true, refresh in a separate resumable
phase.

```go
for pages < w.Slice.Pages && encodedBytes < w.Slice.EncodedBytes {
    if pages > 0 && time.Since(started) >= w.Slice.Duration { break }
    request.AccessVersions, request.SearchGeneration = work.AccessVersions, work.Generation
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

- [ ] **Step 6: Partially update access without reading page text.**

For `WorkAccess`, call `AccessPolicy.Index` once with `Work.AccessVersions` and
`Work.Generation`. Read at most 100 current issued document IDs. Encode bulk
`update` actions that replace only `search_generation` and `access` and use the work generation with
`external_gte`. Do not call `ContentReader.Content` and do not include
`page_text`, `name`, or semantic information. Checkpoint the contiguous successful
prefix, then yield. A missing or retired current ID restarts the node's content
work from current FoundationDB state.

```json
{"update":{"_index":"physical-index","_id":"page-id","version":42,"version_type":"external_gte"}}
{"doc":{"search_generation":42,"access":{"versions":["org-scope-v1","permission-v2"],"keys":["org-scope-v1:opaque","permission-v2:opaque"],"generation":42}},"detect_noop":true}
```

- [ ] **Step 7: Retire issued IDs with external versioning.**

Write active pages with the current FoundationDB generation and `external_gte`.
Replace issued IDs with a text-free retirement record at the deleting or replacing
generation. Keep retirement records until physical-index deletion. Cleanup reads
at most 100 IDs, checkpoints the batch, then yields. Deletion work starts in
cleanup.

- [ ] **Step 8: Handle generation replacement and partial failures.**

Make replacement conflict with registration in FoundationDB. Enumerate acknowledged and unacknowledged issued IDs. Checkpoint only the contiguous successful bulk prefix. Disable automatic index recreation and revoke writes before physical-index deletion.

- [ ] **Step 9: Add fairness and bounded-resource coverage.**

Add a test that runs live content, access, cleanup, rescan, and rebuild work
together. Undeploy the model before access work and require the update to finish
with unchanged text, chunks, and sparse weights. Record page reads, writes,
encoded bytes, slice duration, oldest work age, and peak memory. Require every
class to progress within its configured age and memory to remain constant at
fixed concurrency. Task 13 executes this test.

- [ ] **Step 10: Run the serial coding checks.**

Run: `go test ./internal/test/integration -run '^$' -count=1`

Run: `make check`

Expected: PASS after compiling the integration package without executing its tests. Task 13 runs delayed requests and real recovery.

- [ ] **Step 11: Commit the task.**

```sh
git add internal/domain/search/writer.go internal/adapters/search/opensearch_pages.go internal/adapters/search/opensearch_bulk.go internal/service/search_worker.go internal/service/search_cleanup.go internal/test/integration/search_recovery_test.go internal/test/integration/search_fairness_test.go
git commit -S -m "Index node pages with bounded durable work slices" -m "Co-authored-by: Codex <noreply@openai.com>"
```
