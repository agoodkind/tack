# Ranked Node Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return distinct, currently authorized nodes with stable continuation.

**Architecture:** OpenSearch ranks and groups page matches. FoundationDB stores each bounded ranked set. The node reader supplies current visibility and bounded summaries before MCP renders each result.

**Tech Stack:** Go, OpenSearch hybrid search, FoundationDB, MCP Streamable HTTP.

**Spec:** [Ranking and pagination](../specs/2026-09-19-search-design.md#ranking-and-pagination).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Return at most 25 nodes per response and retain at most 1,000 IDs per ranked set. Do not use facet counts or indexed text as returned node content.

## Review Focus

Test one node with thousands of pages, revoked access during continuation, corrupt indexed scope, long result names, and a cursor reused with different input.

---

## Task 6: Rank and collapse pages using native OpenSearch

Create:

```text
internal/domain/search/query.go                    typed query and result contracts
internal/adapters/search/opensearch_query.go       structured hybrid requests
internal/adapters/search/opensearch_pipeline.go    normalization pipeline
internal/test/integration/search_ranking_test.go  relevance and distinct-node tests
```

Consume the native client. Produce:

```go
type Query struct { Text, NodeType, Index string; OrgID, ScopeID uuid.UUID; Limit int }
type RankedSet struct { NodeIDs []uuid.UUID; Index string; Limited bool }
type Ranker interface { Rank(context.Context, Query) (RankedSet, error) }
func (c *OpenSearchClient) Rank(ctx context.Context, query search.Query) (search.RankedSet, error)
```

- [ ] Add `TestSearchDistinctNodes`: create 149 ordinary nodes and one node with more than 1,000 pages under the real reader's small byte budget. Give all nodes the same phrase, index them through Worker.RunOne, and call OpenSearchClient.Rank. Require 150 unique IDs. The next task requires six MCP responses of 25 unique nodes. Repeat across all three primary shards.
- [ ] Add `TestSearchSemanticRelevance` using every query/text pair from acceptance and 150 distractors. Require each target within the first 25 results. Send a lexical-only control request to the real index and require at least one target to be missed by that control.
- [ ] Run `^TestSearch(DistinctNodes|SemanticRelevance)$`; expect the existing interface to lack semantic ranking and continuation.
- [ ] Install `node-pages-hybrid` with `min_max` normalization and `arithmetic_mean` combination. Use two equal initial weights. Build this request through typed fields and `json.Marshal`; never interpolate user input into JSON or filter strings.

```json
{
  "size": 1000,
  "_source": ["node_id"],
  "query": {"hybrid": {"queries": [
    {"bool": {
      "must": [{"multi_match": {"query": "query-text", "fields": ["name^3", "page_text"]}}],
      "filter": [{"term": {"org_id": "resolved-org"}}, {"term": {"scope_ids": "resolved-scope"}}, {"term": {"retired": false}}]
    }},
    {"neural": {"page_text": {
      "query_text": "query-text", "k": 10000,
      "filter": {"bool": {"filter": [{"term": {"org_id": "resolved-org"}}, {"term": {"scope_ids": "resolved-scope"}}, {"term": {"retired": false}}]}}
    }}}
  ]}},
  "collapse": {"field": "node_id"}
}
```

`query-text`, `resolved-org`, and `resolved-scope` are illustrative JSON values;
the implementation uses Query fields. Add the optional validated node-type filter
to both branches. Validate the semantic field's filter rewrite against the real
3.8 engine. Fail the test if filters apply outside the intended node documents.

- [ ] Validate the proposed candidate depth against the duplicate-heavy fixture and resource bounds. `k:10000` is an initial experiment, not proof that arbitrary page duplication is harmless. Require the distinct-node test to pass before accepting the query configuration. Do not compensate with a fixed Tack page-count limit.
- [ ] Decode only canonical node IDs. Reject malformed backend responses and duplicate IDs after native collapse. Freeze the physical index name in RankedSet. Set Limited when the bounded collection reaches 1,000; do not expose OpenSearch hit totals as node totals.
- [ ] Run ranking tests and `make check`; commit with subject `Rank and collapse node pages with OpenSearch hybrid search`.

## Task 7: Enforce current authorization and preserve continuation

Create:

```text
internal/domain/search/session.go                  continuation state
internal/adapters/foundationdb/search_session.go   bounded ranked-set storage
internal/adapters/mcp/tools/search_cursor.go       cursor binding and advancement
internal/adapters/mcp/tools/search_results.go      current summaries and rendering
internal/test/integration/search_auth_test.go      authenticated isolation tests
internal/test/integration/search_cursor_test.go    byte bounds and continuation
internal/test/integration/search_mcp_fixture_test.go real authentication and opaque metadata
```

Modify [MCP search](../../../internal/adapters/mcp/tools/search.go) and its dependency assembly. Remove facet counts and the old full-view fetch loop. Consume Ranker and NodeReader.Summary. Produce:

```go
type Session struct {
    ID uuid.UUID
    Principal uuid.UUID
    Binding [32]byte
    Index string
    NodeIDs []uuid.UUID
    ExpiresAt time.Time
    Limited bool
}
type SessionStore interface {
    Save(context.Context, Session) error
    Load(context.Context, uuid.UUID) (Session, error)
}
type Cursor struct { SessionID uuid.UUID; Position int; Authentication string }
```

Store sessions in FDB as separate bounded ID pages plus a header, not one growing
value. Use a random per-session secret to authenticate the encoded position.
Bind the principal, normalized query, resolved organization/scope/type, and index
version with deterministic serialization and SHA-256. Expire sessions after
15 minutes; reject changed bindings and unavailable physical indexes explicitly.

- [ ] Create fixture identities through real SQL UserRepo, TokenRepo, and OrgMemberRepo operations. Set `ENV=production`; configure real audit dependencies. Initialize opaque NodeType and PropertyDef records before the first MCP request. Never call `SeedOrg`, `BootstrapIdentities`, or the existing seeded MCP harness.
- [ ] Add Driver.CallRaw and the [authenticated fixture](2026-09-19-opensearch-fixtures.md#authenticated-calls-for-tasks-7-and-10). Derive the entry-point parameter from the declared type slug. The existing driver executes the production authenticated handler and validates JSON/SSE responses.
- [ ] Add the failing cursor test. The fixture must create enough long names to fill the actual response-byte limit before 25 results. Decode the rendered continuation and require all eligible nodes exactly once. Repeat with revoked membership, moved scope, and a changed query.
- [ ] Resolve the entry point, membership, optional scope, and declared type before Rank. Reject empty and over-budget queries using the native task's proven byte bound. Undefined types and foreign scopes fail before any engine request. Remove the exact-reference shortcut or subject it to the same current checks; it must never bypass query validation or authorization.
- [ ] For each stored candidate, read Summary and perform authoritative current membership and ancestry checks. Omit only deleted or unauthorized candidates. Return errors for failed storage, metadata, or engine reads. Search authorization must use current membership reads rather than accepting a stale membership cache entry.
- [ ] Advance the cursor only after omitting an ineligible candidate or successfully rendering an eligible one. This is the required control flow:

```go
for position < len(session.NodeIDs) && len(results) < 25 {
    summary, err := reader.Summary(ctx, session.NodeIDs[position], summaryBytes)
    if errors.Is(err, domain.ErrNotFound) { position++; continue }
    if err != nil { return err }
    allowed, err := resolver.authorizeSearchSummary(ctx, principal, orgID, scopeID, typeKey, summary)
    if err != nil { return err }
    if !allowed { position++; continue }
    encoded, err := renderSearchSummary(summary)
    if err != nil { return err }
    if usedBytes+len(encoded) > resultBudget { break }
    results = append(results, encoded)
    usedBytes += len(encoded)
    position++
}
```

Produce `Resolver.authorizeSearchSummary(ctx context.Context, principal, orgID, scopeID uuid.UUID, typeKey string, summary node.Summary) (bool, error)`. Require summary.OrgID to equal orgID, scopeID to occur in current ScopeIDs, and the optional typeKey to match. Add `OrgMemberRepo.IsMember(ctx context.Context, orgID, userID uuid.UUID) (bool, error)` using `SELECT EXISTS(SELECT 1 FROM org_members WHERE org_id=$1 AND user_id=$2)`. Inject that uncached reader into the search resolver and call markAuthorized after it succeeds. Produce `renderSearchSummary(node.Summary) (string, error)` using the existing list-item renderer and its Unicode-safe shortening. Reserve envelope and cursor bytes before the loop; ensure one bounded item fits an empty response. `summaryBytes`, `resultBudget`, `results`, and `usedBytes` are request-local values derived from that renderer's limits.

- [ ] Corrupt an indexed org/scope field and verify the real MCP request withholds the foreign node. Repeat for UUID/reference input. Test FDB outage as an explicit error, cursor expiry, principal mismatch, stable order after score changes, and the visible 1,000-result bound.
- [ ] Run `^TestSearch(Auth|Cursor|DistinctNodes)` and `make check`; commit with subject `Return authorized search results with stable continuation`.
