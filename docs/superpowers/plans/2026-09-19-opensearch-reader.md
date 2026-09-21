# Paginated Node Reader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return complete searchable node text as stable bounded pages with opaque versioned access keys.

**Architecture:** Metadata defines how each opaque property becomes text. The reader emits one UTF-8 page per call and binds every continuation to the node revision and text projection version. One access policy reads authoritative nodes and relationships and compiles both resource and caller keys. Future permission work replaces that policy without changing the mapping, page identity, pagination, or worker formats.

**Tech Stack:** Go, FoundationDB, JSON, existing `NodeReader`.

**Spec:** [Searchable content](../specs/2026-09-19-search-design.md#searchable-content) and [permission expansion](../specs/2026-09-19-search-acceptance.md#permission-expansion).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Preserve the current bounded node record. Use the central FoundationDB key catalog. Do not inspect property names or property types when projecting text. Do not place permission rules in the page writer.

## Review Focus

Test edited revisions, reordered maps, unfamiliar property types, names spanning pages, UTF-8 boundaries, renamed permission identifiers, and access keys after an ancestry change.

### Task 3: Add metadata-driven paginated node reads

**Files:**

- Create: `internal/domain/node/content.go`
- Create: `internal/domain/node/search_projection_emit.go`
- Create: `internal/domain/search/filter.go`
- Create: `internal/searchaccess/access.go`
- Create: `internal/adapters/foundationdb/node_content.go`
- Create: `internal/adapters/foundationdb/node_content_cursor.go`
- Create: `internal/adapters/foundationdb/node_summary.go`
- Create: `internal/adapters/foundationdb/search_revision.go`
- Create: `internal/adapters/foundationdb/search_access_state.go`
- Modify: `internal/domain/node/types.go`
- Modify: `internal/domain/node/reader.go`
- Modify: `internal/adapters/foundationdb/keys.go`
- Test: `internal/test/integration/search_reader_test.go`
- Test: `internal/test/integration/search_store_fixture_test.go`

**Interfaces:**

- Consumes: Task 2 `SearchProjection`, `NodeView`, current ancestry reads, central FDB tuple helpers.
- Produces: `ContentReader`, `AccessPolicy`, `AccessStateReader`, `AccessFilter`, `ContentPage`, `Summary`, and `SearchScan` for Tasks 4 through 11.

```go
type SearchAccess struct { Versions, Keys []string; Generation int64 }
type ContentRequest struct { NodeID uuid.UUID; Cursor, ProjectionConfig string; AccessVersions []string; MaxBytes int; SearchGeneration int64 }
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
type AccessState struct { AuthorityID uuid.UUID; ActiveVersion string; WriteVersions []string; Generation int64 }
type AccessStateReader interface { State(context.Context, uuid.UUID) (AccessState, error) }
type AccessFilter struct { AuthorityID uuid.UUID; Version string; Keys []string }
type IndexAccessRequest struct { NodeID uuid.UUID; Versions []string; Generation int64 }
type AccessRequest struct { PrincipalID, EntryPointID, ScopeID, AuthorityID uuid.UUID; Version string }
type OrgScopeSource interface {
    Resource(context.Context, uuid.UUID) (uuid.UUID, []uuid.UUID, error)
    Entry(context.Context, uuid.UUID) (uuid.UUID, error)
}
type AccessPolicy interface {
    Supports(string) bool
    ResourceAuthority(context.Context, uuid.UUID) (uuid.UUID, error)
    EntryAuthority(context.Context, uuid.UUID) (uuid.UUID, error)
    Index(context.Context, IndexAccessRequest) (node.SearchAccess, error)
    Query(context.Context, AccessRequest) (search.AccessFilter, error)
}
type AccessCompiler interface {
    Index(context.Context, IndexAccessRequest) (node.SearchAccess, error)
    Query(context.Context, AccessRequest) (search.AccessFilter, error)
}
type PolicySet struct { Source OrgScopeSource; Compilers map[string]AccessCompiler }
```

- [ ] **Step 1: Add the failing multi-page and access test.**

```go
func TestSearchReaderPages(t *testing.T) {
    stores := newSearchStore(t)
    id := putSearchText(t, stores, strings.Repeat("é水🙂 ", 300)+"tail-Z")
    authorityID, err := stores.Access.ResourceAuthority(t.Context(), id)
    if err != nil { t.Fatal(err) }
    accessState, err := stores.AccessStates.State(t.Context(), authorityID)
    if err != nil { t.Fatal(err) }
    request := node.ContentRequest{NodeID:id, MaxBytes:128, AccessVersions:accessState.WriteVersions, SearchGeneration:1}
    var unique strings.Builder
    for ordinal := uint64(0); ; ordinal++ {
        page, err := stores.Views.Content(t.Context(), request)
        if err != nil { t.Fatal(err) }
        if !utf8.ValidString(page.Text) || len(page.Text) > 128 { t.Fatalf("invalid page %d", ordinal) }
        if page.Ordinal != ordinal || !slices.Contains(page.Access.Versions, "org-scope-v1") || page.Access.Generation < 1 { t.Fatalf("bad identity: %#v", page) }
        unique.WriteString(page.Text[page.OverlapBytes:])
        if page.Done { break }
        if page.NextCursor == "" || page.NextCursor == request.Cursor { t.Fatal("cursor did not advance") }
        request.Cursor = page.NextCursor
    }
    if !strings.Contains(unique.String(), "tail-Z") { t.Fatal("final text missing") }
}
```

- [ ] **Step 2: Record the deferred failure contract.**

Task 13 runs `^TestSearchReaderPages$` against the completed branch. The test must fail when paging, revision binding, complete text, or access projection is absent. Do not start FoundationDB during this coding task.

- [ ] **Step 3: Use the projection declarations from Task 2.**

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
type OrgScopeCompiler struct { Source OrgScopeSource }
func (p OrgScopeCompiler) Index(ctx context.Context, request IndexAccessRequest) (node.SearchAccess, error) {
    orgID, scopeIDs, err := p.Source.Resource(ctx, request.NodeID)
    if err != nil { return node.SearchAccess{}, err }
    keys := make([]string, 0, len(scopeIDs))
    for _, version := range request.Versions {
        for _, scopeID := range scopeIDs { keys = append(keys, EncodeKey(version, orgID[:], scopeID[:])) }
    }
    slices.Sort(keys)
    keys = slices.Compact(keys)
    return node.SearchAccess{Versions:slices.Clone(request.Versions), Keys:keys, Generation:request.Generation}, nil
}
func (p OrgScopeCompiler) Query(ctx context.Context, request AccessRequest) (search.AccessFilter, error) {
    orgID, err := p.Source.Entry(ctx, request.EntryPointID)
    if err != nil { return search.AccessFilter{}, err }
    if orgID != request.AuthorityID { return search.AccessFilter{}, search.ErrAccessState }
    return search.AccessFilter{AuthorityID:orgID, Version:request.Version, Keys:[]string{EncodeKey(request.Version, orgID[:], request.ScopeID[:])}}, nil
}
```

Implement `AccessPolicy` with `PolicySet` from the first release. `Supports` checks the compiler map. `ResourceAuthority` and `EntryAuthority` return the organization from `Source`. `Index` rejects an unregistered version, calls each requested compiler with one version, and merges its keys. `Query` dispatches one version. `EncodeKey` length-prefixes the version and every byte part, hashes the result with SHA-256, and returns `version + ":" + base64.RawURLEncoding.EncodeToString(digest[:])`. Search never decodes the result. Production registers `org-scope-v1` with `OrgScopeCompiler`; a transition registers the candidate compiler beside it before `Begin` can succeed.

Store one stable `AccessState` for each organization during initialization. The scheduler copies its write versions and generation into durable work. `Content` calls `AccessPolicy.Index` with those fixed values. Sort and deduplicate versions and keys. Reject empty values, duplicates, unsupported versions, and nonpositive generations. Page mapping only copies `ContentPage.Access`.

A future policy reads its permission-definition node and related nodes through `NodeReader`. Its opaque version identifies the immutable definition revision. It implements this key contract without changing the mapping, document identity, or search interfaces.

- [ ] **Step 6: Implement bounded UTF-8 pages and revision-bound cursors.**

The writer records at most `MaxBytes`, reserves at most one quarter for prior context, splits at rune boundaries, and records the unique-text offset separately. Encode node ID, revision, organization projection epoch, projection config hash, pagination version, byte bound, next unique offset, and ordinal in the cursor. Validate all values in one FDB transaction. Return `ErrContentChanged` after an edit and `ErrNotFound` after deletion. Reject `MaxBytes < 16`.

- [ ] **Step 7: Implement bounded scans and summaries.**

Embed `ContentReader` in `NodeReader`; the concrete `ViewStore` implements both. Scan global node-resolution keys in raw-key order with a caller limit. Build summaries from current identity, ancestry, and bounded name and text prefixes. Do not list organizations through SQL or assemble every page.

- [ ] **Step 8: Add failure and stability coverage.**

Test complete text after removing overlap, reordered maps, exclusions, unfamiliar types, long names, malformed declarations, edits, ancestry, deletion, and empty nonfinal pages. Replace every permission identifier and require the same decisions. Every nonfinal cursor must advance; pages after an edit must return `node.ErrContentChanged`.

- [ ] **Step 9: Run the serial coding checks.**

Run: `go test ./internal/test/integration -run '^$' -count=1`

Run: `make check`

Expected: PASS after compiling the integration package without executing its tests. Task 13 runs the real FoundationDB checks.

- [ ] **Step 10: Commit the task.**

```sh
git add internal/domain/node internal/domain/search/filter.go internal/searchaccess/access.go internal/adapters/foundationdb internal/test/integration/search_reader_test.go internal/test/integration/search_store_fixture_test.go
git commit -S -m "Add metadata-driven paginated node content reads" -m "Co-authored-by: Codex <noreply@openai.com>"
```
