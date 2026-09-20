# Paginated Node Reader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return complete searchable text as stable pages through the node reader.

**Architecture:** Metadata declares how JSON values become text. The initial adapter reads today's bounded record, emits bounded UTF-8 pages, and checks revision identity on every continuation. Its storage implementation can later change without changing callers.

**Tech Stack:** Go, FoundationDB, JSON, existing NodeReader.

**Spec:** [Paginated reads and searchable content](../specs/2026-09-19-search-design.md#searchable-content).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Preserve the current node storage model. Test successive parts using smaller byte bounds on the real reader.

## Review Focus

Test edited revisions, reordered maps, structured values under unfamiliar types, names spanning pages, and UTF-8 characters at page boundaries.

---

## Task 2: Declare generic projection and reader interfaces

Create these focused files:

```text
internal/domain/node/content.go                    page and summary contracts
internal/domain/node/search_projection.go          declarative JSON representation
internal/adapters/foundationdb/node_content.go      bounded current-record adapter
internal/adapters/foundationdb/node_content_cursor.go revision-bound continuation
internal/adapters/foundationdb/node_summary.go      bounded current result content
internal/adapters/foundationdb/search_revision.go   transaction revision counters
internal/domain/node/search_projection_emit.go     generic text interpreter
internal/test/integration/search_reader_test.go    real store tests
internal/test/integration/search_store_fixture_test.go opaque fixture construction
```

Modify [PropertyDef](../../../internal/domain/node/types.go), [NodeReader](../../../internal/domain/node/reader.go), and the FDB mutation entry points identified in the worker tasks. The reader consumes metadata from the same transaction as the node. Do not add search-specific fields to the universal Node struct.

Interfaces produced in `domain/node`:

```go
type TextRule struct {
    Mode string `json:"mode"`
    Fields []TextField `json:"fields,omitempty"`
    Items *TextRule `json:"items,omitempty"`
    Labels map[string]string `json:"labels,omitempty"`
}
type TextField struct { Key string `json:"key"`; Rule TextRule `json:"rule"` }
type SearchProjection struct { Include bool `json:"include"`; Order int `json:"order"`; Rule TextRule `json:"rule"` }
type ContentRequest struct { NodeID uuid.UUID; Cursor, ProjectionConfig string; MaxBytes int }
var ErrContentChanged = errors.New("node content changed during pagination")
type ContentPage struct {
    NodeID, OrgID uuid.UUID
    NodeType, Revision, ProjectionVersion, Name, Text, NextCursor string
    ScopeIDs []uuid.UUID
    Ordinal uint64
    OverlapBytes int
    Done bool
}
type Summary struct { NodeID, OrgID uuid.UUID; NodeType, Name, Text string; ScopeIDs []uuid.UUID }
type SearchScan struct { NodeIDs []uuid.UUID; NextCursor string; Done bool }
type ContentReader interface {
    Content(ctx context.Context, request ContentRequest) (ContentPage, error)
    Summary(ctx context.Context, nodeID uuid.UUID, maxBytes int) (Summary, error)
    ScanSearch(ctx context.Context, cursor string, limit int) (SearchScan, error)
}
```

Add `Search *SearchProjection` to PropertyDef. A nil declaration is invalid for
an applicable definition, including an unfamiliar PropertyType. `Include:false`
explicitly excludes it. Existing `Indexed` continues to mean FDB secondary indexing.

- [ ] Add newSearchStore and putSearchText from the [real-store fixture code](2026-09-19-opensearch-fixtures.md#reader-fixture-for-task-2).
- [ ] Add the failing public reader test:

```go
func TestSearchReaderPages(t *testing.T) {
    stores := newSearchStore(t)
    text := strings.Repeat("é水🙂 ", 300) + "final-character-Z"
    id := putSearchText(t, stores, text)
    request := node.ContentRequest{NodeID: id, MaxBytes: 128}
    var previous node.ContentPage
    pages := 0
    tailSeen := false
    for {
        page, err := stores.Views.Content(t.Context(), request)
        if err != nil { t.Fatal(err) }
        if !utf8.ValidString(page.Text) || len(page.Text) > 128 { t.Fatal("invalid bounded text") }
        if pages > 0 && (page.Revision != previous.Revision || page.ProjectionVersion != previous.ProjectionVersion || page.Ordinal != previous.Ordinal+1) { t.Fatal("unstable read") }
        tailSeen = tailSeen || strings.Contains(page.Text, "final-character-Z")
        pages++
        if page.Done { break }
        if page.NextCursor == "" || page.NextCursor == request.Cursor { t.Fatal("no progress") }
        previous, request.Cursor = page, page.NextCursor
    }
    if pages < 2 || !tailSeen { t.Fatal("incomplete multi-page read") }
}
```

- [ ] Run `^TestSearchReaderPages$`. Expect missing reader methods before implementation.
- [ ] Implement `node.EmitSearchText(ctx context.Context, view *NodeView, defs []*PropertyDef, out io.Writer) error`. Sort applicable declarations by Order, then definition UUID. Emit the full name first. Check context between values, then call this recursive writer for each included value. Wrap its errors with node and property IDs. Never dispatch on PropertyType, NodeType, or property spelling.

```go
func emitSearchValue(raw json.RawMessage, rule TextRule, out io.Writer) error {
    raw = bytes.TrimSpace(raw)
    if len(raw) == 0 || bytes.Equal(raw, []byte("null")) { return nil }
    if !json.Valid(raw) { return errors.New("malformed JSON") }
    switch rule.Mode {
    case "scalar":
        text := string(raw)
        if raw[0] == '"' {
            if err := json.Unmarshal(raw, &text); err != nil { return err }
        } else if raw[0] == '{' || raw[0] == '[' { return errors.New("expected scalar") }
        if len(rule.Labels) != 0 {
            label, exists := rule.Labels[text]
            if !exists { return fmt.Errorf("undeclared display value %q", text) }
            text = label
        }
        _, err := io.WriteString(out, text+"\n")
        return err
    case "array":
        if rule.Items == nil { return errors.New("array has no item rule") }
        var values []json.RawMessage
        if err := json.Unmarshal(raw, &values); err != nil { return err }
        for _, value := range values {
            if err := emitSearchValue(value, *rule.Items, out); err != nil { return err }
        }
        return nil
    case "object", "values":
        var values map[string]json.RawMessage
        if err := json.Unmarshal(raw, &values); err != nil { return err }
        if rule.Mode == "object" {
            for _, field := range rule.Fields {
                if err := emitSearchValue(values[field.Key], field.Rule, out); err != nil { return err }
            }
        } else {
            if rule.Items == nil { return errors.New("object values have no item rule") }
            for _, key := range slices.Sorted(maps.Keys(values)) {
                if err := emitSearchValue(values[key], *rule.Items, out); err != nil { return err }
            }
        }
        return nil
    default:
        return fmt.Errorf("invalid text representation %q", rule.Mode)
    }
}
```

Each decoded leaf emits one separating newline. JSON null and absent values emit
nothing. Wrong JSON shapes, unknown labels, duplicate declared object keys,
recursive-rule depth beyond 64 levels, and malformed JSON
return errors with node and property IDs. Validate declarations when metadata is
written; do not silently reinterpret invalid stored declarations during a read.

- [ ] Implement an `io.Writer` that records only the requested page and bounded adjacent context while advancing a UTF-8 byte offset. Split at valid rune boundaries; reserve at most one quarter of the page for repeated preceding context. Return its length in OverlapBytes and record the unique-text offset separately. An empty final page may set Done; an empty nonfinal page must still advance. Never append all emitted text to a slice. Reject MaxBytes below 16. The initial adapter may replay its bounded record to reach the offset; the later storage adapter must seek without decoding earlier pages.
- [ ] Store node revision counters and an organization projection epoch in separate FDB keys. Increment the node revision with every node write or deletion; increment the epoch with metadata or relationship changes. Encode node ID, revision, epoch, ProjectionConfig, pagination algorithm version, byte bound, next unique-text offset, and ordinal in the reader cursor. Read and validate them in one transaction before each page. Return ErrContentChanged on mismatch, ErrNotFound on deletion, and an explicit corruption error for missing expected records. ProjectionConfig is an opaque hash of the approved mapping/model configuration; the reader does not inspect its model settings.
- [ ] Implement `ScanSearch` over the FDB global node resolution keys with bounded range reads. Its cursor advances by raw key; it does not list organizations from SQL or decode whole nodes. Implement Summary from current node identity, current ancestry, and bounded name/text prefixes. Initial decoding may read the existing bounded value; later storage must supply those prefixes without assembling all pages.
- [ ] Add assertions for complete decoded coverage after removing recorded overlap, deterministic text across reordered maps, invalid declarations, excluded/inapplicable values, a long name, and a cursor resumed after `Nodes.Set`. Assert `errors.Is(err, node.ErrContentChanged)` for the edit. Vary part counts across reads and after edits. Require indexing before the final part is read and completion only after Done. Repeat with the production byte budget without asserting a particular part count.
- [ ] Run `^TestSearchReader`, run `make check`, and commit with subject `Add metadata-driven paginated node content reads`.

Embed ContentReader into NodeReader when assembling the new runtime. Until that
assembly commit, the concrete ViewStore implements both interfaces. This avoids
changing unrelated reader implementations before their callers are replaced.

## Task 3: Accept metadata changes after startup

Modify the metadata collection and registration logic in [MCP server assembly](../../../internal/adapters/mcp/server.go). Expose projection declarations through the existing metadata administration boundary rather than introducing a search-specific property list.

Interfaces consumed: `PropertyDefStore.Set`, `NodeTypeStore.Set`, and the reader methods above. Produce `ViewStore.ProjectionVersion(ctx context.Context, nodeID uuid.UUID) (string, error)` and add it to NodeReader; this returns the current organization epoch and reader configuration version.

- [ ] Add an authenticated MCP test that searches an opaque type, writes a new type and projection through the exported metadata store operations, then searches the new type with the same authenticated session. Repeat with every identifier changed. Require identical coverage and relative ranks.
- [ ] Run `^TestSearchMetadataAfterStartup$`; expect the stale registration or absent projection path to fail.
- [ ] Key cached metadata by the current organization epoch. Reload declarations and rebuild the per-user tool server when that epoch changes. Preserve caller membership checks during reload. A failed metadata read must return an error, not retain a successful empty type list.
- [ ] Run the metadata test and `make check`; commit with subject `Refresh search metadata after declaration changes`.
