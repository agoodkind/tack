# Ranked Node Query Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rank every eligible page match with one query embedding and complete point-in-time continuation.

**Architecture:** OpenSearch infers sparse query weights once. Later reads reuse the saved opaque weights against `rank_features`. The query receives a structured access filter from Task 3 and applies it before ranking.

**Tech Stack:** Go, OpenSearch 3.8.0, official OpenSearch Go client v4.7.3, ML Commons.

**Spec:** [Ranking and continuation](../specs/2026-09-19-search-design.md#ranking-and-continuation) and [permission expansion](../specs/2026-09-19-search-acceptance.md#permission-expansion).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Infer query weights once. Read at most 100 raw page matches per engine batch. Do not use collapse, `knn`, dense scripts, hybrid candidate windows, or application-side permission filtering as the primary filter.

## Review Focus

Test duplicate-heavy nodes, equal sort values, invalid model output, deleted points in time, foreign organizations, sibling scopes, and later index writes.

---

### Task 6: Rank and paginate native sparse matches

**Files:**

- Create: `internal/domain/search/query.go`
- Create: `internal/adapters/search/opensearch_query.go`
- Create: `internal/adapters/search/opensearch_snapshot.go`
- Test: `internal/test/integration/search_ranking_test.go`
- Test: `internal/test/integration/search_permission_filter_test.go`

**Interfaces:**

- Consumes: Task 1 `Adapter`, Task 3 `AccessPolicy.Query` and `AccessFilter`, and the active physical index.
- Produces: `Ranker`, `Snapshot`, and exact sort tokens for Task 7 sessions.

```go
type Query struct { Text, Index, NodeType string; Access AccessFilter }
type Snapshot struct { PITID, Index string; QueryTokens json.RawMessage }
type RankHit struct { NodeID uuid.UUID; Sort json.RawMessage }
type RankBatch struct { Hits []RankHit; PITID string }
type Ranker interface {
    Open(context.Context, Query) (Snapshot, error)
    Read(context.Context, Query, Snapshot, json.RawMessage) (RankBatch, error)
    Close(context.Context, Snapshot) error
}
```

- [ ] **Step 1: Add failing relevance and distinct-node tests.**

```go
func TestSearchDistinctNodes(t *testing.T) {
    ranker, query := newRankedCorpus(t, rankedCorpus{Nodes: 1501, DuplicatePages: 36000, PrimaryShards: 3})
    snapshot, err := ranker.Open(t.Context(), query)
    if err != nil { t.Fatal(err) }
    defer ranker.Close(context.Background(), snapshot)
    got := collectDistinctNodes(t, ranker, query, snapshot)
    if len(got) != 1501 { t.Fatalf("distinct nodes = %d, want 1501", len(got)) }
}
```

Add the six accepted semantic pairs with at least 150 distractors. Require each target within the first 25 distinct nodes across three repeats and a reindex. Require one lexical-only control to miss.

- [ ] **Step 2: Record the deferred failure contract.**

Task 13 runs `^TestSearch(SemanticRelevance|DistinctNodes)$` against the completed branch. The tests must fail when relevance, complete traversal, exact sort values, or one prediction per session is broken. Do not start OpenSearch during this coding task.

- [ ] **Step 3: Implement one concrete ML Commons prediction request.**

Send `{"text_docs":[query.Text]}` through a narrow type that satisfies `opensearch.Request`. Call `opensearch.Do` and `opensearch.ParseError`. Require exactly one nonempty map of finite, nonnegative weights. Store its exact JSON bytes in `Snapshot.QueryTokens`. Reject output above the configured session byte bound.

```go
type predictRequest struct { ModelID string; Body io.Reader }
func (r predictRequest) GetRequest(method string) (*http.Request, error) {
    path := "/_plugins/_ml/models/" + url.PathEscape(r.ModelID) + "/_predict"
    request, err := http.NewRequest(method, path, r.Body)
    if err != nil { return nil, err }
    request.Header.Set("Content-Type", "application/json")
    return request, nil
}
```

- [ ] **Step 4: Open and close the point in time with typed APIs.**

Resolve the alias to one physical index before prediction. Read its access version through `Adapter.IndexInfo`. Use typed point-in-time create and delete calls. Save replacement PIT IDs returned by reads. A deleted PIT returns an explicit restart error.

- [ ] **Step 5: Serialize the current permission filter before ranking.**

Call `AccessPolicy.Query` after membership, organization, and scope resolution. Reject an access version that differs from the physical index. Decode `Query.Access.Clauses` as a nonempty JSON array. Append optional `node_type` and required `retired:false` clauses. The ranker never names or derives permission rules.

```go
type queryClause struct { Term map[string]json.RawMessage `json:"term,omitempty"` }
var filters []json.RawMessage
if err := json.Unmarshal(query.Access.Clauses, &filters); err != nil { return nil, err }
if len(filters) == 0 { return nil, errors.New("access filter is empty") }
filters = append(filters, json.RawMessage(`{"term":{"retired":false}}`))
if query.NodeType != "" {
    nodeTypeValue, err := json.Marshal(query.NodeType)
    if err != nil { return nil, err }
    clause, err := json.Marshal(queryClause{Term:map[string]json.RawMessage{"node_type":nodeTypeValue}})
    if err != nil { return nil, err }
    filters = append(filters, clause)
}
```

- [ ] **Step 6: Build the exact ranked request.**

Use `opensearchapi.SearchReq.GetRequest` for the request path and parameters. Marshal a typed body with `size:100`, saved PIT, `_source:["node_id"]`, the filters above, lexical `multi_match`, nested `neural_sparse`, and sort by descending `_score`, ascending `node_id`, then ascending `_shard_doc`. Insert query text and saved token JSON through `json.Marshal`. Decode a narrow response through `opensearch.Do` because `SearchResp` omits replacement PIT IDs and changes sort value types.

```json
{"query":{"bool":{"filter":[{"term":{"access.org_id":"resolved-org"}},{"term":{"access.scope_ids":"resolved-scope"}},{"term":{"retired":false}}],"minimum_should_match":1,"should":[{"multi_match":{"query":"query text","fields":["name^3","page_text"]}},{"nested":{"path":"page_text_semantic_info.chunks","score_mode":"max","query":{"neural_sparse":{"page_text_semantic_info.chunks.embedding":{"query_tokens":{}}}}}}]}}}
```

- [ ] **Step 7: Preserve exact continuation.**

Require exactly three sort values. Store the original sort JSON without converting numbers. Pass it back as `search_after`. Treat only an empty raw batch as exhaustion. A short batch and a batch containing only already visited nodes remain nonterminal.

- [ ] **Step 8: Prove OpenSearch filters before ranking.**

Create 400 foreign-organization pages and 400 sibling-scope pages with stronger lexical matches than two eligible pages. Build the filter through `AccessPolicy.Query`, call `Ranker.Open` and `Ranker.Read`, and require every raw hit to belong to the eligible organization and scope. This test examines raw ranker output, so FoundationDB post-filtering cannot make it pass.

```go
for _, hit := range batch.Hits {
    if !eligible[hit.NodeID] { t.Fatalf("forbidden raw hit %s", hit.NodeID) }
}
```

- [ ] **Step 9: Define profiling and regression checks.**

Require Lucene `FeatureQuery` operations over `rank_features` and no dense script. Prove one prediction per snapshot, later-write exclusion, repeated relevance, complete traversal, and a stopped read that returns the same next hit after retry.

- [ ] **Step 10: Run the serial coding checks.**

Run: `go test ./internal/test/integration -run '^$' -count=1`

Run: `make check`

Expected: PASS after compiling the integration package without executing its tests. Task 13 runs relevance, traversal, filtering, and profiling.

- [ ] **Step 11: Commit the task.**

```sh
git add internal/domain/search/query.go internal/adapters/search/opensearch_query.go internal/adapters/search/opensearch_snapshot.go internal/test/integration/search_ranking_test.go internal/test/integration/search_permission_filter_test.go
git commit -S -m "Paginate node page matches with OpenSearch sparse ranking" -m "Co-authored-by: Codex <noreply@openai.com>"
```
