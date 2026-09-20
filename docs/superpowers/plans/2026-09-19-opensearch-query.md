# Ranked Node Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return every matching, currently authorized node through stable ranked continuation without a total-result cap.

**Architecture:** OpenSearch freezes the index view and returns ranked page matches in bounded batches. FoundationDB stores the consumed position and visited node IDs. The node reader supplies current authorization and bounded summaries before MCP renders results.

**Tech Stack:** Go, OpenSearch exact vector scoring and point-in-time pagination, FoundationDB, MCP Streamable HTTP.

**Spec:** [Ranking and pagination](../specs/2026-09-19-search-design.md#ranking-and-pagination).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Return at most 25 nodes per response within its byte budget. Each response scans at most four engine batches of at most 100 matches each. These bound individual requests, never total results. Do not return facet counts or indexed text as node content.

## Review Focus

Test more than 1,000 distinct nodes, thousands of pages per node, lower-scoring repeated pages, revoked access, response-byte boundaries, replay, and expired snapshots.

## Task 6: Read ranked matches through native OpenSearch pagination

Create:

```text
internal/domain/search/query.go                    typed query and result contracts
internal/adapters/search/opensearch_query.go       structured combined requests
internal/adapters/search/opensearch_snapshot.go    query inference and point in time
internal/test/integration/search_ranking_test.go  relevance and complete pagination
```

Consume the native client. Produce:

```go
type Query struct { Text, NodeType, Index string; OrgID, ScopeID uuid.UUID }
type Snapshot struct { PITID, Index string; Vector []float32 }
type RankHit struct { NodeID uuid.UUID; Sort json.RawMessage }
type RankBatch struct { Hits []RankHit; PITID string }
type Ranker interface {
    Open(context.Context, Query) (Snapshot, error)
    Read(context.Context, Query, Snapshot, json.RawMessage) (RankBatch, error)
    Close(context.Context, Snapshot) error
}
```

`Open` pins the physical index, runs local query inference once, validates 384
finite values, and opens a point in time with a 15-minute keep-alive. Persist
the vector for every later batch. `Read` preserves exact sort JSON and renews
the point in time. `Close` deletes it. Persist a replacement PIT ID before authorization or rendering.
Failed inference, failed shards, timeout, and unavailable snapshots are errors.

- [ ] Add `TestSearchDistinctNodes`: create at least 1,501 matching nodes, including a dominant multi-page node, through the real reader and Worker.RunOne. Traverse every batch through the public adapter, retaining each node's first match. Require every eligible ID in the order of a test-only ordinary-collapse reference over this bounded fixture. Repeat across three primary shards with 36,000 parts from one node indexed through the public adapter, including lower-scoring repeated parts.
- [ ] Add `TestSearchSemanticRelevance` with all acceptance query/text pairs and 150 distractors. Require each target within the first 25 distinct nodes. A lexical-only control against the real index must miss at least one target.
- [ ] Run `^TestSearch(DistinctNodes|SemanticRelevance)$`; record the existing interface's failure before implementation.
- [ ] Call `POST /_plugins/_ml/_predict/text_embedding/{model_id}` with typed `text_docs:[query.Text]`, `return_number:true`, and `target_response:["sentence_embedding"]`. Persist the actual returned vector; never repeat query inference for continuation.
- [ ] Open `POST /{physical_index}/_search/point_in_time?keep_alive=15m&allow_partial_pit_creation=false`. Send the following request to `POST /_search?allow_partial_search_results=false`. Omit `search_after` initially; later requests use only the last consumed hit's sort values.

```json
{
  "size": 100,
  "pit": {"id":"saved-pit-id", "keep_alive":"15m"},
  "_source": ["node_id"],
  "sort": [{"_score":"desc"}, {"node_id":"asc"}],
  "query": {"bool": {
    "filter": [{"term":{"org_id":"resolved-org"}}, {"term":{"scope_ids":"resolved-scope"}}, {"term":{"retired":false}}],
    "minimum_should_match": 1,
    "should": [
      {"multi_match":{"query":"query-text", "fields":["name^3", "page_text"]}},
      {"nested": {"path":"embedding_text_semantic_info.chunks", "score_mode":"max",
        "query": {"script_score": {"query":{"match_all":{}}, "script": {
          "lang":"knn", "source":"knn_score", "params": {
            "field":"embedding_text_semantic_info.chunks.embedding",
            "query_value":[], "space_type":"l2"
          }
        }}}
      }}
    ]
  }}
}
```

Substitute validated Query fields and the saved vector using typed fields and
`json.Marshal`. Add the optional metadata-defined type filter. OpenSearch sums
keyword and vector scores and uses each page's best embedding. The first match
for a node establishes its rank; node ID breaks ties between nodes.

Native [collapse with search_after](https://docs.opensearch.org/latest/search-plugins/searching-data/collapse-search/)
requires the collapsed field as the only sort field. Omit collapse to preserve
relevance sorting. Persist visited node IDs to skip their later matches.
Identical score and node-ID sort pairs may be skipped together after that node
is consumed; those ties contain no other node. Do not add a page-level tie-breaker.
Lower-scoring repeated matches still require bounded reads and omission.

- [ ] Preserve the failed hybrid regression: one node's 36,000 parts excluded other eligible nodes. Use no nearest-neighbor `k` cutoff or hybrid normalization queue. Exact scoring does more work as content grows; measure cost without imposing a total-result ceiling.
- [ ] Require exactly two sort values and canonical node IDs. Reject malformed responses. Only an empty raw engine batch establishes exhaustion; a short batch or empty deduplicated result does not. Never expose raw page totals as node totals.
- [ ] Create, edit, and delete indexed pages after opening the point in time. Require the complete traversal to match the initial reference order and scores. Stop before consuming a hit and require that hit first on retry. Verify `db` and `invoice` use different actual model vectors and that continuation reuses its stored vector.
- [ ] Repeat relevance queries three times with the native ingest pipeline, unfamiliar metadata, and foreign organization/scope/retirement controls. Require stable ordering and exclusion of ineligible controls. Delete a test point in time and require a restart error, never an implicit fresh search.
- [ ] Run ranking tests and `make check`; commit with subject `Paginate node page matches with OpenSearch exact vector scores`.

## Task 7: Enforce current authorization and commit continuation progress

Create:

```text
internal/domain/search/session.go                  continuation state and bounded commits
internal/adapters/foundationdb/search_session.go   headers and visited-node records
internal/adapters/foundationdb/search_replay.go    replay records and expiry cleanup
internal/adapters/mcp/tools/search_cursor.go       cursor binding and advancement
internal/adapters/mcp/tools/search_results.go      current summaries and rendering
internal/test/integration/search_auth_test.go      authenticated isolation
internal/test/integration/search_cursor_test.go    byte bounds, replay, and continuation
internal/test/integration/search_mcp_fixture_test.go real authentication and opaque metadata
```

Modify [MCP search](../../../internal/adapters/mcp/tools/search.go) and dependency assembly. Remove facet counts and the full-view fetch loop. Consume Ranker and NodeReader.Summary. Produce:

```go
type Session struct {
    ID, Principal, Generation uuid.UUID
    Binding [32]byte
    Secret [32]byte
    Query Query
    Snapshot Snapshot
    After json.RawMessage
    Version, NextPage uint64
    ExpiresAt time.Time
    Closing bool
    CleanupAfter []byte
}
type PageCommit struct {
    After json.RawMessage
    PITID string
    Visited, Results []uuid.UUID
    Done bool
}
type SessionStore interface {
    Create(context.Context, Session) error
    Load(context.Context, uuid.UUID) (Session, error)
    Seen(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error)
    UpdateSnapshot(context.Context, Session, Snapshot) (Session, error)
    CommitPage(context.Context, Session, PageCommit) error
    ReadPage(context.Context, uuid.UUID, uint64) (PageCommit, error)
}
type Cursor struct { SessionID uuid.UUID; Page uint64; Authentication string }
```

Store one bounded header, one key per visited node, and one bounded replay record
per response. Never assemble all visited IDs or results in memory or a transaction.
Bind principal, normalized query, resolved filters, physical index, and search generation using
deterministic serialization and SHA-256. Authenticate the page number with a
persisted random per-session secret. Use HMAC-SHA256 over session ID, page number,
and Binding with unambiguous encoding and constant-time verification.

`CommitPage` checks expected Version and NextPage in an FDB transaction. It writes
at most 400 visited IDs, at most 25 result IDs, consumed sort values, the latest
PIT ID, a replay record, and the next page number atomically. It renews 15-minute
inactivity expiry. Concurrent writers conflict; the loser reads the committed
response. Replay does not renew inactivity expiry. Initial requests without a
cursor create new sessions. Close a newly opened PIT if Create fails.

`UpdateSnapshot` rejects closing or expired sessions, compares and increments Version, and saves the latest PIT ID without advancing
After, and returns the updated Session. On conflict, reload the header and retry
from its committed position or replay the committed page. Persist replacement
IDs before other fallible processing. If a lost engine response invalidates the
saved PIT, return an explicit restart error; never open a replacement silently.

- [ ] Create identities through real SQL UserRepo, TokenRepo, and OrgMemberRepo operations. Set `ENV=production` and configure real audit dependencies. Initialize opaque NodeType and PropertyDef records before MCP calls. Never call SeedOrg, BootstrapIdentities, or the seeded harness.
- [ ] Add Driver.CallRaw and the [authenticated fixture](2026-09-19-opensearch-fixtures.md#authenticated-calls-for-tasks-7-and-10). Derive the entry-point parameter from its declared type slug. Exercise the real authenticated handler and validate actual JSON/SSE responses.
- [ ] Add failing cursor tests with at least 1,501 nodes and names that exhaust response bytes before 25 results. Follow every continuation, including empty pages; require all eligible nodes exactly once. Repeat with revoked membership, moved scope, changed query, and lower-scoring repeated parts.
- [ ] Resolve entry point, membership, scope, and declared type before Open. Reject empty and over-budget queries using the proven byte bound. Undefined types and foreign scopes fail before engine calls. Remove the exact-reference shortcut or apply identical validation and authorization.
- [ ] Verify cursor binding and expiry, then check ReadPage before advancing. Reject an uncommitted page number other than NextPage. Reauthorize and render saved result IDs on replay; omit newly deleted or unauthorized nodes without refilling a committed page. Replay never advances again and obeys the same response-byte limit.
- [ ] Read at most four batches per response. For each hit, check bounded persisted and request-local visited IDs. Skip visited nodes. Otherwise read Summary and perform current authorization. Omit only deleted or unauthorized candidates; storage, metadata, and engine failures are errors. Authorization is evaluated at first consumption; later grants require a new session to reconsider omitted nodes.
- [ ] Advance After only after consuming a hit. Mark an omitted or rendered node visited. Check response bytes before rendering; stop before consuming a node that cannot fit. Stop after 25 results or four batches and return continuation, even with no results. Only an empty raw engine batch sets Done. Successful nonfinal calls must advance or replay a committed page.
- [ ] Commit the consumed prefix before responding. After an uncertain commit, read the replay record before retrying. Failure before commit repeats work; failure after commit replays the page. Delete the point in time after committing completion; retain replay records until expiry.

Produce `Resolver.authorizeSearchSummary(ctx context.Context, principal, orgID, scopeID uuid.UUID, typeKey string, summary node.Summary) (bool, error)`. Require matching OrgID, current ScopeIDs, and optional typeKey. Add uncached `OrgMemberRepo.IsMember(ctx context.Context, orgID, userID uuid.UUID) (bool, error)` using `SELECT EXISTS(SELECT 1 FROM org_members WHERE org_id=$1 AND user_id=$2)`. Inject it into the resolver and call markAuthorized only after membership succeeds.

Produce `renderSearchSummary(node.Summary) (string, error)` using the existing list-item renderer and Unicode-safe shortening. Reserve envelope and cursor bytes. Ensure one bounded item fits an empty response. If updated summaries would overflow replay, shorten each within a guaranteed per-item budget so the committed IDs fit. Authorization and rendering never read multiple content pages.

- [ ] Corrupt indexed org/scope fields and require MCP to withhold foreign nodes, including UUID/reference inputs. Test an FDB outage, principal mismatch, lost response, concurrent same-cursor requests, and process restart. Replay must not advance twice or reveal cached unauthorized content.
- [ ] Force four batches of visited lower-scoring parts before a new node. Require an empty response with continuation and eventual retrieval. Renew advancing sessions beyond 15 minutes; replay must not renew expiry. Expired or deleted snapshots return restart errors. Grant access after omitting a node; only a new search must reconsider it. Restart during cleanup and require bounded deletion to resume without affecting active sessions.
- [ ] Run `^TestSearch(Auth|Cursor|DistinctNodes)` and `make check`; commit with subject `Return authorized search results with durable continuation`.
