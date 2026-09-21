# Ranked Authorized Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return ranked, currently authorized node IDs with complete durable continuation and one query embedding per search session.

**Architecture:** OpenSearch computes sparse query tokens once and ranks page documents after applying opaque access filters. FoundationDB stores the physical index, point in time, exact sort position, tokens, visited nodes, replay records, and deadlines. The MCP handler rereads current FoundationDB permission data before returning each node. Runtime configuration keeps the public handler unavailable until the release plan activates it.

**Tech Stack:** Go, FoundationDB, MCP, official OpenSearch Go client v4.7.3, ML Commons.

**Spec:** [Ranking and continuation](../specs/2026-09-19-search-design.md#ranking-and-continuation), [authorization](../specs/2026-09-19-search-acceptance.md#authorization-and-input-validation), and [permission expansion](../specs/2026-09-19-search-acceptance.md#permission-expansion).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Complete ranking, sessions, authorization, MCP registration, configuration, and runtime wiring in one task. Keep the default public state unavailable until the release plan completes the empty rebuild and acceptance checks.

## Review Focus

Test duplicate-heavy nodes, exact sort values, one prediction per session, forbidden high-scoring pages, corrupt indexed access, revoked membership, byte-limited responses, replay, concurrent continuation, process changes, expiration, and complete traversal beyond 1,000 raw matches.

---

### Task 1: Build and connect ranked authorized search

**Files:**

- Create: `internal/domain/search/query.go`
- Create: `internal/domain/search/session.go`
- Create: `internal/domain/search/filter.go`
- Create: `internal/domain/node/summary.go`
- Create: `internal/adapters/search/opensearch_query.go`
- Create: `internal/adapters/search/opensearch_snapshot.go`
- Create: `internal/adapters/foundationdb/node_summary.go`
- Create: `internal/adapters/foundationdb/search_session.go`
- Create: `internal/adapters/foundationdb/search_replay.go`
- Modify: `internal/searchaccess/access.go`
- Create: `internal/adapters/mcp/tools/search_cursor.go`
- Create: `internal/adapters/mcp/tools/search_results.go`
- Modify: `internal/adapters/mcp/tools/search.go`
- Modify: `internal/adapters/mcp/server.go`
- Modify: `internal/config/config.go`
- Modify: `internal/runtime/graph.go`
- Test: `internal/test/integration/search_ranking_test.go`
- Test: `internal/test/integration/search_permission_filter_test.go`
- Test: `internal/test/integration/search_auth_test.go`
- Test: `internal/test/integration/search_cursor_test.go`

**Core interfaces:**

```go
type Query struct { Text, Index, NodeType string; Access AccessFilter }
type Snapshot struct { PITID, Index string; QueryTokens json.RawMessage }
type RankHit struct { NodeID uuid.UUID; Sort json.RawMessage }
type RankBatch struct { Hits []RankHit; PITID string }
type Ranker interface { Open(context.Context, Query) (Snapshot, error); Read(context.Context, Query, Snapshot, json.RawMessage) (RankBatch, error); Close(context.Context, Snapshot) error }
type SummaryReader interface { Summary(context.Context, uuid.UUID, int) (node.Summary, error) }
type QueryAccessPolicy interface { EntryAuthority(context.Context, uuid.UUID) (uuid.UUID, error); Query(context.Context, searchaccess.AccessRequest) (AccessFilter, error) }
type SessionStore interface { Create(context.Context, Session) (Session, error); Load(context.Context, uuid.UUID) (Session, error); Replay(context.Context, uuid.UUID, uint64) (PageCommit, bool, error); HasVisited(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error); CommitPage(context.Context, uuid.UUID, uint64, PageCommit) (Session, error); BeginCleanup(context.Context, uuid.UUID) error; CleanupSlice(context.Context, uuid.UUID, int) (bool, error) }
```

- [ ] **Establish the failing public-boundary tests.** Add six accepted semantic query pairs with at least 150 distractors and one lexical-only control. Add 1,501 distinct nodes and 36,000 duplicate pages for one node. Require complete continuation across every eligible node. Add authenticated MCP tests for filtering, current authorization, replay, process changes, and cleanup.

- [ ] **Compute query tokens once.** Send `{"text_docs":[query.Text]}` to the pinned model through one narrow `opensearch.Request` implementation. Use `opensearch.Do` and `opensearch.ParseError`. Require exactly one nonempty map of finite, nonnegative weights. Store the exact token JSON in the session. Reject tokens above the configured session byte bound.

- [ ] **Open one stable search snapshot.** Resolve the public alias to one physical index. Verify its mapping and model identity. Create a point in time through the typed API. Save replacement point-in-time IDs from each read. Return an explicit restart error when OpenSearch no longer recognizes the point in time.

- [ ] **Compile caller access as opaque values.** Extend `PolicySet` and `OrgScopeCompiler` with `EntryAuthority` and `Query`. Return sorted opaque caller keys for one active version. Use the same `EncodeKey` contract as indexing. Search code never decodes the key or switches on permission types.

- [ ] **Filter before ranking.** Resolve the authenticated entry point's permission authority and active version from FoundationDB. Ask `QueryAccessPolicy.Query` for sorted opaque caller keys. Add one exact `access.versions` term, one `access.keys` terms clause, `retired:false`, and the optional node type before semantic or lexical scoring. Reject empty, duplicate, unsorted, unsupported, or oversized access values.

- [ ] **Use native sparse ranking.** Build the search path with `SearchReq.GetRequest`. Send a typed body with `size:100`, saved point in time, `_source:["node_id"]`, lexical `multi_match`, nested `neural_sparse`, and sorting by descending `_score`, ascending `node_id`, then ascending `_shard_doc`. Use the saved `query_tokens`. Do not use collapse, `knn`, dense scripts, hybrid candidate windows, or application filtering as the primary permission filter.

- [ ] **Preserve exact continuation.** Store all three original sort values as raw JSON. Return them unchanged as `search_after`. Treat only an empty raw batch as engine exhaustion. A short batch or a batch containing only visited node IDs remains nonterminal. Read at most four batches of 100 raw page hits for one public response, then return a continuation cursor even when no node is returnable.

- [ ] **Store bounded durable sessions.** Bind the principal, permission authority, query, access version and keys, physical index, and search generation through deterministic serialization and SHA-256. Authenticate cursors with HMAC-SHA256. Store a bounded header, chunked query-token bytes, one key per visited node, one replay record per response, and bucketed expiry and presence keys. Use a 15-minute idle deadline and a two-hour absolute deadline.

- [ ] **Authorize every result from current source data.** Implement the bounded `SummaryReader` against current FoundationDB identity, ancestry, membership, and relationship data. For each new node ID, reject deleted or currently forbidden nodes even if OpenSearch returned them. Stop before the first result that would exceed the response byte budget. Return at most 25 distinct nodes.

- [ ] **Commit before responding.** Commit the exact consumed sort position, replacement point-in-time ID, at most 400 visited IDs, at most 25 result IDs, completion state, and replay bytes in one FoundationDB transaction. Concurrent calls for one session version conflict and then return the committed replay. Renew only the idle deadline.

- [ ] **Reauthorize replay and clean up safely.** Reread current permission data for replayed result IDs without advancing or renewing the session. Reject a changed principal, query binding, restored search generation, or expired deadline. Close the point in time after committed completion. Mark the session as closing, delete bounded child records, and delete the header last.

- [ ] **Prove both permission layers.** Give forbidden pages stronger lexical matches and require raw ranker output to exclude them. Then corrupt one indexed access key so a forbidden page passes OpenSearch and require the final FoundationDB check to reject it. Revoke membership after opening a session and require later results and replay to reject newly forbidden nodes.

- [ ] **Register the production handler.** Add `OPENSEARCH_PUBLIC_ENABLED` with a default of false, plus explicit request-byte, response-byte, session-byte, deadline, batch, and result configuration. Construct the ranker, session store, and handler in `internal/runtime/graph.go`. `tack_search` keeps returning exactly `Search is temporarily unavailable.` while the flag is false. When the flag is true, the same registration runs ranked search. The flag changes only public dispatch. Index workers continue while it is false. No second tool or alternate handler exists.

- [ ] **Prove horizontal Tack scaling.** Open a session on one Tack process and alternate every continuation between two processes backed by the same FoundationDB and OpenSearch. Require exact replay, no duplicate node, complete exhaustion, and cleanup. Increase Tack processes under fixed query load and require higher throughput without changing stored formats.

- [ ] **Run and commit the slice.** Run the focused real-dependency integration tests. Run `make build` once and fix every failure. Review `git diff --check` and the complete diff. Commit all files together because the production handler depends on the complete slice:

```sh
git add internal
git commit -S -m "Add ranked authorized OpenSearch queries" -m "Co-authored-by: Codex <noreply@openai.com>"
```
