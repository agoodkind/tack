# Ranked node search implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Return every matching, currently authorized node through stable ranked continuation without a total-result cap.

**Architecture:** OpenSearch infers sparse query weights once. Every ranked page request reuses those opaque weights against `rank_features`. A point in time freezes index contents. FoundationDB stores position, visited nodes, replay records, and deadlines.

**Tech Stack:** Go, OpenSearch conventional neural sparse search, FoundationDB, MCP Streamable HTTP.

**Spec:** [Ranking and continuation](../specs/2026-09-19-search-design.md#ranking-and-continuation).

## Global constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints).
Each public response returns at most 25 nodes and reads at most four engine batches
of at most 100 page matches. These bounds never limit total continuation results.
Each engine request starts with the next configured OpenSearch address. Search
requests use the next address after a connection failure.

## Task 6: Rank and paginate native sparse matches

Create:

```text
internal/domain/search/query.go                    typed query and result contracts
internal/adapters/search/opensearch_query.go       structured sparse requests
internal/adapters/search/opensearch_snapshot.go    query inference and point in time
internal/test/integration/search_ranking_test.go  relevance and full pagination
```

Produce:

```go
type Query struct { Text, NodeType, Index string; OrgID, ScopeID uuid.UUID }
type Snapshot struct { PITID, Index string; QueryTokens json.RawMessage }
type RankHit struct { NodeID uuid.UUID; Sort json.RawMessage }
type RankBatch struct { Hits []RankHit; PITID string }
type Ranker interface {
    Open(context.Context, Query) (Snapshot, error)
    Read(context.Context, Query, Snapshot, json.RawMessage) (RankBatch, error)
    Close(context.Context, Snapshot) error
}
```

`Open` resolves the physical index, calls native sparse inference once, validates a
nonempty finite token-weight map, and opens a point in time. Store the encoded map
without interpreting token keys. Split persisted bytes across bounded FDB values if
needed and reject output above the configured total session bound. `Read` reuses the
map and persists replacement PIT IDs before authorization or rendering.

- [ ] Add `TestSearchSemanticRelevance` with the six acceptance pairs and at least
  150 distractors. Require every target within the first 25 distinct nodes across
  three repeats and a reindex. A lexical-only control must miss one pair.
- [ ] Add `TestSearchDistinctNodes` with 1,501 nodes and 36,000 page documents for
  one node across three primary shards. Traverse every raw batch and compare first
  node occurrences with a bounded test-only collapse reference. Require every node.
- [ ] Run `^TestSearch(SemanticRelevance|DistinctNodes)$` and record the existing
  interface failure.
- [ ] Infer query weights with
  `POST /_plugins/_ml/_predict/sparse_encoding/{model_id}` and typed
  `{"text_docs":[query.Text]}`. Require one response map with finite nonnegative
  numbers. Persist its exact encoded representation for continuation.
- [ ] Open
  `POST /{physical_index}/_search/point_in_time?keep_alive=15m&allow_partial_pit_creation=false`.
  Send ranked requests to `POST /_search?allow_partial_search_results=false`:

```json
{
  "size": 100,
  "pit": {"id": "saved-pit-id", "keep_alive": "15m"},
  "_source": ["node_id"],
  "sort": [
    {"_score": "desc"},
    {"node_id": "asc"},
    {"_shard_doc": "asc"}
  ],
  "query": {"bool": {
    "filter": [
      {"term": {"org_id": "resolved-org"}},
      {"term": {"scope_ids": "resolved-scope"}},
      {"term": {"retired": false}}
    ],
    "minimum_should_match": 1,
    "should": [
      {"multi_match": {"query": "query-text", "fields": ["name^3", "page_text"]}},
      {"nested": {
        "path": "sparse_embedding",
        "score_mode": "max",
        "query": {"neural_sparse": {
          "sparse_embedding.sparse_encoding": {"query_tokens": {}}
        }}
      }}
    ]
  }}
}
```

Substitute validated fields and the stored token map through typed structures and
`json.Marshal`. Add the optional metadata-defined type filter. Never insert query
text into JSON manually.

- [ ] Require exactly three sort values. `_shard_doc` prevents equal score and node
  ID values from skipping page documents. Preserve exact sort JSON in `search_after`.
- [ ] Omit collapse. Persist one visited record per node and retain each node's first
  page match. Lower page matches remain raw continuation work.
- [ ] Preserve regressions for approximate hybrid starvation and dense exact scans.
  Reject `script_score`, `knn`, hybrid normalization, fixed candidate windows, and
  result-size requests above the raw batch bound.
- [ ] Profile the production query. Require Lucene `FeatureQuery` operations over
  `rank_features` and no dense script. Record query inference separately from page
  search latency.
- [ ] Prove the point in time excludes later writes. Require only an empty raw batch
  to set exhaustion. A short batch and an empty deduplicated batch remain nonterminal.
- [ ] Stop before consuming a hit and require the same hit after retry. Delete the
  point in time and require a restart error.
- [ ] Run ranking tests and `make check`. Commit with subject
  `Paginate node page matches with OpenSearch sparse ranking`.

## Task 7: Authorize results and commit continuation

Create:

```text
internal/domain/search/session.go                  bounded session state
internal/adapters/foundationdb/search_session.go   headers and visited records
internal/adapters/foundationdb/search_replay.go    replay and expiry cleanup
internal/adapters/mcp/tools/search_cursor.go       cursor binding and progress
internal/adapters/mcp/tools/search_results.go      current summaries and rendering
internal/test/integration/search_auth_test.go      authenticated isolation
internal/test/integration/search_cursor_test.go    replay, bytes, and deadlines
```

Replace [MCP search](../../../internal/adapters/mcp/tools/search.go). Remove facet
counts and the full-view fetch loop. Consume Ranker and `NodeReader.Summary`.

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
type PageCommit struct {
    After json.RawMessage
    PITID string
    Visited, Results []uuid.UUID
    Done bool
}
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

Bind principal, query, filters, physical index, and search generation with
deterministic serialization and SHA-256. Authenticate cursors with HMAC-SHA256.
Store one bounded header, bounded query-token chunks, one key per visited node, and
one bounded replay record per response. Never read all visited IDs in one transaction.

`CommitPage` writes at most 400 visited IDs, 25 result IDs, consumed sort values,
latest PIT ID, replay data, and the next page atomically. It renews only the
15-minute inactivity deadline. The absolute two-hour deadline never changes.
`HasVisited` accepts only the current bounded engine batch. `UpdatePIT` replaces
only the PIT ID. It preserves sort position, page number, and both deadlines.

- [ ] Build authenticated fixtures through real user, token, membership, metadata,
  FoundationDB, and MCP operations. Load no product seed.
- [ ] Resolve membership, entry point, scope, type, and query byte bounds before
  `Open`. Undefined types and foreign scopes fail before any engine request.
- [ ] Check committed replay before advancing. Reauthorize saved result IDs. Replay
  never advances or renews a deadline.
- [ ] For each raw hit, check persisted and request-local visited records, then read
  one bounded current summary. Mark deleted or unauthorized nodes visited. Treat
  storage and metadata failures as errors.
- [ ] Consume a hit only after confirming it fits the response. Stop at 25 results,
  the byte budget, or four engine batches. Return continuation after any nonterminal
  stop, including a response with zero nodes.
- [ ] Commit the consumed prefix before responding. Resolve uncertain commits from
  replay records. Concurrent callers must conflict and return the same committed page.
- [ ] Reject mismatched, idle-expired, absolute-expired, and restored-generation
  cursors. Close a point in time after committed completion. Bounded cleanup deletes
  token chunks, visited records, replay records, and the header last.
- [ ] Corrupt indexed authorization fields, revoke membership, move scopes, lose a
  response, restart the process, and force four visited-only batches. Require current
  authorization, exact replay, and eventual continuation.
- [ ] Run `^TestSearch(Auth|Cursor|DistinctNodes)` and `make check`. Commit with
  subject `Return authorized search results with durable continuation`.
