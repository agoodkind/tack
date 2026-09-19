# Tack MCP Output Bounds and Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tack MCP tools return bounded, paginated output, and `tack_search` finds tickets by keyword and by per-org synonyms.

**Architecture:** FoundationDB gains a cursor-paged read (`NodeReader.ListPage`) that every MCP list tool uses. Write tools return a short confirmation instead of the full node. Meilisearch search reports an error when it is unavailable, gets a backfill command and metadata-driven searchable fields, and expands queries with synonym nodes that each org owns.

**Tech Stack:** Go, FoundationDB Go bindings, `mark3labs/mcp-go`, `meilisearch-go` v0.36.3, Meilisearch v1.12, Docker Compose test stack.

**Spec:** this plan. Tickets: TACK-508 (Phase A), TACK-509 (Phase B), TACK-510 (Phase C).

## Tickets

| Phase | Ticket | Priority | Depends on |
| --- | --- | --- | --- |
| A. Bound MCP tool output | TACK-508 | high | none |
| B. Make keyword search work | TACK-509 | urgent | Task 1 (test harness) |
| C. Per-org synonyms | TACK-510 | medium | TACK-509 |

Move each ticket to `TACK::In Progress` when its first task starts and to `TACK::Done` after its acceptance checks pass on production. Use `tack_set_issue_state`.

## Verified causes

1. List tools return every row. `listHandler` never sets `NodeListQuery.Limit` or `Cursor` (`internal/adapters/mcp/tools/node_list_create.go:31-56`). `ViewStore.List` reads full ranges with `fdb.RangeOptions{}` (`internal/adapters/foundationdb/view.go:152-214`). No tool schema has `limit` or `cursor`.
2. Create, update, and `tack_set_*` return `renderNode`, which prints every property including the full description (`node_list_create.go:168`, `node_read_update_delete.go:130`, `reference_property_tool.go:160`, `render_node.go:40-56`).
3. `tack_describe_workspace` lists every child with an org-wide scan (`workspace.go:86-91`).
4. Comment and activity rows print the name twice because `nodeListItem` uses the name as the title when the type has no reference (`render_collection.go:89-97`).
5. `tack_search` returned 0 results in production on 2026-09-18 for `backup` and for the exact title text `Temporal-DB WAL archive`. `TACK-275` returned 1 result because the exact-reference step reads FDB (`search.go:51`).
6. `buildSearcher` falls back to `searchadapter.Noop{}` on setup failure, and `Noop.Search` returns no results and no error (`internal/runtime/graph.go:132-144`, `internal/adapters/search/search.go:15-20`). No command backfills Meilisearch from FDB.

## Global Constraints

- No file exceeds 200 lines. `view.go` is already 300 lines, so new FDB code goes in new files.
- Behavior follows NodeType and PropertyDef metadata. Never compare a type key or property name to a string literal outside `internal/service/seed.go`.
- Build with `make build`. Never run `go build` directly.
- Tests use the real stack through `docker-compose.test.yml`: `make test-unit` and `make test-integration`. No mocks or fakes in new tests. Existing fakes in `internal/adapters/mcp/tools/*_test.go` only gain the methods the compiler requires.
- Every new user-visible path adds `ops qa datagen` coverage in the same change.
- Every `./server ops` command registers through the `clispec` audit choke-point. Commands never declare their own `execute` flag.
- Log with `telemetry.L(ctx)` and `noun.verb` message names with named `slog.Attr` fields.
- Errors include context and an identifier: `fmt.Errorf("list page %s: %w", nodeType, err)`.
- Commit with `git commit -S`, subject in imperative mood with the ticket in parentheses, ending with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.
- Validate on QA before production. Deploy only from `main` after `git fetch` confirms `HEAD == origin/main`.

## File structure

| File | Change | Responsibility |
| --- | --- | --- |
| `internal/domain/node/cursor.go` | create | Encode and decode an opaque page cursor |
| `internal/domain/node/reader.go` | modify | Add `NodePage` and `NodeReader.ListPage` |
| `internal/adapters/foundationdb/view_page.go` | create | Cursor-paged FDB reads |
| `internal/test/integration/mcp_harness.go` | create | Call MCP tools through the production HTTP handler |
| `internal/adapters/mcp/tools/list_page_args.go` | create | Parse `limit` and `cursor` tool arguments |
| `internal/adapters/mcp/tools/node_tool_schema.go` | modify | Add `limit` and `cursor` to list tool schemas |
| `internal/adapters/mcp/tools/node_list_create.go` | modify | Use `ListPage`; short create confirmation |
| `internal/adapters/mcp/tools/render_write.go` | create | Short confirmation for create, update, and set |
| `internal/adapters/mcp/tools/templates/collection.md.tmpl` | modify | Print the next cursor |
| `internal/adapters/mcp/tools/workspace.go` | modify | Count children per type |
| `internal/adapters/mcp/tools/response.go` | modify | Byte cap on success text |
| `internal/domain/search/searcher.go` | modify | `ErrUnavailable`, `IndexBatch`, `SearchVariants` |
| `internal/adapters/search/meilisearch.go` | modify | Searchable attributes, batch index |
| `internal/adapters/search/meilisearch_multi.go` | create | Federated multi-search |
| `internal/service/search_doc.go` | create | Build a search document from a view and property defs |
| `internal/ops/search_reindex.go` | create | Backfill Meilisearch from FDB |
| `internal/adapters/mcp/tools/search_synonyms.go` | create | Expand a query with org synonym nodes |
| `internal/datagen/generate_search_checks.go` | create | Datagen search assertions |
| `docker-compose.test.yml` | modify | Add Meilisearch to the test stack |

---

## Phase A: Bound MCP tool output (TACK-508)

### Task 1: MCP integration test harness

The integration suite has no test that calls MCP tools. `datagen.Driver` already sends JSON-RPC `tools/call` requests to `graph.MCPHandler` with real tokens (`internal/datagen/driver.go`, `internal/datagen/session.go` `RunSeed` commit branch). The harness reuses that path.

**Files:**
- Create: `internal/test/integration/mcp_harness.go`
- Test: `internal/test/integration/mcp_harness_test.go`

**Interfaces:**
- Produces: `func NewMCPHarness(t *testing.T) *MCPHarness`, `func (h *MCPHarness) Call(t *testing.T, toolName string, args datagen.ToolArguments) datagen.Result`, `func (h *MCPHarness) CallExpectError(t *testing.T, toolName string, args datagen.ToolArguments) string`, fields `Workspace string` and `Project string`.

- [ ] **Step 1: Read the identity bootstrap.** Open `internal/datagen/session.go` and copy the exact calls the commit branch of `RunSeed` makes: `config.Load` (or the equivalent), `runtime.BuildGraph`, `BootstrapIdentities`, and `NewDriver`. Use the same signatures in Step 3.

- [ ] **Step 2: Write the failing test**

```go
package integration

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

func TestMCPHarnessListsWorkspaces(t *testing.T) {
	harness := NewMCPHarness(t)

	result := harness.Call(t, "tack_list_workspaces", datagen.ToolArguments{})

	if !strings.Contains(result.Text(), harness.Workspace) {
		t.Fatalf("workspace %q missing from list:\n%s", harness.Workspace, result.Text())
	}
}
```

- [ ] **Step 3: Run it to confirm it fails**

Run: `make test-integration`
Expected: FAIL with `undefined: NewMCPHarness`.

- [ ] **Step 4: Implement the harness**

Build the graph from the test environment variables, bootstrap one user, one org, one workspace, and one project through MCP create tools, and keep the token. Use the Step 1 signatures. The shape is:

```go
package integration

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
)

// MCPHarness calls MCP tools through the production HTTP handler with a real
// bearer token, so each call runs auth, org membership, audit, and rendering.
type MCPHarness struct {
	ctx       context.Context
	driver    *datagen.Driver
	token     string
	Workspace string
	Project   string
}

// NewMCPHarness builds the runtime graph against the test stack and creates a
// fresh workspace and project for the calling test.
func NewMCPHarness(t *testing.T) *MCPHarness {
	t.Helper()
	requireIntegration(t)
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	graph, err := appruntime.BuildGraph(ctx, cfg)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	t.Cleanup(graph.Close)
	identities, err := datagen.BootstrapIdentities(ctx, graph /* Step 1 arguments */)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	harness := &MCPHarness{
		ctx:    ctx,
		driver: datagen.NewDriver(graph.MCPHandler, false, 1),
		token:  identities.Token, // Step 1 field name
	}
	harness.createWorkspaceAndProject(t)
	return harness
}
```

`requireIntegration` wraps the existing `TACK_INTEGRATION` skip in `setup.go`; extract it there if it is inline. `createWorkspaceAndProject` calls `tack_create_project` with a unique identifier built from `t.Name()` and stores the printed references. `Call` fails the test when `driver.Call` returns an error. `CallExpectError` fails the test when the call succeeds and returns the error text.

- [ ] **Step 5: Run the test to confirm it passes**

Run: `make test-integration`
Expected: PASS for `TestMCPHarnessListsWorkspaces`.

- [ ] **Step 6: Commit**

```bash
git add internal/test/integration/mcp_harness.go internal/test/integration/mcp_harness_test.go internal/test/integration/setup.go
git commit -S -m "Add MCP integration harness that calls tools through the HTTP handler (TACK-508)"
```

### Task 2: Cursor-paged FDB reads

**Files:**
- Create: `internal/domain/node/cursor.go`
- Modify: `internal/domain/node/reader.go:64-80`
- Create: `internal/adapters/foundationdb/view_page.go`
- Modify: every `NodeReader` implementation the compiler reports (known: `internal/adapters/mcp/tools/resolve_typed_test.go` `resolverReader`, `render_test.go` `fakeReader`, `internal/ops/repair_console_fakes_test.go` `repairReader`)
- Test: `internal/test/integration/view_page_test.go`

**Interfaces:**
- Produces:
  - `type NodePage struct { Views []*NodeView; NextCursor string }`
  - `NodeReader.ListPage(ctx context.Context, q NodeListQuery) (NodePage, error)`. It requires `q.Limit > 0`. It supports `ByProperty` and the full type scan. It returns `domain.ErrInvalidArgument` for relation scans.
  - `func EncodeCursor(lastID uuid.UUID) string`, `func DecodeCursor(cursor string) (uuid.UUID, error)`

- [ ] **Step 1: Write the failing test**

```go
package integration

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

const pagedIssueCount = 150

func TestListPageReturnsEveryIssueOnce(t *testing.T) {
	env := SetupTestEnv(t)
	projectID := createTestProject(t, env)
	for i := 0; i < pagedIssueCount; i++ {
		createTestIssue(t, env, projectID, fmt.Sprintf("paged issue %d", i))
	}
	projectRaw, _ := json.Marshal(projectID.String())
	query := node.NodeListQuery{
		OrgID:      env.OrgID,
		NodeType:   "issue",
		ByProperty: &node.PropertyMatch{PropName: "scope_id", Value: projectRaw},
		Limit:      25,
	}

	seen := map[uuid.UUID]bool{}
	pageCount := 0
	for {
		page, err := env.Stores.Views.ListPage(env.Ctx, query)
		if err != nil {
			t.Fatalf("list page %d: %v", pageCount, err)
		}
		pageCount++
		for _, view := range page.Views {
			if seen[view.ID] {
				t.Fatalf("issue %s returned twice", view.ID)
			}
			seen[view.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
	}

	if len(seen) != pagedIssueCount {
		t.Fatalf("saw %d issues, want %d", len(seen), pagedIssueCount)
	}
	if pageCount != 6 {
		t.Fatalf("page count = %d, want 6", pageCount)
	}
}
```

Add `createTestProject` and `createTestIssue` to `internal/test/integration/setup_nodes.go` using `env.NodeSvc.Create(env.Ctx, service.CreateInput{...})` the way `audit_intent_test.go:26-28` creates a workspace. The type key literals are allowed here because test fixtures mirror the seed.

- [ ] **Step 2: Run it to confirm it fails**

Run: `make test-integration`
Expected: FAIL with `env.Stores.Views.ListPage undefined`.

- [ ] **Step 3: Add the domain types**

`internal/domain/node/cursor.go`:

```go
package node

import (
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
)

// EncodeCursor returns the opaque cursor that resumes a paged list after
// lastID. Callers must treat the value as opaque.
func EncodeCursor(lastID uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(lastID[:])
}

// DecodeCursor returns the node ID a cursor resumes after.
func DecodeCursor(cursor string) (uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return uuid.Nil, fmt.Errorf("decode cursor %q: %w", cursor, domain.ErrInvalidArgument)
	}
	lastID, err := uuid.FromBytes(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("decode cursor %q: %w", cursor, domain.ErrInvalidArgument)
	}
	return lastID, nil
}
```

Add to `reader.go` after `NodeStreamResult`:

```go
// NodePage is one page of a ListPage call. NextCursor is empty on the last
// page.
type NodePage struct {
	Views      []*NodeView
	NextCursor string
}
```

Add to the `NodeReader` interface:

```go
	// ListPage returns at most q.Limit views after q.Cursor, in node ID
	// order. It supports ByProperty and full type scans.
	ListPage(ctx context.Context, q NodeListQuery) (NodePage, error)
```

- [ ] **Step 4: Implement `ListPage` in FDB**

`internal/adapters/foundationdb/view_page.go`:

```go
package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// pageBatchSize bounds each FDB range read so one transaction stays well
// under the 5 second and 10 MB limits.
const pageBatchSize = 256

type pageEntry struct {
	id   uuid.UUID
	view *node.NodeView
}

// ListPage reads batches in key order until it has q.Limit matching views
// plus one more, which proves another page exists.
func (s *ViewStore) ListPage(ctx context.Context, q node.NodeListQuery) (page node.NodePage, err error) {
	defer telemetry.FDBOp(ctx, "store.view.list_page")(&err)
	if q.Limit <= 0 {
		return node.NodePage{}, fmt.Errorf("list page %s: limit must be positive: %w", q.NodeType, domain.ErrInvalidArgument)
	}
	if q.BySourceRelation != nil || q.ByTargetRelation != nil {
		return node.NodePage{}, fmt.Errorf("list page %s: relation scans are not paged: %w", q.NodeType, domain.ErrInvalidArgument)
	}
	prefix := s.pagePrefix(q)
	begin, err := s.pageBegin(q, prefix)
	if err != nil {
		return node.NodePage{}, err
	}
	prefixRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return node.NodePage{}, fmt.Errorf("list page %s: %w", q.NodeType, err)
	}
	end := fdb.FirstGreaterOrEqual(prefixRange.End)
	matched := make([]*node.NodeView, 0, q.Limit+1)
	for len(matched) <= q.Limit {
		entries, lastKey, readErr := s.readPageBatch(q, fdb.SelectorRange{Begin: begin, End: end})
		if readErr != nil {
			return node.NodePage{}, readErr
		}
		for _, entry := range entries {
			if entry.view != nil && matchesPageFilters(entry.view, q) {
				matched = append(matched, entry.view)
			}
			if len(matched) > q.Limit {
				break
			}
		}
		if len(entries) < pageBatchSize {
			break
		}
		begin = fdb.FirstGreaterThan(lastKey)
	}
	if len(matched) <= q.Limit {
		return node.NodePage{Views: matched}, nil
	}
	views := matched[:q.Limit]
	return node.NodePage{Views: views, NextCursor: node.EncodeCursor(views[len(views)-1].ID)}, nil
}

func (s *ViewStore) pagePrefix(q node.NodeListQuery) []byte {
	if q.ByProperty != nil {
		return nodeByPropertyValuePrefix(q.OrgID, q.NodeType, q.ByProperty.PropName, encodePropertyValue(q.ByProperty.Value))
	}
	return nodeViewPrefix(q.OrgID, q.NodeType)
}

func (s *ViewStore) pageBegin(q node.NodeListQuery, prefix []byte) (fdb.KeySelector, error) {
	if q.Cursor == "" {
		return fdb.FirstGreaterOrEqual(fdb.Key(prefix)), nil
	}
	lastID, err := node.DecodeCursor(q.Cursor)
	if err != nil {
		return fdb.KeySelector{}, err
	}
	if q.ByProperty != nil {
		key := nodeByPropertyKey(q.OrgID, q.NodeType, q.ByProperty.PropName, encodePropertyValue(q.ByProperty.Value), lastID)
		return fdb.FirstGreaterThan(fdb.Key(key)), nil
	}
	return fdb.FirstGreaterThan(fdb.Key(nodeViewKey(q.OrgID, q.NodeType, lastID))), nil
}

func matchesPageFilters(view *node.NodeView, q node.NodeListQuery) bool {
	if !matchPropFilters(view, q.PropFilters) {
		return false
	}
	if q.CreatedAfter != nil && view.CreatedAt.Before(*q.CreatedAfter) {
		return false
	}
	if q.CreatedBefore != nil && view.CreatedAt.After(*q.CreatedBefore) {
		return false
	}
	return true
}
```

Put `readPageBatch` in `internal/adapters/foundationdb/view_page_batch.go` to keep each file under 200 lines:

```go
package foundationdb

// readPageBatch reads one batch of at most pageBatchSize keys and returns the
// decoded entries plus the last raw key read. For ByProperty scans it reads
// the view for each index entry; for full scans the value is the view.
func (s *ViewStore) readPageBatch(q node.NodeListQuery, keyRange fdb.SelectorRange) ([]pageEntry, fdb.Key, error) {
	result, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		kvs, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: pageBatchSize}).GetSliceWithError()
		if err != nil {
			return nil, err
		}
		entries := make([]pageEntry, 0, len(kvs))
		for _, kv := range kvs {
			entry, decodeErr := decodePageEntry(tr, q, kv)
			if decodeErr != nil {
				return nil, decodeErr
			}
			entries = append(entries, entry)
		}
		var lastKey fdb.Key
		if len(kvs) > 0 {
			lastKey = kvs[len(kvs)-1].Key
		}
		return pageBatch{entries: entries, lastKey: lastKey}, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list page %s batch: %w", q.NodeType, err)
	}
	batch := result.(pageBatch)
	return batch.entries, batch.lastKey, nil
}

type pageBatch struct {
	entries []pageEntry
	lastKey fdb.Key
}

func decodePageEntry(tr fdb.ReadTransaction, q node.NodeListQuery, kv fdb.KeyValue) (pageEntry, error) {
	if q.ByProperty == nil {
		var view node.NodeView
		if err := json.Unmarshal(kv.Value, &view); err != nil {
			return pageEntry{}, fmt.Errorf("decode view %x: %w", kv.Key, err)
		}
		return pageEntry{id: view.ID, view: &view}, nil
	}
	unpacked, err := tuple.Unpack(stripPrefix(kv.Key))
	if err != nil || len(unpacked) < 6 {
		return pageEntry{}, fmt.Errorf("decode property index key %x: %w", kv.Key, err)
	}
	nodeIDText, _ := unpacked[5].(string)
	nodeID, err := uuid.Parse(nodeIDText)
	if err != nil {
		return pageEntry{}, fmt.Errorf("decode node id %q: %w", nodeIDText, err)
	}
	raw, err := tr.Get(fdb.Key(nodeViewKey(q.OrgID, q.NodeType, nodeID))).Get()
	if err != nil {
		return pageEntry{}, fmt.Errorf("read view %s: %w", nodeID, err)
	}
	if len(raw) == 0 {
		return pageEntry{id: nodeID}, nil
	}
	var view node.NodeView
	if err := json.Unmarshal(raw, &view); err != nil {
		return pageEntry{}, fmt.Errorf("decode view %s: %w", nodeID, err)
	}
	return pageEntry{id: nodeID, view: &view}, nil
}
```

An index entry whose view is missing yields `view == nil`. `ListPage` skips it but still advances past its key.

- [ ] **Step 5: Add `ListPage` to each fake the compiler names.** Each fake returns `node.NodePage{Views: <its List result truncated to q.Limit>}`. Run `make build` until it compiles.

- [ ] **Step 6: Run the test to confirm it passes**

Run: `make test-integration`
Expected: PASS for `TestListPageReturnsEveryIssueOnce`.

- [ ] **Step 7: Commit**

```bash
git add internal/domain/node internal/adapters/foundationdb/view_page.go internal/adapters/foundationdb/view_page_batch.go internal/test/integration internal/adapters/mcp/tools internal/ops
git commit -S -m "Add cursor-paged NodeReader.ListPage for property and type scans (TACK-508)"
```

### Task 3: `limit` and `cursor` on MCP list tools

**Files:**
- Create: `internal/adapters/mcp/tools/list_page_args.go`
- Modify: `internal/adapters/mcp/tools/node_tool_schema.go:11-20`
- Modify: `internal/adapters/mcp/tools/node_list_create.go:31-65`
- Modify: `internal/adapters/mcp/tools/render_collection.go:11-18`, `collection_template.go` (the file that defines `collectionTemplateData`), `templates/collection.md.tmpl`
- Test: `internal/test/integration/mcp_list_page_test.go`

**Interfaces:**
- Consumes: `NodeReader.ListPage`, `NewMCPHarness`.
- Produces: `func pageArgs(args argMap) (limit int, cursor string, err error)`, `const defaultListLimit = 25`, `const maxListLimit = 100`, `func pageSchemaFields() []schemaField`, `collectionTemplateData.NextCursor string`.

- [ ] **Step 1: Write the failing test**

```go
package integration

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

var nextCursorPattern = regexp.MustCompile("Next cursor: `([^`]+)`")

func TestListIssuesPagesThroughEveryIssue(t *testing.T) {
	harness := NewMCPHarness(t)
	for i := 0; i < pagedIssueCount; i++ {
		harness.Call(t, "tack_create_issue", datagen.ToolArguments{
			"workspace_reference": harness.Workspace,
			"project_reference":   harness.Project,
			"name":                fmt.Sprintf("paged issue %03d", i),
		})
	}

	seen := map[string]bool{}
	arguments := datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
		"project_reference":   harness.Project,
	}
	for {
		text := harness.Call(t, "tack_list_issues", arguments).Text()
		for i := 0; i < pagedIssueCount; i++ {
			name := fmt.Sprintf("paged issue %03d", i)
			if strings.Contains(text, name) {
				seen[name] = true
			}
		}
		match := nextCursorPattern.FindStringSubmatch(text)
		if match == nil {
			break
		}
		arguments["cursor"] = match[1]
	}

	if len(seen) != pagedIssueCount {
		t.Fatalf("saw %d issues, want %d", len(seen), pagedIssueCount)
	}
}

func TestListIssuesRejectsLimitAboveMaximum(t *testing.T) {
	harness := NewMCPHarness(t)

	text := harness.CallExpectError(t, "tack_list_issues", datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
		"project_reference":   harness.Project,
		"limit":               101,
	})

	if !strings.Contains(text, "limit must be between 1 and 100") {
		t.Fatalf("unexpected error text:\n%s", text)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `make test-integration`
Expected: FAIL. `tack_list_issues` rejects `cursor` and `limit` as unknown arguments.

- [ ] **Step 3: Add argument parsing**

`list_page_args.go`:

```go
package tools

import (
	"encoding/json"
	"fmt"

	"goodkind.io/tack/internal/domain"
)

const (
	defaultListLimit = 25
	maxListLimit     = 100
)

// pageSchemaFields are the paging inputs every list-style tool accepts.
func pageSchemaFields() []schemaField {
	return []schemaField{
		{Name: "limit", Type: schemaInteger, Desc: fmt.Sprintf("Rows per page, 1 to %d. Default %d.", maxListLimit, defaultListLimit)},
		{Name: "cursor", Type: schemaString, Desc: "Cursor printed at the end of the previous page. Omit for the first page."},
	}
}

// pageArgs reads limit and cursor. An absent limit returns defaultListLimit.
func pageArgs(args argMap) (int, string, error) {
	limit := defaultListLimit
	if raw, ok := args["limit"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &limit); err != nil {
			return 0, "", fmt.Errorf("limit must be an integer: %w", domain.ErrInvalidArgument)
		}
	}
	if limit < 1 || limit > maxListLimit {
		return 0, "", fmt.Errorf("limit must be between 1 and %d: %w", maxListLimit, domain.ErrInvalidArgument)
	}
	return limit, optionalString(args, "cursor"), nil
}
```

- [ ] **Step 4: Wire the schema and handler.** In `listTool`, append `pageSchemaFields()...` after the `filters` field. In `listHandler`, after building `q`:

```go
		limit, cursor, err := pageArgs(args)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		q.Limit = limit
		q.Cursor = cursor
		page, err := b.Reader.ListPage(ctx, q)
		if err != nil {
			return classifyError(ctx, err), nil
		}
```

Replace `renderList(rc, plural, views)` with `renderListPage(rc, plural, page)`.

- [ ] **Step 5: Render the cursor.** Add `NextCursor string` to `collectionTemplateData`. Add to `render_collection.go`:

```go
func renderListPage(rc *renderCtx, kind string, page node.NodePage) string {
	items := make([]markdownItem, 0, len(page.Views))
	for _, view := range page.Views {
		items = append(items, nodeListItem(rc, view))
	}
	data := collectionTemplateData{Heading: titleText(kind), Count: len(page.Views), Noun: kind, Items: items, NextCursor: page.NextCursor}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}
```

Replace `collection.md.tmpl` with:

```
#### {{.Heading}}

{{.Count}} {{.Noun}} shown.

{{- range .Items}}
- {{.Title}}
{{- range .Fields}}
  - {{.Label}}: {{.Value}}
{{- end}}
{{- end}}
{{- if .NextCursor}}

More results exist. Next cursor: `{{.NextCursor}}`
{{- end}}
```

- [ ] **Step 6: Run the tests to confirm they pass**

Run: `make test-integration && make test-unit`
Expected: PASS. Update any existing test that asserts `found.` to assert `shown.`.

- [ ] **Step 7: Commit**

```bash
git add internal/adapters/mcp/tools internal/test/integration
git commit -S -m "Add limit and cursor inputs to generated MCP list tools (TACK-508)"
```

### Task 4: Short confirmations from write tools

**Files:**
- Create: `internal/adapters/mcp/tools/render_write.go`
- Modify: `node_list_create.go:168`, `node_read_update_delete.go:130`, `reference_property_tool.go:160`
- Test: `internal/test/integration/mcp_write_confirmation_test.go`

**Interfaces:**
- Produces: `func renderWriteConfirmation(rc *renderCtx, verb string, view *node.NodeView, changedFields []string) string`

- [ ] **Step 1: Write the failing test**

```go
package integration

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

const maxWriteConfirmationBytes = 1024

func TestCreateIssueReturnsShortConfirmation(t *testing.T) {
	harness := NewMCPHarness(t)
	longDescription := strings.Repeat("A long description line.\n", 400)

	text := harness.Call(t, "tack_create_issue", datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
		"project_reference":   harness.Project,
		"name":                "confirmation check",
		"properties":          map[string]string{"description": longDescription},
	}).Text()

	if len(text) > maxWriteConfirmationBytes {
		t.Fatalf("create response is %d bytes, want at most %d", len(text), maxWriteConfirmationBytes)
	}
	if !strings.Contains(text, "confirmation check") || !strings.Contains(text, "description") {
		t.Fatalf("confirmation lacks name or changed field:\n%s", text)
	}
}
```

Add `TestUpdateIssueReturnsShortConfirmation` with the same shape through `tack_update_issue`.

- [ ] **Step 2: Run to confirm failure**

Run: `make test-integration`
Expected: FAIL. The response is over 10 KB because it repeats the description.

- [ ] **Step 3: Implement the renderer**

```go
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"goodkind.io/tack/internal/domain/node"
)

// renderWriteConfirmation reports a successful write without repeating
// property values. Callers read the full node with tack_get_*.
func renderWriteConfirmation(rc *renderCtx, verb string, view *node.NodeView, changedFields []string) string {
	reference := identifierFor(view, rc)
	if reference == "" {
		reference = view.ID.String()
	}
	fields := []markdownField{
		markdownCodeFieldValue("Reference", reference),
		markdownFieldValue("Name", view.Name),
		markdownCodeFieldValue("Type", view.NodeType),
	}
	if len(changedFields) > 0 {
		sorted := append([]string(nil), changedFields...)
		sort.Strings(sorted)
		fields = append(fields, markdownFieldValue("Changed", strings.Join(sorted, ", ")))
	}
	heading := fmt.Sprintf("%s %s `%s`", verb, strings.ToLower(view.NodeType), reference)
	return executeMarkdownTemplate("node.md.tmpl", nodeTemplateData{Heading: heading, Fields: fields})
}

func changedFieldNames(name *string, props map[string]json.RawMessage) []string {
	names := make([]string, 0, len(props)+1)
	if name != nil {
		names = append(names, "name")
	}
	for key := range props {
		names = append(names, key)
	}
	return names
}
```

- [ ] **Step 4: Use it.** In `createHandler` return `successText(renderWriteConfirmation(rc, "Created", result.View, changedFieldNames(&name, rawProps)), instr)`. In `updateHandler` return `renderWriteConfirmation(rc, "Updated", view, changedFieldNames(in.Name, rawProps))`. In the reference setter return `renderWriteConfirmation(rc, "Updated", view, []string{def.Name})`.

- [ ] **Step 5: Run the tests to confirm they pass**

Run: `make test-integration && make test-unit`
Expected: PASS. Update unit tests that asserted full-node output from create or update to assert the confirmation.

- [ ] **Step 6: Commit**

```bash
git add internal/adapters/mcp/tools internal/test/integration
git commit -S -m "Return short confirmations from MCP create, update, and set tools (TACK-508)"
```

### Task 5: Bounded describe, single name, byte cap, and guide text

**Files:**
- Modify: `internal/adapters/mcp/tools/workspace.go:72-93`, `render_collection.go:20-41,89-106`, `templates/workspace_describe.md.tmpl`
- Modify: `internal/adapters/mcp/tools/response.go:13-24`
- Modify: `internal/adapters/mcp/tools/templates/getting_started.md.tmpl`
- Test: `internal/test/integration/mcp_describe_test.go`

**Interfaces:**
- Produces: `const maxSuccessTextBytes = 32 * 1024`, `func capText(text string) string`, `nodeTypeSummary.ChildCount int`

- [ ] **Step 1: Write the failing tests**

```go
func TestDescribeWorkspaceCountsChildrenPerType(t *testing.T) {
	harness := NewMCPHarness(t)

	text := harness.Call(t, "tack_describe_workspace", datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
	}).Text()

	if strings.Contains(text, "#### Children") {
		t.Fatalf("describe still lists children:\n%s", text)
	}
	if !strings.Contains(text, "Direct children: 1") {
		t.Fatalf("describe lacks the project count:\n%s", text)
	}
}

func TestListCommentsPrintsNameOnce(t *testing.T) {
	harness := NewMCPHarness(t)
	issue := harness.CreateIssue(t, "commented issue")
	harness.Call(t, "tack_create_comment", datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
		"project_reference":   harness.Project,
		"issue_reference":     issue,
		"name":                "unique comment body",
	})

	text := harness.Call(t, "tack_list_comments", datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
		"project_reference":   harness.Project,
		"issue_reference":     issue,
	}).Text()

	if strings.Count(text, "unique comment body") != 1 {
		t.Fatalf("comment name printed %d times:\n%s", strings.Count(text, "unique comment body"), text)
	}
}
```

Add `CreateIssue(t, name) string` to the harness; it returns the printed reference from the confirmation.

- [ ] **Step 2: Run to confirm failure**

Run: `make test-integration`
Expected: FAIL on both tests.

- [ ] **Step 3: Count children per type.** In the describe handler, replace the org-wide `reader.List` with one `ListPage` per summarized type, `ByProperty` `parent_id` equal to the workspace ID and `Limit: maxListLimit`. Store `len(page.Views)` in `ChildCount` and print `Direct children: N` (or `N+` when `NextCursor` is set) in each type item. Remove the `Children` section from `workspace_describe.md.tmpl` and `renderWorkspaceDescribe`.

- [ ] **Step 4: Print the name once.** In `nodeListItem`, add the `Name` field only when the title came from a reference:

```go
	fields := []markdownField{markdownCodeFieldValue("Type", view.NodeType)}
	ident := identifierFor(view, rc)
	if ident == "" {
		ident = view.Name
	} else {
		fields = append([]markdownField{markdownFieldValue("Name", view.Name)}, fields...)
	}
```

- [ ] **Step 5: Add the byte cap**

```go
// maxSuccessTextBytes bounds every tool response. Paging keeps normal output
// far below it; the cap stops a single oversized property from flooding an
// agent's context.
const maxSuccessTextBytes = 32 * 1024

const truncationNotice = "\n\nOutput truncated at 32 KB. Narrow the request, lower `limit`, or use `cursor`."

func capText(text string) string {
	if len(text) <= maxSuccessTextBytes {
		return text
	}
	cut := strings.LastIndexByte(text[:maxSuccessTextBytes], '\n')
	if cut <= 0 {
		cut = maxSuccessTextBytes
	}
	return text[:cut] + truncationNotice
}
```

Call `capText(text)` in `successText` before building the result. Add `TestGetIssueCapsHugeDescription` that creates an issue with a 100 KB description, calls `tack_get_issue`, and asserts the text ends with the truncation notice and is at most `32*1024 + len(notice)` bytes.

- [ ] **Step 6: Update the guide.** In `getting_started.md.tmpl`:
  1. Under "Read workflows", state that list tools return 25 rows by default, accept `limit` up to 100, and print `Next cursor` when more rows exist; pass it back as `cursor`.
  2. Under "Response format", state that create, update, and set tools return a short confirmation, and `tack_get_<tool-token>` returns the full node.
  3. In "Orientation sequence" step 2, replace "the direct `children` of the workspace" with "a count of direct children per node type".
  Update the string assertions in `mcp_ergonomics_test.go:62-79,119-132` to match.

- [ ] **Step 7: Run all tests**

Run: `make build && make test-unit && make test-integration`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/adapters/mcp/tools internal/test/integration
git commit -S -m "Count workspace children, print comment names once, and cap MCP response size (TACK-508)"
```

### Task 6: Datagen coverage and release (TACK-508)

- [ ] **Step 1: Add datagen paging coverage.** In `internal/datagen/generate_search_checks.go` add `func (g *Generator) verifyListPaging(ctx context.Context, workspace WorkspaceIdentity, projectReference string) error`. It calls `tack_list_issues` with `limit: 5`, requires `Next cursor` in the text when the project has more than 5 issues, calls again with that cursor, and returns an error when the second page repeats a reference from the first. Call it from `Generator.Run` after issues are generated.
- [ ] **Step 2: Run datagen locally**

Run: `docker compose -f docker-compose.test.yml run --rm app ops qa datagen seed --scale small --commit` with `TACK_DATAGEN_ALLOW_TARGET=local`.
Expected: exit 0.
- [ ] **Step 3: Commit, open a pull request, and merge after CI passes.**
- [ ] **Step 4: Deploy to QA.** Run the QA datagen commands from AGENTS.md. On QA, call `tack_list_issues` for `TACK` through an MCP client and confirm one page of 25 rows and a cursor.
- [ ] **Step 5: Deploy to production with `./server ops deploy`,** repeat the Step 4 check, and set TACK-508 to `TACK::Done`.

---

## Phase B: Make keyword search work (TACK-509)

### Task 7: Report an unavailable search backend

**Files:**
- Modify: `internal/domain/search/searcher.go`
- Modify: `internal/adapters/search/search.go:12-20`
- Modify: `internal/adapters/mcp/tools/search.go:59-62`
- Modify: `internal/adapters/mcp/tools/response.go` `classifyError`
- Test: `internal/test/integration/mcp_search_test.go`

**Interfaces:**
- Produces: `var ErrUnavailable = errors.New("search backend unavailable")` in `domainsearch`.

- [ ] **Step 1: Write the failing test.** The test stack has no Meilisearch until Task 9, so `BuildGraph` returns `Noop`.

```go
func TestSearchReportsUnavailableBackend(t *testing.T) {
	t.Setenv("MEILI_URL", "http://[::1]:1")
	harness := NewMCPHarness(t)

	text := harness.CallExpectError(t, "tack_search", datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
		"query":               "anything",
	})

	if !strings.Contains(text, "Search is unavailable") {
		t.Fatalf("unexpected error text:\n%s", text)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `make test-integration`
Expected: FAIL. The call succeeds with `0 results`.

- [ ] **Step 3: Implement.** Add `ErrUnavailable` to `searcher.go`. Change `Noop.Search` to `return nil, nil, domainsearch.ErrUnavailable` and fix its doc comment to say search reports the backend as unavailable. Add a case to `classifyError`:

```go
	case errors.Is(err, domainsearch.ErrUnavailable):
		return recoverableError("Search is unavailable because the search backend is not connected. Use tack_list_<plural> with filters, or yield to the user")
```

- [ ] **Step 4: Run to confirm pass,** then commit:

```bash
git commit -S -m "Report an unavailable search backend from tack_search instead of zero results (TACK-509)"
```

### Task 8: Metadata-driven search documents and searchable attributes

**Files:**
- Create: `internal/service/search_doc.go`
- Modify: `internal/service/node.go:345-359` (delete `searchDocFromView`), its callers in `node.go:151` and `node_create_effects.go:13-22`
- Modify: `internal/adapters/search/meilisearch.go:32-53`
- Modify: `internal/runtime/graph.go:132-144`
- Test: `internal/service/search_doc_test.go`

**Interfaces:**
- Produces: `func SearchDocFromView(view *node.NodeView, defs []*node.PropertyDef) *domainsearch.NodeDoc` (exported for Task 10), `var searchableAttributes = []string{"name", "props"}`.

- [ ] **Step 1: Write the failing test.** This is a pure function over real domain values, so no fakes are involved.

```go
func TestSearchDocKeepsTextPropertiesOnly(t *testing.T) {
	view := &node.NodeView{
		ID: uuid.New(), OrgID: uuid.New(), NodeType: "issue", Name: "Backup drill",
		Props: map[string]json.RawMessage{
			"description": json.RawMessage(`"Restore the ledger"`),
			"state_id":    json.RawMessage(`"0190c0de-0000-7000-8000-000000000000"`),
		},
	}
	defs := []*node.PropertyDef{
		{Name: "description", Type: node.PropertyTypeText},
		{Name: "state_id", Type: node.PropertyTypeUUID},
	}

	doc := SearchDocFromView(view, defs)

	if _, ok := doc.Props["state_id"]; ok {
		t.Fatal("uuid property was indexed")
	}
	if string(doc.Props["description"]) != `"Restore the ledger"` {
		t.Fatalf("description = %s", doc.Props["description"])
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `make test-unit` (add `./internal/service/...` to the unit target if it only covers `./internal/ops/...`).
Expected: FAIL with `undefined: SearchDocFromView`.

- [ ] **Step 3: Implement**

```go
package service

// searchablePropertyTypes are the property types whose values are words a
// person would type into search. UUID, number, date, and checkbox values are
// excluded because they never match a keyword query.
var searchablePropertyTypes = map[node.PropertyType]bool{
	node.PropertyTypeText:        true,
	node.PropertyTypeSelect:      true,
	node.PropertyTypeMultiSelect: true,
	node.PropertyTypeURL:         true,
}

// SearchDocFromView builds the search document for a view, keeping only
// properties whose definition has a searchable type.
func SearchDocFromView(view *node.NodeView, defs []*node.PropertyDef) *domainsearch.NodeDoc {
	doc := &domainsearch.NodeDoc{
		ID:       view.ID.String(),
		OrgID:    view.OrgID.String(),
		NodeType: view.NodeType,
		Name:     view.Name,
	}
	searchable := make(map[string]bool, len(defs))
	for _, def := range defs {
		if searchablePropertyTypes[def.Type] {
			searchable[def.Name] = true
		}
	}
	for key, raw := range view.Props {
		if !searchable[key] {
			continue
		}
		if doc.Props == nil {
			doc.Props = make(map[string]json.RawMessage)
		}
		doc.Props[key] = raw
	}
	return doc
}
```

In `NodeService.Update` and `indexCreateSearch`, load `s.propertyDefs.List(ctx, view.OrgID)` and call `SearchDocFromView`. Log and skip indexing on a property def read error, matching the existing warn-and-continue behavior.

- [ ] **Step 4: Set searchable attributes.** Rename `EnsureIndex` parameters to `EnsureIndex(collection string, filterableAttributes, searchableAttributes []string) error`, and after the filterable update add:

```go
	searchable := append([]string(nil), searchableAttributes...)
	if _, err := c.meili.Index(collection).UpdateSearchableAttributes(&searchable); err != nil {
		return fmt.Errorf("set searchable attributes on %s: %w", collection, err)
	}
```

In `buildSearcher` pass `[]string{"name", "props"}`. The order ranks a name match above a property match.

- [ ] **Step 5: Run `make build && make test-unit`,** then commit:

```bash
git commit -S -m "Index only searchable property types and rank name above properties (TACK-509)"
```

### Task 9: Meilisearch in the test stack and an end-to-end search test

**Files:**
- Modify: `docker-compose.test.yml`
- Test: `internal/test/integration/mcp_search_test.go`

- [ ] **Step 1: Add the service.** Copy the `meilisearch` service block from `docker-compose.yml:247-280` into `docker-compose.test.yml`, drop production volumes, set `MEILI_ENV: development` and a test master key, and set `MEILI_URL: http://meilisearch:7700` and `MEILI_MASTER_KEY` on the runner. Add `meilisearch` to the runner's `depends_on` with `condition: service_healthy`. Keep `TestSearchReportsUnavailableBackend` working through its `t.Setenv` override.

- [ ] **Step 2: Write the test**

```go
func TestSearchFindsIssueByTitleWord(t *testing.T) {
	harness := NewMCPHarness(t)
	reference := harness.CreateIssue(t, "Temporal WAL archive rollout")

	found := waitFor(t, 10*time.Second, func() bool {
		text := harness.Call(t, "tack_search", datagen.ToolArguments{
			"workspace_reference": harness.Workspace,
			"query":               "archive",
		}).Text()
		return strings.Contains(text, reference)
	})

	if !found {
		t.Fatalf("search for %q never returned %s", "archive", reference)
	}
}
```

Meilisearch indexes asynchronously, so the test polls. Put `waitFor(t, timeout, check) bool` in `internal/test/integration/wait.go`; it retries `check` every 200 ms until it returns true or the timeout passes.

- [ ] **Step 3: Run**

Run: `make test-integration`
Expected: PASS for both search tests.

- [ ] **Step 4: Commit**

```bash
git commit -S -m "Add Meilisearch to the test stack and test tack_search end to end (TACK-509)"
```

### Task 10: `ops search-reindex` backfill

**Files:**
- Modify: `internal/domain/search/searcher.go`, `internal/adapters/search/meilisearch.go`, `internal/adapters/search/search.go`
- Create: `internal/ops/search_reindex.go`
- Modify: `internal/audit` verb list (add `VerbOpsSearchReindex` next to `VerbOpsReindex`)
- Test: `internal/test/integration/search_reindex_test.go`

**Interfaces:**
- Consumes: `NodeReader.ListPage`, `service.SearchDocFromView`.
- Produces: `Searcher.IndexBatch(ctx context.Context, collection string, docs []*NodeDoc) error`; ops operation `search-reindex`, run as `./server ops batch search-reindex --execute`.

- [ ] **Step 1: Write the failing test.** Create an issue with `noopSearcher`, so Meilisearch never sees it, then run the operation and search.

```go
func TestSearchReindexIndexesExistingNodes(t *testing.T) {
	env := SetupTestEnv(t)
	projectID := createTestProject(t, env)
	issueID := createTestIssue(t, env, projectID, "Reindexed quarry ticket")

	if err := ops.Run(env.Ctx, env.Ops.Cfg, "search-reindex"); err != nil {
		t.Fatalf("search-reindex: %v", err)
	}

	client := searchadapter.New(env.Ops.Cfg.MeiliURL, env.Ops.Cfg.MeiliMasterKey)
	found := waitFor(t, 10*time.Second, func() bool {
		docs, _, err := client.Search(env.Ctx, "nodes", "quarry", map[string]string{"org_id": env.OrgID.String()})
		if err != nil {
			return false
		}
		for _, doc := range docs {
			if doc.ID == issueID.String() {
				return true
			}
		}
		return false
	})
	if !found {
		t.Fatalf("issue %s not searchable after reindex", issueID)
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `make test-integration`
Expected: FAIL with an unknown operation error.

- [ ] **Step 3: Add `IndexBatch`.** Add it to the `Searcher` interface. `Noop.IndexBatch` returns `ErrUnavailable`. The Meilisearch client:

```go
// IndexBatch adds or replaces docs in one task and waits for the task, so a
// backfill reports rejected documents instead of dropping them silently.
func (c *Client) IndexBatch(ctx context.Context, collection string, docs []*domainsearch.NodeDoc) error {
	if len(docs) == 0 {
		return nil
	}
	primaryKey := "id"
	task, err := c.meili.Index(collection).AddDocuments(docs, &meilisearch.DocumentOptions{PrimaryKey: &primaryKey})
	if err != nil {
		return fmt.Errorf("index batch of %d in %s: %w", len(docs), collection, err)
	}
	finished, err := c.meili.WaitForTask(task.TaskUID, indexTaskPollInterval)
	if err != nil {
		return fmt.Errorf("wait for index task %d: %w", task.TaskUID, err)
	}
	if finished.Status != meilisearch.TaskStatusSucceeded {
		return fmt.Errorf("index task %d ended %s: %s", task.TaskUID, finished.Status, finished.Error.Message)
	}
	telemetry.L(ctx).Info("search.batch_indexed", slog.String("collection", collection), slog.Int("count", len(docs)))
	return nil
}
```

Check `WaitForTask` and `TaskStatusSucceeded` names against `/Users/agoodkind/go/pkg/mod/github.com/meilisearch/meilisearch-go@v0.36.3` before writing; set `indexTaskPollInterval = 250 * time.Millisecond`. Add `IndexBatch` to `internal/test/integration/noop_searcher.go`.

- [ ] **Step 4: Implement the operation** in `internal/ops/search_reindex.go`, following `reindex.go`:

```go
const searchReindexPageSize = 500

func init() {
	Register(Operation{
		Name:        "search-reindex",
		Audit:       audit.Spec{Verb: string(audit.VerbOpsSearchReindex), Mutates: true},
		Description: "Rebuild the Meilisearch nodes index from FoundationDB views",
		Run:         runSearchReindex,
	})
}

func runSearchReindex(ctx context.Context, env *Env) error {
	searcher := searchadapter.New(env.Cfg.MeiliURL, env.Cfg.MeiliMasterKey)
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return fmt.Errorf("search reindex: list orgs: %w", err)
	}
	for _, orgID := range orgIDs {
		if err := reindexOrgSearch(ctx, env, searcher, orgID); err != nil {
			return fmt.Errorf("search reindex org %s: %w", orgID, err)
		}
	}
	return nil
}

func reindexOrgSearch(ctx context.Context, env *Env, searcher *searchadapter.Client, orgID uuid.UUID) error {
	defs, err := env.Stores.PropertyDefs.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list property defs: %w", err)
	}
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list node types: %w", err)
	}
	for _, nodeType := range nodeTypes {
		indexedCount, err := reindexTypeSearch(ctx, env, searcher, orgID, nodeType.TypeKey, defs)
		if err != nil {
			return fmt.Errorf("type %s: %w", nodeType.TypeKey, err)
		}
		env.Log.InfoContext(ctx, "search_reindex.type_done",
			slog.String("org_id", orgID.String()),
			slog.String("node_type", nodeType.TypeKey),
			slog.Int("indexed", indexedCount))
	}
	return nil
}

func reindexTypeSearch(ctx context.Context, env *Env, searcher *searchadapter.Client, orgID uuid.UUID, typeKey string, defs []*node.PropertyDef) (int, error) {
	query := node.NodeListQuery{OrgID: orgID, NodeType: typeKey, Limit: searchReindexPageSize}
	indexedCount := 0
	for {
		page, err := env.Stores.Views.ListPage(ctx, query)
		if err != nil {
			return indexedCount, err
		}
		docs := make([]*domainsearch.NodeDoc, 0, len(page.Views))
		for _, view := range page.Views {
			docs = append(docs, service.SearchDocFromView(view, defs))
		}
		if err := searcher.IndexBatch(ctx, "nodes", docs); err != nil {
			return indexedCount, err
		}
		indexedCount += len(docs)
		if page.NextCursor == "" {
			return indexedCount, nil
		}
		query.Cursor = page.NextCursor
	}
}
```

Unlike `reindex.go`, this returns the first error instead of logging and continuing, so an operator sees a partial backfill as a failure. Split into `search_reindex.go` and `search_reindex_type.go` if the file passes 200 lines. Confirm the operation appears in `./server ops batch --help` and that running it without `--execute` prints a dry run and writes nothing.

- [ ] **Step 5: Run to confirm pass**

Run: `make build && make test-unit && make test-integration`
Expected: PASS, including `cli_execute_gate_test.go`.

- [ ] **Step 6: Commit**

```bash
git commit -S -m "Add ops batch search-reindex to backfill Meilisearch from FoundationDB (TACK-509)"
```

### Task 11: Datagen search check and release (TACK-509)

- [ ] **Step 1: Add the check.** In `generate_search_checks.go` add `func (g *Generator) verifySearchFindsIssue(ctx context.Context, token string, workspace WorkspaceIdentity, reference, titleWord string) error`. It calls `tack_search` with `query: titleWord` up to 20 times, 500 ms apart, and returns an error naming the reference when the result text never contains it. Call it once per project after issues are generated, using the first word of the first issue name.
- [ ] **Step 2: Run datagen locally** as in Task 6 Step 2. Expected: exit 0.
- [ ] **Step 3: Commit, open a pull request, merge after CI passes.**
- [ ] **Step 4: Find the production cause.** Read the app logs on production for `meilisearch.connected` or `meilisearch.setup_failed` since the last restart, and record which one appears in the TACK-509 description.
- [ ] **Step 5: Deploy to QA.** Run `docker compose run --rm app ops batch search-reindex --execute`. Confirm `tack_search` for `backup` returns backup tickets.
- [ ] **Step 6: Deploy to production,** run the same reindex command, repeat the Step 5 check, and set TACK-509 to `TACK::Done`.

---

## Phase C: Per-org synonyms (TACK-510)

### Task 12: `synonym_set` node type

**Files:**
- Modify: `internal/domain/node/types.go` (add `FeatureHasSynonyms`)
- Modify: `internal/service/seed.go:14-29,75+,306-318`
- Test: `internal/test/integration/mcp_synonym_set_test.go`

**Interfaces:**
- Produces: node type key `synonym_set` (tools `tack_create_synonym_set` and siblings, list token `synonym_sets`), feature `has_synonyms`, property `terms` of type `text` that applies to `has_synonyms`. `terms` holds comma-separated equivalent terms, for example `db, database`.

- [ ] **Step 1: Write the failing test**

```go
func TestCreateSynonymSetThroughMCP(t *testing.T) {
	harness := NewMCPHarness(t)

	text := harness.Call(t, "tack_create_synonym_set", datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
		"name":                "database terms",
		"properties":          map[string]string{"terms": "db, database"},
	}).Text()

	if !strings.Contains(text, "database terms") {
		t.Fatalf("unexpected create output:\n%s", text)
	}
}
```

- [ ] **Step 2: Run to confirm failure.** Expected: FAIL with an unknown tool.

- [ ] **Step 3: Seed it.** Add `FeatureHasSynonyms Feature = "has_synonyms"` beside the other feature constants. In `seed.go` add `nodeTypeSynonymSet = "synonym_set"`, add `nodeTypeSynonymSet` to the workspace `canContain`, and add the spec row:

```go
		{"synonym_set", "synonym_sets", "Synonym Set", nodeTypeSynonymSet, node.Features{node.FeatureHasSynonyms}, scopedName, nil, []string{nodeTypeWorkspace}, nil},
```

Add the property def:

```go
		{
			ID:                node.SystemPropID(orgID, "terms"),
			OrgID:             orgID,
			Name:              "terms",
			Type:              node.PropertyTypeText,
			AppliesToFeatures: []string{string(node.FeatureHasSynonyms)},
			Indexed:           false,
		},
```

- [ ] **Step 4: Re-seed existing orgs.** `Seeder.SeedOrg` is idempotent. Find the command that calls `SeedOrg` for existing orgs with `search_code` on this repository ("call SeedOrg for existing orgs"). If none exists, add `ops batch seed-orgs` in `internal/ops/seed_orgs.go` following Task 10 Step 4: it lists orgs with `listOrgIDs` and calls `service.NewSeeder(env.Stores.PropertyDefs, env.Stores.NodeTypes).SeedOrg` for each.

- [ ] **Step 5: Run to confirm pass,** then commit:

```bash
git commit -S -m "Add synonym_set node type with a terms property to the org seed (TACK-510)"
```

### Task 13: Expand search queries with synonyms

**Files:**
- Create: `internal/adapters/mcp/tools/search_synonyms.go`
- Create: `internal/adapters/search/meilisearch_multi.go`
- Modify: `internal/domain/search/searcher.go`, `internal/adapters/search/search.go`, `internal/test/integration/noop_searcher.go`
- Modify: `internal/adapters/mcp/tools/search.go:55-65`
- Test: `internal/test/integration/mcp_search_synonyms_test.go`

**Interfaces:**
- Consumes: `NodeReader.ListPage`, `FeatureHasSynonyms`.
- Produces: `Searcher.SearchVariants(ctx context.Context, collection string, queries []string, filters map[string]string, limit int) ([]NodeDoc, error)`, `func expandQuery(query string, synonymSets [][]string) []string`, `const maxQueryVariants = 8`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSearchUsesOrgSynonyms(t *testing.T) {
	harness := NewMCPHarness(t)
	reference := harness.CreateIssue(t, "Database failover drill")
	harness.Call(t, "tack_create_synonym_set", datagen.ToolArguments{
		"workspace_reference": harness.Workspace,
		"name":                "database terms",
		"properties":          map[string]string{"terms": "db, database"},
	})

	found := waitFor(t, 10*time.Second, func() bool {
		text := harness.Call(t, "tack_search", datagen.ToolArguments{
			"workspace_reference": harness.Workspace,
			"query":               "db",
		}).Text()
		return strings.Contains(text, reference)
	})

	if !found {
		t.Fatalf("search for db never returned %s", reference)
	}
}

func TestSynonymsDoNotCrossOrgs(t *testing.T) {
	owner := NewMCPHarness(t)
	other := NewMCPHarness(t)
	reference := other.CreateIssue(t, "Database failover drill")
	owner.Call(t, "tack_create_synonym_set", datagen.ToolArguments{
		"workspace_reference": owner.Workspace,
		"name":                "database terms",
		"properties":          map[string]string{"terms": "db, database"},
	})
	waitFor(t, 5*time.Second, func() bool {
		text := other.Call(t, "tack_search", datagen.ToolArguments{
			"workspace_reference": other.Workspace,
			"query":               "database",
		}).Text()
		return strings.Contains(text, reference)
	})

	text := other.Call(t, "tack_search", datagen.ToolArguments{
		"workspace_reference": other.Workspace,
		"query":               "db",
	}).Text()

	if strings.Contains(text, reference) {
		t.Fatalf("another org's synonym set changed this org's results:\n%s", text)
	}
}
```

`NewMCPHarness` must create a separate org per call for the second test. Confirm that in Task 1 and adjust it if `BootstrapIdentities` reuses one org.

- [ ] **Step 2: Run to confirm failure.** Expected: `TestSearchUsesOrgSynonyms` FAILS.

- [ ] **Step 3: Add query expansion**

```go
package tools

// maxQueryVariants bounds the federated search fan-out so a large synonym
// set cannot multiply query cost.
const maxQueryVariants = 8

// expandQuery returns the original query plus one variant per synonym
// substitution of a whole query word, up to maxQueryVariants.
func expandQuery(query string, synonymSets [][]string) []string {
	variants := []string{query}
	seen := map[string]bool{strings.ToLower(query): true}
	words := strings.Fields(query)
	for wordIndex, word := range words {
		for _, terms := range synonymSets {
			if !containsFold(terms, word) {
				continue
			}
			for _, term := range terms {
				replaced := append([]string(nil), words...)
				replaced[wordIndex] = term
				variant := strings.Join(replaced, " ")
				key := strings.ToLower(variant)
				if seen[key] {
					continue
				}
				seen[key] = true
				variants = append(variants, variant)
				if len(variants) == maxQueryVariants {
					return variants
				}
			}
		}
	}
	return variants
}

func containsFold(terms []string, word string) bool {
	for _, term := range terms {
		if strings.EqualFold(term, word) {
			return true
		}
	}
	return false
}
```

Add `loadSynonymSets(ctx, resolver, workspace) ([][]string, error)`. It finds node types whose `Features` include `FeatureHasSynonyms` in `resolver.typeIndex`, and for each calls `ListPage` with `ByProperty` `parent_id` equal to the workspace ID and `Limit: maxListLimit`. It finds the property to read by listing the org's property defs and taking the one whose `AppliesToFeatures` includes `has_synonyms`. It splits the value on commas and trims spaces. Multi-word terms are skipped because `expandQuery` substitutes single words.

- [ ] **Step 4: Add federated search.** In `meilisearch_multi.go`:

```go
// SearchVariants runs one federated multi-search with one query per variant
// and returns hits merged and ranked by Meilisearch.
func (c *Client) SearchVariants(ctx context.Context, collection string, queries []string, filters map[string]string, limit int) ([]domainsearch.NodeDoc, error) {
	filter := buildFilter(filters)
	requests := make([]*meilisearch.SearchRequest, 0, len(queries))
	for _, query := range queries {
		requests = append(requests, &meilisearch.SearchRequest{IndexUID: collection, Query: query, Filter: filter})
	}
	response, err := c.meili.MultiSearch(&meilisearch.MultiSearchRequest{
		Federation: &meilisearch.MultiSearchFederation{Limit: int64(limit)},
		Queries:    requests,
	})
	if err != nil {
		return nil, fmt.Errorf("federated search of %d variants in %s: %w", len(queries), collection, err)
	}
	return decodeHits(response.Hits), nil
}
```

Move the filter-string loop and hit decoding out of `Search` into `buildFilter` and `decodeHits` in the same file, and have `Search` call them. `Noop.SearchVariants` returns `ErrUnavailable`.

- [ ] **Step 5: Use it in `tack_search`.** Replace the `searcher.Search` call:

```go
			synonymSets, err := loadSynonymSets(ctx, resolver, ws)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			docs, err := searcher.SearchVariants(ctx, "nodes", expandQuery(query, synonymSets), filters, maxListLimit)
```

The Task 3 `limit` argument can replace `maxListLimit` here if Task 3 added paging to `tack_search`.

- [ ] **Step 6: Run to confirm pass**

Run: `make build && make test-unit && make test-integration`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git commit -S -m "Expand tack_search queries with org synonym sets through federated search (TACK-510)"
```

### Task 14: Datagen, guide, and release (TACK-510)

- [ ] **Step 1: Add datagen coverage.** In `generate_search_checks.go` add `verifySynonymSearch`: create a synonym set `db, database` in each workspace, create an issue named `Database datagen check`, and require that `tack_search` for `db` returns it within 10 seconds.
- [ ] **Step 2: Update the guide.** Add a sentence to the "Search" section of `getting_started.md.tmpl`: `tack_search` also matches words listed together in the workspace's synonym sets, which are managed with `tack_create_synonym_set`.
- [ ] **Step 3: Run datagen locally, commit, open a pull request, merge after CI passes.**
- [ ] **Step 4: Deploy to QA.** Re-seed orgs (Task 12 Step 4), create a synonym set, and confirm the synonym search works.
- [ ] **Step 5: Deploy to production,** re-seed orgs, repeat the check, and set TACK-510 to `TACK::Done`.
