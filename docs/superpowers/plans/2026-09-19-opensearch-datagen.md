# Search QA Data Generation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Verify public search behavior in QA with opaque metadata and real authenticated MCP calls.

**Architecture:** The guarded QA operation creates its own minimal metadata, credentials, nodes, and expected results. It invokes public MCP tools through the existing datagen driver and deletes or isolates its fixture through existing lifecycle rules.

**Tech Stack:** Go, existing datagen driver, MCP Streamable HTTP, real FoundationDB and OpenSearch.

**Spec:** [Evidence and environment](../specs/2026-09-19-search-acceptance.md#evidence-and-environment).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Use opaque generated identifiers. Keep fixture phrases in data. Preserve the production target guard.

## Review Focus

Test final-page text, semantic-only relevance, shorter edits, deletion, excluded properties, continuation duplicates, engine outage, and production guard rejection.

---

### Task 1: Add public QA search coverage

**Files:**

- Create: `internal/datagen/search_fixture.go`
- Create: `internal/datagen/search_pages.go`
- Create: `internal/datagen/search_results.go`
- Create: `internal/datagen/generate_search_checks.go`
- Test: `internal/test/integration/search_datagen_test.go`

**Interfaces:**

- This plan requires the ranked public `tack_search` handler, search runtime, `Driver.CallRaw`, and the existing guarded `ops qa datagen` entry point.
- This plan implements `datagen.VerifySearch(context.Context, *config.Config) error` for deployment acceptance.

```go
type searchPage struct { IDs []uuid.UUID; Cursor string; ResponseBytes int }
func callSearch(context.Context, *Driver, string, string, uuid.UUID, string, string) (searchPage, error)
```

- [ ] **Step 1: Add the failing real-operation test.**

```go
func TestSearchDatagen(t *testing.T) {
    cfg := testenv.Config(t)
    cfg.DatagenAllowTarget = "local"
    cfg.SearchPageBytes = 128
    if err := datagen.VerifySearch(t.Context(), cfg); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Record the deferred failure contract.**

The final validation plan runs `^TestSearchDatagen$` against the completed branch. The test must fail when public verification, opaque fixtures, continuation, or the production target guard is absent. Do not start live dependencies during this coding task.

- [ ] **Step 3: Create opaque included and excluded metadata.**

Generate unrelated UUID-derived node type, property name, and property type values. Write one included scalar projection and one explicit excluded projection. Create a metadata-defined entry node and real authentication through production repositories. Do not load product seeds.

```go
included := &node.PropertyDef{ID: uuid.Must(uuid.NewV7()), OrgID: orgID,
    Name: opaqueKey("included"), Type: node.PropertyType(opaqueKey("type")),
    Search: &node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: "scalar"}}}
excluded := &node.PropertyDef{ID: uuid.Must(uuid.NewV7()), OrgID: orgID,
    Name: opaqueKey("excluded"), Type: node.PropertyType(opaqueKey("type")),
    Search: &node.SearchProjection{Include: false}}
```

- [ ] **Step 4: Add a public continuation helper.**

Implement `callSearch` through `Driver.CallRaw`. Supply the metadata-derived entry argument, query, and optional cursor. Decode result node IDs, continuation, and response bytes. Repeat until continuation is absent. Reject a duplicate ID, repeated cursor, more than 25 IDs in one page, or more response bytes than the public limit.

```go
for cursor != "" || first {
    page, err := callSearch(ctx, driver, token, parameter, entryID, query, cursor)
    if err != nil { return err }
    for _, id := range page.IDs {
        if _, exists := seen[id]; exists { return fmt.Errorf("duplicate search result %s", id) }
        seen[id] = struct{}{}
    }
    first = false
    cursor = page.Cursor
}
```

- [ ] **Step 5: Add semantic relevance and complete page coverage.**

Create accepted semantic pairs and lexical distractors. Create one node where only the final reader page contains the target. Require both expected IDs within their accepted rank bounds. Search excluded text and require no result.

- [ ] **Step 6: Add edit, deletion, and continuation coverage.**

Replace a long searchable value with a shorter value. Require the old term to disappear and the new term to appear. Delete the node and require absence. Create more than 25 eligible nodes plus one duplicate-heavy node. Traverse every continuation and compare the complete distinct ID set.

- [ ] **Step 7: Add explicit outage coverage.**

Add a test that stops the disposable engine, commits a new node through MCP, and requires search to return an unavailable error rather than an empty result. Restart the engine and require the node within a ten-second deadline. The final validation plan executes this test.

- [ ] **Step 8: Preserve the production target guard.**

Add a test that uses a production target and requires rejection before metadata or node writes. Assert the fixture organization does not exist afterward. The final validation plan executes this test.

- [ ] **Step 9: Run the serial coding checks.**

Run: `make build`

Expected: PASS. The final validation plan runs real JSON and SSE public verification.

- [ ] **Step 10: Create the next Graphite slice.**

```sh
git add internal/datagen/search_fixture.go internal/datagen/search_pages.go internal/datagen/search_results.go internal/datagen/generate_search_checks.go internal/test/integration/search_datagen_test.go
```

Run Graphite MCP `create` from stack position 6 with this exact message:

```text
Exercise paginated semantic search in QA datagen

Co-authored-by: Codex <noreply@openai.com>
```

This branch is stack position 7.
