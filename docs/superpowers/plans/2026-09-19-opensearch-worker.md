# Durable Search Work Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Record every search update transactionally and claim it after failures or restarts.

**Architecture:** Source mutations write desired search state in the same FoundationDB transaction. Workers claim one node generation from one stable hash bucket. Content and resource-access changes use distinct work kinds under one monotonic generation. Metadata, ancestry, and permission-policy transitions create bounded scan work instead of one organization-sized job.

**Tech Stack:** Go, FoundationDB, existing transaction and tuple helpers.

**Spec:** [Durable indexing](../specs/2026-09-19-search-design.md#durable-indexing-and-bounded-work).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Use the central key catalog. Keep live mutations, access updates, cleanup, rescans, and rebuilds in separate claim classes. Never store a full text page or complete node access set in work state.

## Review Focus

Test failed source transactions, claim expiry, stale owners, content and access
write races, membership-only changes, metadata scan overlap, repeated edits,
deletion, and restart.

---

### Task 4: Record and claim durable search work

**Files:**

- Create: `internal/domain/search/work.go`
- Create: `internal/adapters/foundationdb/search_work.go`
- Create: `internal/adapters/foundationdb/search_schedule.go`
- Create: `internal/adapters/foundationdb/search_scan.go`
- Modify: `internal/adapters/foundationdb/keys.go`
- Modify: `internal/adapters/foundationdb/node.go`
- Modify: `internal/adapters/foundationdb/node_delete.go`
- Modify: `internal/adapters/foundationdb/relationship.go`
- Modify: `internal/adapters/foundationdb/property.go`
- Modify: `internal/adapters/foundationdb/node_type.go`
- Test: `internal/test/integration/search_work_test.go`

**Interfaces:**

- Consumes: Task 3 content revisions and projection epochs.
- Produces: `WorkStore`, `Work`, `WriteIntent`, and bounded issued-page reads for Task 5 and Task 9.

```go
type Work struct {
    ID, Owner, Index, Revision, ProjectionVersion, Cursor, Phase string
    NodeID uuid.UUID
    AccessVersions []string
    Generation, AccessStateGeneration int64
    Class WorkClass
    Kind WorkKind
    Deleted bool
}
type WorkClass string
const ( WorkLive WorkClass = "live"; WorkCleanup WorkClass = "cleanup"; WorkRescan WorkClass = "rescan"; WorkRebuild WorkClass = "rebuild" )
type WorkKind string
const ( WorkContent WorkKind = "content"; WorkAccess WorkKind = "access" )
type SliceLimit struct { Pages int; EncodedBytes int; Duration time.Duration }
type WriteIntent struct {
    WorkID, Owner, Index, DocumentID string
    Generation int64
    Page node.ContentPage
}
type IssuedPage struct { DocumentID string; Generation int64 }
type WorkStore interface {
    InitializeIndex(context.Context, string) error
    Claim(context.Context, WorkClass, string, time.Duration) (Work, error)
    Register(context.Context, Work, node.ContentPage) (WriteIntent, error)
    CompletePage(context.Context, WriteIntent, string, bool) error
    Refreshed(context.Context, Work) error
    Restart(context.Context, Work) error
    Yield(context.Context, Work) error
    Release(context.Context, Work, string) error
    Retired(context.Context, Work, string, int) ([]WriteIntent, string, bool, error)
    CompleteCleanup(context.Context, Work, string, bool) error
    AccessBatch(context.Context, Work, string, int) ([]IssuedPage, string, bool, error)
    CompleteAccess(context.Context, Work, string, bool) error
}
```

- [ ] **Step 1: Add the failing restart and stale-owner test.**

```go
func TestSearchWorkSurvivesRestart(t *testing.T) {
    stores := newSearchStore(t)
    id := putSearchText(t, stores, "durable search text")
    first, err := stores.SearchWork.Claim(t.Context(), search.WorkLive, "worker-a", time.Minute)
    if err != nil { t.Fatal(err) }
    page, err := stores.Views.Content(t.Context(), node.ContentRequest{NodeID: id, MaxBytes: 128})
    if err != nil { t.Fatal(err) }
    reopened := reopenSearchStore(t, stores)
    if err := reopened.Nodes.Delete(t.Context(), id, id); err != nil { t.Fatal(err) }
    if _, err := reopened.SearchWork.Register(t.Context(), first, page); !errors.Is(err, search.ErrWorkChanged) {
        t.Fatalf("Register error = %v, want ErrWorkChanged", err)
    }
}
```

- [ ] **Step 2: Record the deferred failure contract.**

Task 13 runs `^TestSearchWorkSurvivesRestart$` against the completed branch. The test must fail when durable work, leases, restart recovery, or work-class fairness is absent. Do not start FoundationDB during this coding task.

- [ ] **Step 3: Add bounded key families to the central catalog.**

Add desired generation, event, claim, cursor, error, issued ID, scan, and class-age keys to `keys.go`. Prefix claimable work with the first SHA-256 byte of organization and node identity. Keep job headers, large cursors, errors, and issued IDs in separate bounded values. Use `withPrefix`, `stripPrefix`, and tuple encoding.

```go
func searchBucket(orgID, nodeID uuid.UUID) byte {
    digest := sha256.Sum256(append(orgID[:], nodeID[:]...))
    return digest[0]
}
```

- [ ] **Step 4: Schedule desired state inside source transactions.**

Call an internal `scheduleSearchContent(tr, nodeID, revision,
projectionVersion, deleted)` from node create, set, update, and delete. Each scheduler reads the authority's access state in the same transaction and records its sorted write versions and state generation. Expose
`scheduleSearchAccess(tr, nodeID)` and `scheduleSearchAccessScan(tr, rootID)` to
the permission service. Each helper increments the same per-node search
generation inside the caller's transaction.

The current ancestry writer schedules content work because ancestry changes text
and current access. A future data-driven permission service calls the access
helpers when a resource grant changes. A principal membership change calls no
document scheduler because the query policy reads current permission nodes.
Generic relationship storage must not classify permission node or relationship
types. The scheduler accepts the existing transaction. It must not call
`db.Transact` or open another transaction.

- [ ] **Step 5: Implement lease-safe claims and registrations.**

Use the established FoundationDB transaction retry pattern. `Claim` reads one
work class, kind, and bucket. `Register` verifies desired generation, owner,
lease, revision, text projection version, access-state generation, write versions, and target index in the same
transaction that records the issued document ID. Access work reads current
issued IDs through `AccessBatch` and checkpoints through `CompleteAccess`.
It does not register a new document ID. A stale
owner returns `ErrWorkChanged`.

```go
if desired.Generation != work.Generation || claim.Owner != work.Owner || claim.ExpiresAt.Before(now) {
    return search.WriteIntent{}, search.ErrWorkChanged
}
```

- [ ] **Step 6: Add bounded organization scans.**

Each scan transaction reads a bounded raw-key range and creates or refreshes
content or access jobs. Persist the next raw key and work kind. A completion
transaction compares the scan event version before clearing it, so a newer
metadata, ancestry, resource-permission, or policy-version event remains pending.

- [ ] **Step 7: Remove obsolete history after convergence.**

After current generation indexing and retirement finish, delete its claim, cursor, error, completed event, obsolete desired generation, and acknowledged issued-ID records. Preserve current desired state and issued IDs still needed to reject delayed writes.

- [ ] **Step 8: Add lifecycle and scale coverage.**

Test node writes, relationships, metadata, deletion, failed source transactions,
scan overlap, claim expiry, restart, and hundreds of edits. Change only a
principal membership and require zero content or access jobs. Change a resource
grant and require access jobs without content jobs. Race older content and access
claims against newer generations. Compare key-family counts before and after
convergence. Increase worker count under a fixed workload and require higher
throughput without a key-format change.

- [ ] **Step 9: Run the serial coding checks.**

Run: `go test ./internal/test/integration -run '^$' -count=1`

Run: `make check`

Expected: PASS after compiling the integration package without executing its tests. Task 13 runs the restart and bounded-state checks.

- [ ] **Step 10: Commit the task.**

```sh
git add internal/domain/search/work.go internal/adapters/foundationdb/keys.go internal/adapters/foundationdb/search_work.go internal/adapters/foundationdb/search_schedule.go internal/adapters/foundationdb/search_scan.go internal/adapters/foundationdb/node.go internal/adapters/foundationdb/node_delete.go internal/adapters/foundationdb/relationship.go internal/adapters/foundationdb/property.go internal/adapters/foundationdb/node_type.go internal/test/integration/search_work_test.go
git commit -S -m "Record durable search work in FoundationDB mutations" -m "Co-authored-by: Codex <noreply@openai.com>"
```
