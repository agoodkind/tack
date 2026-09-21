# Paginated Node Reader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return complete searchable node text as stable bounded pages with indexed access fields.

**Architecture:** Metadata defines how each opaque property becomes text. The reader emits one UTF-8 page per call and binds every continuation to the node revision and projection version. One access policy computes both indexed access fields and query filters, so future permission work changes that policy and the versioned mapping instead of pagination or worker code.

**Tech Stack:** Go, FoundationDB, JSON, existing `NodeReader`.

**Spec:** [Searchable content](../specs/2026-09-19-search-design.md#searchable-content) and [permission expansion](../specs/2026-09-19-search-acceptance.md#permission-expansion).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Preserve the current bounded node record. Use the central FoundationDB key catalog. Do not inspect property names or property types when projecting text. Do not place permission rules in the page writer.

## Review Focus

Test edited revisions, reordered maps, unfamiliar property types, names spanning pages, UTF-8 boundaries, and access fields after an ancestry change.

---

### Task 2: Add metadata-driven paginated node reads

**Files:**

- Create: `internal/domain/node/content.go`
- Create: `internal/domain/node/search_projection_emit.go`
- Create: `internal/domain/search/filter.go`
- Create: `internal/searchaccess/access.go`
- Create: `internal/adapters/foundationdb/node_content.go`
- Create: `internal/adapters/foundationdb/node_content_cursor.go`
- Create: `internal/adapters/foundationdb/node_summary.go`
- Create: `internal/adapters/foundationdb/search_revision.go`
- Modify: `internal/domain/node/types.go`
- Modify: `internal/domain/node/reader.go`
- Modify: `internal/adapters/foundationdb/keys.go`
- Test: `internal/test/integration/search_reader_test.go`
- Test: `internal/test/integration/search_store_fixture_test.go`

**Interfaces:**

- Consumes: Task 1B `SearchProjection`, `NodeView`, current ancestry reads, central FDB tuple helpers.
- Produces: `ContentReader`, `AccessPolicy`, `AccessFilter`, `ContentPage`, `Summary`, and `SearchScan` for Tasks 4 through 10.

```go
type AccessField struct { Name string; Values []string }
type SearchAccess struct { Version string; Fields []AccessField }
type ContentRequest struct { NodeID uuid.UUID; Cursor, ProjectionConfig string; MaxBytes int }
type ContentPage struct {
    NodeID uuid.UUID
    NodeType, Revision, ProjectionVersion, Name, Text, NextCursor string
    Access SearchAccess
    Ordinal uint64
    OverlapBytes int
    Done bool
}
type Summary struct { NodeID uuid.UUID; NodeType, Name, Text string; Access SearchAccess }
type SearchScan struct { NodeIDs []uuid.UUID; NextCursor string; Done bool }
type ContentReader interface {
    Content(context.Context, ContentRequest) (ContentPage, error)
    Summary(context.Context, uuid.UUID, int) (Summary, error)
    ScanSearch(context.Context, string, int) (SearchScan, error)
}
type AccessFilter struct { Version string; Clauses json.RawMessage }
type IndexAccessRequest struct { NodeID, OrgID uuid.UUID; ScopeIDs []uuid.UUID }
type AccessRequest struct { PrincipalID, OrgID, ScopeID uuid.UUID }
type AccessPolicy interface {
    Index(context.Context, IndexAccessRequest) (node.SearchAccess, error)
    Query(context.Context, AccessRequest) (search.AccessFilter, error)
}
```

- [ ] **Step 1: Add the failing multi-page and access test.**

```go
func TestSearchReaderPages(t *testing.T) {
    stores := newSearchStore(t)
    id := putSearchText(t, stores, strings.Repeat("é水🙂 ", 300)+"tail-Z")
    request := node.ContentRequest{NodeID: id, MaxBytes: 128}
    var unique strings.Builder
    for ordinal := uint64(0); ; ordinal++ {
        page, err := stores.Views.Content(t.Context(), request)
        if err != nil { t.Fatal(err) }
        if !utf8.ValidString(page.Text) || len(page.Text) > 128 { t.Fatalf("invalid page %d", ordinal) }
        if page.Ordinal != ordinal || page.Access.Version != "org-scope-v1" { t.Fatalf("bad identity: %#v", page) }
        unique.WriteString(page.Text[page.OverlapBytes:])
        if page.Done { break }
        if page.NextCursor == "" || page.NextCursor == request.Cursor { t.Fatal("cursor did not advance") }
        request.Cursor = page.NextCursor
    }
    if !strings.Contains(unique.String(), "tail-Z") { t.Fatal("final text missing") }
}
```

- [ ] **Step 2: Run the reader test and record the expected failure.**

Run: `go test ./internal/test/integration -run '^TestSearchReaderPages$' -count=1`

Expected: FAIL because `Content`, `ContentRequest`, and `SearchAccess` do not exist.

- [ ] **Step 3: Use the projection declarations from Task 1B.**

```go
type TextRule struct {
    Mode string `json:"mode"`
    Fields []TextField `json:"fields,omitempty"`
    Items *TextRule `json:"items,omitempty"`
    Labels map[string]string `json:"labels,omitempty"`
}
type TextField struct { Key string `json:"key"`; Rule TextRule `json:"rule"` }
type SearchProjection struct { Include bool `json:"include"`; Order int `json:"order"`; Rule TextRule `json:"rule"` }
```

Read `PropertyDef.Search` without inferring missing declarations. `Include:false` is a complete exclusion. `Include:true` selects the validated rule. Do not use the FDB `Indexed` flag.

- [ ] **Step 4: Implement deterministic text emission.**

Add `EmitSearchText(ctx context.Context, view *NodeView, defs []*PropertyDef, out io.Writer) error`. Emit the node name first. Sort applicable included declarations by `Order`, then definition UUID. Decode scalar, array, declared object fields, and sorted object values recursively. Emit one newline after each leaf. Return an error with node and property IDs for malformed JSON, wrong shapes, or undeclared labels.

```go
case "values":
    var values map[string]json.RawMessage
    if err := json.Unmarshal(raw, &values); err != nil { return err }
    if rule.Items == nil { return errors.New("object values have no item rule") }
    for _, key := range slices.Sorted(maps.Keys(values)) {
        if err := emitSearchValue(values[key], *rule.Items, out); err != nil { return err }
    }
```

- [ ] **Step 5: Implement the current access policy once.**

```go
const accessVersion = "org-scope-v1"
type OrgScopeAccess struct{}
func (OrgScopeAccess) Index(_ context.Context, request IndexAccessRequest) (node.SearchAccess, error) {
    if request.OrgID == uuid.Nil { return node.SearchAccess{}, errors.New("organization is required") }
    scopes := make([]string, len(request.ScopeIDs))
    for i, id := range request.ScopeIDs { scopes[i] = id.String() }
    return node.SearchAccess{Version:accessVersion, Fields:[]node.AccessField{
        {Name:"org_id", Values:[]string{request.OrgID.String()}},
        {Name:"scope_ids", Values:scopes},
    }}, nil
}
func (OrgScopeAccess) Query(_ context.Context, request AccessRequest) (search.AccessFilter, error) {
    if request.OrgID == uuid.Nil { return search.AccessFilter{}, errors.New("organization is required") }
    clauses := []map[string]map[string]string{{"term":{"access.org_id":request.OrgID.String()}}}
    if request.ScopeID != uuid.Nil { clauses = append(clauses, map[string]map[string]string{"term":{"access.scope_ids":request.ScopeID.String()}}) }
    raw, err := json.Marshal(clauses)
    if err != nil { return search.AccessFilter{}, err }
    return search.AccessFilter{Version:accessVersion, Clauses:raw}, nil
}
```

`Content` reads current ancestry, then calls `AccessPolicy.Index` with node ID, organization, and scope IDs. Sort fields by name and values by bytes before returning. Reject duplicate field names. Page mapping only copies `ContentPage.Access`. A future permission model changes these two methods, adds strict `access` mapping fields, increments `Version`, and rebuilds the index. The `SearchAccess`, `AccessFilter`, page, worker, query, and session formats remain unchanged.

- [ ] **Step 6: Implement bounded UTF-8 pages and revision-bound cursors.**

The writer records at most `MaxBytes`, reserves at most one quarter for prior context, splits at rune boundaries, and records the unique-text offset separately. Encode node ID, revision, organization projection epoch, projection config hash, pagination version, byte bound, next unique offset, and ordinal in the cursor. Validate all values in one FDB transaction. Return `ErrContentChanged` after an edit and `ErrNotFound` after deletion. Reject `MaxBytes < 16`.

- [ ] **Step 7: Implement bounded scans and summaries.**

Embed `ContentReader` in `NodeReader`; the concrete `ViewStore` implements both. Scan global node-resolution keys in raw-key order with a caller limit. Build summaries from current identity, ancestry, and bounded name and text prefixes. Do not list organizations through SQL or assemble every page.

- [ ] **Step 8: Add failure and stability coverage.**

Test complete text after removing overlap, reordered maps, excluded fields, unfamiliar property types, long names, malformed declarations, edits between pages, ancestry changes, deletion, and empty nonfinal pages. Require every nonfinal cursor to advance and every page after an edit to return `errors.Is(err, node.ErrContentChanged)`.

- [ ] **Step 9: Run the complete task checks.**

Run: `go test ./internal/test/integration -run '^TestSearchReader' -count=1`

Run: `make check`

Expected: PASS with no skipped search test.

- [ ] **Step 10: Commit the task.**

```sh
git add internal/domain/node internal/domain/search/filter.go internal/searchaccess/access.go internal/adapters/foundationdb internal/test/integration/search_reader_test.go internal/test/integration/search_store_fixture_test.go
git commit -S -m "Add metadata-driven paginated node content reads" -m "Co-authored-by: Codex <noreply@openai.com>"
```
