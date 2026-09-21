# Authorized Public Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return every matching, currently authorized node through durable ranked continuation.

**Architecture:** FoundationDB stores the query snapshot, exact rank position, visited nodes, replay records, and deadlines. OpenSearch excludes most forbidden candidates before ranking. The node reader performs the final current authorization check before each result is returned.

**Tech Stack:** Go, FoundationDB, MCP Streamable HTTP, Task 6 `Ranker`.

**Spec:** [Authorization and input validation](../specs/2026-09-19-search-acceptance.md#authorization-and-input-validation) and [permission expansion](../specs/2026-09-19-search-acceptance.md#permission-expansion).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Return at most 25 nodes after reading at most four batches of 100 raw hits. These response bounds do not limit total continuation. Check current FoundationDB state before returning every node.

## Review Focus

Test corrupt indexed access fields, revoked membership, moved scopes, byte-limited responses, lost responses, concurrent continuation, process changes, and four batches with no returnable result.

---

### Task 7: Authorize results and commit continuation

**Files:**

- Create: `internal/domain/search/session.go`
- Create: `internal/adapters/foundationdb/search_session.go`
- Create: `internal/adapters/foundationdb/search_replay.go`
- Create: `internal/adapters/mcp/tools/search_cursor.go`
- Create: `internal/adapters/mcp/tools/search_results.go`
- Create: `internal/adapters/mcp/tools/search_ranked.go`
- Test: `internal/test/integration/search_auth_test.go`
- Test: `internal/test/integration/search_cursor_test.go`

**Interfaces:**

- Consumes: Task 2 `AccessPolicy`, `ContentReader.Summary`, Task 6 `Ranker`, existing resolver and response byte helpers.
- Produces: durable sessions and the ranked `tack_search` registration that Task 8 activates while deleting the old implementation.

```go
type Session struct {
    ID, Principal, Generation uuid.UUID
    Binding [32]byte
    Secret [32]byte
    Query Query
    Snapshot Snapshot
    After json.RawMessage
    Version, NextPage uint64
    IdleExpiresAt, AbsoluteExpiresAt time.Time
    Closing bool
}
type PageCommit struct { After json.RawMessage; PITID string; Visited, Results []uuid.UUID; Done bool }
type SessionStore interface {
    Create(context.Context, Session) (Session, error)
    Load(context.Context, uuid.UUID) (Session, error)
    Replay(context.Context, uuid.UUID, uint64) (PageCommit, bool, error)
    HasVisited(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error)
    UpdatePIT(context.Context, uuid.UUID, uint64, string) (Session, error)
    CommitPage(context.Context, uuid.UUID, uint64, PageCommit) (Session, error)
    BeginCleanup(context.Context, uuid.UUID) error
    CleanupSlice(context.Context, uuid.UUID, int) (bool, error)
}
```

- [ ] **Step 1: Add the failing authenticated permission test.**

```go
func TestSearchAuthorizationRejectsCorruptIndex(t *testing.T) {
    fixture := newSearchMCPFixture(t, 128)
    eligible, foreign := putPermissionCorpus(t, fixture, "needle")
    corruptIndexedAccess(t, fixture.Adapter, foreign, eligible.Access)
    result := callEverySearchPage(t, fixture, "needle")
    if !slices.Contains(result.IDs, eligible.ID) { t.Fatal("eligible node missing") }
    if slices.Contains(result.IDs, foreign.ID) { t.Fatal("foreign node disclosed") }
}
```

- [ ] **Step 2: Run the authorization and cursor tests and record the failure.**

Run: `go test ./internal/test/integration -run '^TestSearch(Auth|Authorization|Cursor)' -count=1`

Expected: FAIL because durable sessions and the new public search path do not exist.

- [ ] **Step 3: Store bounded session state.**

Bind principal, query, filters, physical index, and search generation with deterministic serialization and SHA-256. Authenticate cursors with HMAC-SHA256. Store one bounded header, bounded token chunks, one key per visited node, and one replay record per response. Prefix each session key family with the first SHA-256 byte of the complete session ID. Store expiry entries inside that bucket.

- [ ] **Step 4: Resolve and filter before opening a snapshot.**

Resolve membership, entry point, scope, optional type, and query byte bounds. Call `AccessPolicy.Query` to build `Query.Access`. Reject undefined types and foreign scopes before `Ranker.Open`. The MCP handler must not construct permission clauses itself.

```go
accessFilter, err := deps.Access.Query(ctx, searchaccess.AccessRequest{
    PrincipalID: principal.ID, OrgID: workspace.OrgID, ScopeID: scopeID,
})
if err != nil { return classifyError(ctx, err), nil }
snapshot, err := deps.Ranker.Open(ctx, search.Query{Text: queryText, Index: activeIndex, NodeType: nodeType, Access: accessFilter})
```

- [ ] **Step 5: Advance only a bounded consumed prefix.**

Check replay before reading. For each raw batch, load visited flags only for that batch. For each new hit, read one bounded current summary and authorize it against current membership and ancestry. Mark deleted and unauthorized IDs visited. Stop before the first result that exceeds the response byte budget. Stop after 25 results or four raw batches. Return continuation after every nonterminal stop, including zero-result pages.

- [ ] **Step 6: Commit before responding.**

Commit at most 400 visited IDs, 25 result IDs, exact consumed sort JSON, replacement PIT ID, and replay bytes in one transaction. Renew only the 15-minute idle deadline. Preserve the two-hour absolute deadline. Concurrent calls for one page must conflict, then return the same replay record.

```go
committed, err := sessions.CommitPage(ctx, session.ID, session.Version, search.PageCommit{
    After: consumedAfter, PITID: currentPIT, Visited: visited, Results: resultIDs, Done: exhausted,
})
```

- [ ] **Step 7: Reauthorize replay and reject stale cursors.**

Reauthorize saved result IDs without advancing or renewing the session. Reject a changed principal, query binding, restored search generation, idle deadline, or absolute deadline. Close the PIT after committed completion. Mark the session closing before bounded cleanup. Delete the header last.

- [ ] **Step 8: Prove the permission boundary and final check separately.**

Reuse Task 6's raw-ranker test to prove selective pre-ranking filtering. In this task, corrupt indexed organization and scope values so a forbidden document passes OpenSearch. Require the current FoundationDB check to reject it. Count summary reads and require them to stay within four batches. A future permission model must pass both tests before release.

- [ ] **Step 9: Prove horizontal Tack continuation.**

Open a session on one Tack process. Alternate every continuation between two processes backed by the same FoundationDB and OpenSearch. Require exact replay, no duplicate node ID, complete exhaustion, and cleanup. Increase process count under fixed load and require throughput to increase without a stored-format change.

- [ ] **Step 10: Run the complete task checks.**

Run: `go test ./internal/test/integration -run '^TestSearch(Auth|Authorization|Cursor|DistinctNodes|PermissionFilter)' -count=1`

Run: `make check`

Expected: PASS with no skipped search test.

- [ ] **Step 11: Commit the task.**

```sh
git add internal/domain/search/session.go internal/adapters/foundationdb/search_session.go internal/adapters/foundationdb/search_replay.go internal/adapters/mcp/tools/search_ranked.go internal/adapters/mcp/tools/search_cursor.go internal/adapters/mcp/tools/search_results.go internal/test/integration/search_auth_test.go internal/test/integration/search_cursor_test.go
git commit -S -m "Return authorized search results with durable continuation" -m "Co-authored-by: Codex <noreply@openai.com>"
```
