# Bound MCP output and implement keyword search

Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`
to implement these tasks and track checkboxes. Add cursor paging to node reads,
short write confirmations, explicit search failures, metadata-defined text
indexing, and organization-owned synonym nodes.

Use Go, FoundationDB Go bindings, `mark3labs/mcp-go`, `meilisearch-go` v0.36.3,
Meilisearch v1.12, and the Docker Compose test stack.

The phases have these ticket dependencies:

| Phase | Ticket | Priority | Dependency |
|---|---|---|---|
| A: Bound output | TACK-508 | high | none |
| B: Keyword search | TACK-509 | urgent | Task 1 harness |
| C: Organization synonyms | TACK-510 | medium | TACK-509 |

Use `tack_set_issue_state` to set `TACK::In Progress` at the first task and
`TACK::Done` only after production acceptance.

## Verified defects and implementation constraints

The source review found these defects:

- `listHandler` omits limit/cursor (`node_list_create.go:31-56`), and
  `ViewStore.List` reads full ranges (`view.go:152-214`). Schemas lack paging inputs.
- Writes return `renderNode`, including complete descriptions
  (`node_list_create.go:168`, `node_read_update_delete.go:130`,
  `reference_property_tool.go:160`, `render_node.go:40-56`).
- Workspace describe scans every organization child (`workspace.go:86-91`).
- Comment/activity output repeats names when a type lacks a reference
  (`render_collection.go:89-97`).
- On 2026-09-18, production search returned 0 matches for `backup` and
  `Temporal-DB WAL archive`. Exact-reference lookup in FDB returned 1 for
  `TACK-275` (`search.go:51`).
- `buildSearcher` substitutes `searchadapter.Noop{}` after setup failure;
  `Noop.Search` returns an empty result without error (`graph.go:132-144`,
  `search.go:15-20`). The system does not provide an FDB-to-Meilisearch backfill.

Keep implementation files at most 200 lines. Existing `view.go` already has
300; put new FDB behavior in separate files. Interpret NodeType/PropertyDef
metadata rather than matching concrete type/property literals outside seed.go.
Use real dependencies through `make test-unit` and `make test-integration` in
`docker-compose.test.yml`. Do not add mocks or fakes; extend existing tool
fakes only with compiler-required methods. Add QA datagen for each public change.
Register ops through clispec without a separate execute flag. Log through
`telemetry.L(ctx)` with `noun.verb` and slog attributes. Include identifiers and
wrapped errors, such as `fmt.Errorf("list page %s: %w", nodeType, err)`.
Build with `make build`, not `go build`. Sign imperative commits with the ticket
in parentheses and `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.
Validate QA before production and deploy main only after fetch confirms
`HEAD == origin/main`.

## Task 1: Add a real MCP harness

Create `internal/test/integration/mcp_harness.go` and its test. Reuse
`datagen.Driver` JSON-RPC tools/call requests through `graph.MCPHandler` with
real tokens. The existing integration suite does not invoke MCP tools.
Read `internal/datagen/session.go` first and copy current RunSeed commit-mode
signatures for config loading, `runtime.BuildGraph`, `BootstrapIdentities`, and
`NewDriver`. Expose `func NewMCPHarness(t *testing.T) *MCPHarness`,
`func (h *MCPHarness) Call(t *testing.T, toolName string, args datagen.ToolArguments) datagen.Result`,
and `func (h *MCPHarness) CallExpectError(t *testing.T, toolName string, args datagen.ToolArguments) string`,
with `Workspace string` and `Project string` fields.

- [ ] Add the failing test and run `make test-integration`, requiring
  `undefined: NewMCPHarness`:

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

- [ ] Implement the harness from current production signatures:

```go
package integration

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
)

// MCPHarness invokes production HTTP tools with real bearer authentication,
// membership checks, audit recording, and rendering.
type MCPHarness struct {
	ctx       context.Context
	driver    *datagen.Driver
	token     string
	Workspace string
	Project   string
}

// NewMCPHarness creates a test workspace and project against the real stack.
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
		token:  identities.Token, // Use the verified bootstrap token field.
	}
	harness.createWorkspaceAndProject(t)
	return harness
}
```

Bootstrap one user, org, workspace, and project using MCP creation and retain
the token. Extract `requireIntegration` from setup.go's `TACK_INTEGRATION` skip
if necessary. Create project identifiers from `t.Name()` and save rendered
references. Implement `Call(t, toolName, args) datagen.Result` to fail on driver
errors and `CallExpectError(t, toolName, args) string` to fail on success and
return error text. Use `datagen.ToolArguments` for arguments.

- [ ] Require the harness test to pass, then commit:

```bash
git add internal/test/integration/mcp_harness.go internal/test/integration/mcp_harness_test.go internal/test/integration/setup.go
git commit -S -m "Add MCP integration harness that calls tools through the HTTP handler (TACK-508)"
```

## Task 2: Page FDB reads

Create `internal/domain/node/cursor.go`, `internal/adapters/foundationdb/view_page.go`,
`view_page_batch.go`, and `internal/test/integration/view_page_test.go`.
Extend reader.go:64-80 and each compiler-reported NodeReader implementation,
including resolverReader, fakeReader, and repairReader in their existing tests.

- [ ] Add this regression and require missing ListPage compilation failure:

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

Add createTestProject/createTestIssue in setup_nodes.go using
`env.NodeSvc.Create(env.Ctx, service.CreateInput{...})`, matching workspace
creation in audit_intent_test.go:26-28. Fixture type literals mirror the seed.

- [ ] Implement opaque cursor encoding and decoding:

```go
package node

import (
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
)

// EncodeCursor returns an opaque continuation after lastID.
func EncodeCursor(lastID uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(lastID[:])
}

// DecodeCursor returns the last node ID from an opaque cursor.
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

Add after NodeStreamResult and in NodeReader respectively:

```go
// NodePage contains one ListPage result; the final page has an empty NextCursor.
type NodePage struct {
	Views      []*NodeView
	NextCursor string
}
```

```go
	// ListPage returns at most q.Limit views after q.Cursor in ID order.
	// It supports ByProperty and full type scans.
	ListPage(ctx context.Context, q NodeListQuery) (NodePage, error)
```

- [ ] Implement production paging, requiring positive limits and rejecting
  relation scans with `domain.ErrInvalidArgument`:

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

// pageBatchSize bounds FDB reads below transaction time and size limits.
const pageBatchSize = 256

type pageEntry struct {
	id   uuid.UUID
	view *node.NodeView
}

// ListPage reads matching views plus one to detect another page.
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

Use a separate view_page_batch.go for the batch implementation. The 256-key
read bound stays below FDB's 5-second and 10 MB transaction limits:

```go
package foundationdb

// readPageBatch returns decoded entries and the last key from one bounded read.
// Property scans resolve each indexed view; full scans decode values directly.
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

A missing indexed view produces `view == nil`. Skip it while advancing the key.

- [ ] Extend existing fakes with `node.NodePage{Views: <its List result truncated to q.Limit>}`.
  Run `make build` and `make test-integration`; require compilation and the
  150-issue, six-page regression to pass. Commit:

```bash
git add internal/domain/node internal/adapters/foundationdb/view_page.go internal/adapters/foundationdb/view_page_batch.go internal/test/integration internal/adapters/mcp/tools internal/ops
git commit -S -m "Add cursor-paged NodeReader.ListPage for property and type scans (TACK-508)"
```

## Task 3: Expose list limit and cursor

Create tools/list_page_args.go. Update node_tool_schema.go:11-20,
node_list_create.go:31-65, render_collection.go:11-18, collection_template.go,
and templates/collection.md.tmpl. Add integration/mcp_list_page_test.go.

- [ ] Add these tests and require `make test-integration` to fail because
  schemas do not accept cursor and limit:

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

- [ ] Implement argument parsing and schema fields:

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

func pageSchemaFields() []schemaField {
	return []schemaField{
		{Name: "limit", Type: schemaInteger, Desc: fmt.Sprintf("Return 1 to %d rows per page. The default is %d.", maxListLimit, defaultListLimit)},
		{Name: "cursor", Type: schemaString, Desc: "Use the cursor from the previous page. Omit for the first page."},
	}
}

// pageArgs reads limit and cursor, defaulting an omitted limit to defaultListLimit.
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

- [ ] Append `pageSchemaFields()...` after listTool's filters. After constructing
  `q` in listHandler, use:

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
Add `NextCursor string` to collectionTemplateData and render:

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

Replace the collection template with:

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

- [ ] Run `make test-integration && make test-unit` and require success.
  Update old `found.` assertions to `shown.`. Commit:

```bash
git add internal/adapters/mcp/tools internal/test/integration
git commit -S -m "Add limit and cursor inputs to generated MCP list tools (TACK-508)"
```

## Task 4: Return short write confirmations

Create tools/render_write.go and integration/mcp_write_confirmation_test.go.
Update create at node_list_create.go:168, update at node_read_update_delete.go:130,
and the reference setter at reference_property_tool.go:160.

- [ ] Add the regression and a matching TestUpdateIssueReturnsShortConfirmation
  through tack_update_issue. Require integration failure from the existing
  description response exceeding 10 KB:

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

- [ ] Implement the renderer:

```go
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"goodkind.io/tack/internal/domain/node"
)

// renderWriteConfirmation omits property values; tack_get_* returns the full node.
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

Use `successText(renderWriteConfirmation(rc, "Created", result.View,
changedFieldNames(&name, rawProps)), instr)` for create,
`renderWriteConfirmation(rc, "Updated", view, changedFieldNames(in.Name, rawProps))`
for update, and `renderWriteConfirmation(rc, "Updated", view, []string{def.Name})`
for the reference setter.

- [ ] Require `make test-integration && make test-unit` success. Update tests
  that require full-node write output to require confirmation. Commit:

```bash
git add internal/adapters/mcp/tools internal/test/integration
git commit -S -m "Return short confirmations from MCP create, update, and set tools (TACK-508)"
```

## Task 5: Bound describe and response text

Update workspace.go:72-93, render_collection.go:20-41,89-106,
workspace_describe.md.tmpl, response.go:13-24, and getting_started.md.tmpl.
Add integration/mcp_describe_test.go.

- [ ] Add these tests and require integration failure:

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

Add harness `CreateIssue(t, name) string`, returning its confirmation reference.
For each summarized type, replace org-wide List with ListPage using `ByProperty`
`parent_id` equal to workspace ID and `Limit: maxListLimit`. Store
`len(page.Views)` in `nodeTypeSummary.ChildCount int`. Print `Direct children: N`
or `N+` with a nonempty NextCursor. Remove the Children template section and
renderWorkspaceDescribe code. Add the Name field only when the title uses a reference:

```go
	fields := []markdownField{markdownCodeFieldValue("Type", view.NodeType)}
	ident := identifierFor(view, rc)
	if ident == "" {
		ident = view.Name
	} else {
		fields = append([]markdownField{markdownFieldValue("Name", view.Name)}, fields...)
	}
```

- [ ] Add the response byte cap:

```go
// maxSuccessTextBytes limits tool text, including oversized individual properties.
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

Invoke capText in successText before constructing results. Test a 100 KB issue
description through tack_get_issue. Require the notice suffix and at most
`32*1024 + len(notice)` bytes.

- [ ] Update getting-started guidance: lists default to 25, accept up to 100,
  and print Next cursor for continuation as cursor. Writes return short
  confirmations; tack_get_<tool-token> returns full nodes. Replace orientation
  step 2's direct children enumeration with counts by node type. Update
  mcp_ergonomics_test.go:62-79,119-132.
- [ ] Require `make build && make test-unit && make test-integration` success
  and commit:

```bash
git add internal/adapters/mcp/tools internal/test/integration
git commit -S -m "Count workspace children, print comment names once, and cap MCP response size (TACK-508)"
```

## Task 6: Generate paging checks and release TACK-508

- [ ] In generate_search_checks.go, add
  `func (g *Generator) verifyListPaging(ctx context.Context, workspace WorkspaceIdentity, projectReference string) error`.
  Request tack_list_issues with limit 5. Above five issues, require Next cursor,
  continue once, and reject repeated references. Invoke verifyListPaging from
  Generator.Run after issue creation.
- [ ] With `TACK_DATAGEN_ALLOW_TARGET=local`, require exit 0 from
  `docker compose -f docker-compose.test.yml run --rm app ops qa datagen seed --scale small --commit`.
- [ ] Commit, open a PR, and merge after CI. Deploy QA and run AGENTS.md datagen.
  Verify one 25-row TACK issue page with a cursor through MCP. Deploy production
  with `./server ops deploy`, repeat, then close TACK-508.

## Task 7: Report search unavailability

Update domain/search/searcher.go, adapters/search/search.go:12-20,
tools/search.go:59-62, and response.go classifyError. Add
`var ErrUnavailable = errors.New("search backend unavailable")` in domainsearch
and integration/mcp_search_test.go. Before Task 9, the test stack lacks Meilisearch
and BuildGraph selects Noop.

- [ ] Add the test and require integration failure from the current successful
  `0 results` response:

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

- [ ] Make Noop.Search return `nil, nil, domainsearch.ErrUnavailable`, update
  its comment, and classify it:

```go
	case errors.Is(err, domainsearch.ErrUnavailable):
		return recoverableError("Search is unavailable because the search backend is not connected. Use tack_list_<plural> with filters, or yield to the user")
```

- [ ] Require the test to pass and commit:

```bash
git commit -S -m "Report an unavailable search backend from tack_search instead of zero results (TACK-509)"
```

## Task 8: Derive searchable text from metadata

Create service/search_doc.go and search_doc_test.go. Delete searchDocFromView
from node.go:345-359 and update calls at node.go:151 and
node_create_effects.go:13-22. Update meilisearch.go:32-53 and graph.go:132-144.

- [ ] Add this real-domain-value test and require undefined SearchDocFromView:

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
		t.Fatal("search indexed the UUID property")
	}
	if string(doc.Props["description"]) != `"Restore the ledger"` {
		t.Fatalf("description = %s", doc.Props["description"])
	}
}
```

Use make test-unit, adding service packages if its selection only includes ops.

- [ ] Implement projection:

```go
package service

// searchablePropertyTypes selects text-like values for keyword queries.
// Keyword projection excludes UUID, number, date, and checkbox values.
var searchablePropertyTypes = map[node.PropertyType]bool{
	node.PropertyTypeText:        true,
	node.PropertyTypeSelect:      true,
	node.PropertyTypeMultiSelect: true,
	node.PropertyTypeURL:         true,
}

// SearchDocFromView includes properties with searchable definition types.
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

In NodeService.Update and indexCreateSearch, read
`s.propertyDefs.List(ctx, view.OrgID)` and invoke SearchDocFromView. Preserve
warn-and-skip behavior after definition read failure. Set
`var searchableAttributes = []string{"name", "props"}`. Change EnsureIndex to
`EnsureIndex(collection string, filterableAttributes, searchableAttributes []string) error`
and add after filterable setup:

```go
	searchable := append([]string(nil), searchableAttributes...)
	if _, err := c.meili.Index(collection).UpdateSearchableAttributes(&searchable); err != nil {
		return fmt.Errorf("set searchable attributes on %s: %w", collection, err)
	}
```

Pass `[]string{"name", "props"}` from buildSearcher to rank names above properties.

- [ ] Require `make build && make test-unit` success and commit:

```bash
git commit -S -m "Index only searchable property types and rank name above properties (TACK-509)"
```

## Task 9: Test real Meilisearch

Copy the service from docker-compose.yml:247-280 into the test stack without
production volumes. Set `MEILI_ENV: development`, a test master key,
`MEILI_URL: http://meilisearch:7700`, and MEILI_MASTER_KEY on the runner. Add
service_healthy depends_on. Preserve the unavailable test's t.Setenv override.

- [ ] Add and pass this integration test:

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

Implement `waitFor(t, timeout, check) bool` in integration/wait.go with 200 ms
polling until success or timeout, matching asynchronous indexing. Require both
search tests to pass with make test-integration and commit:

```bash
git commit -S -m "Add Meilisearch to the test stack and test tack_search end to end (TACK-509)"
```

## Task 10: Backfill search

Extend the Searcher interface, meilisearch.go, and Noop with IndexBatch. Add
ops/search_reindex.go, VerbOpsSearchReindex beside VerbOpsReindex, and
integration/search_reindex_test.go. Register `search-reindex` as
`./server ops batch search-reindex --execute`.

- [ ] Create through noopSearcher, then backfill and query. Require the test
  to fail initially with unknown operation:

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

- [ ] Add `Searcher.IndexBatch(ctx context.Context, collection string, docs []*NodeDoc) error`.
  Noop returns ErrUnavailable. Implement the client:

```go
// IndexBatch waits for completion and reports rejected document tasks.
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

Check API declarations against
`/Users/agoodkind/go/pkg/mod/github.com/meilisearch/meilisearch-go@v0.36.3`.
Set `indexTaskPollInterval = 250 * time.Millisecond` and extend noop_searcher.go.

- [ ] Implement the operation following reindex.go:

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

Return the first failure rather than logging and continuing. Split into
search_reindex.go and search_reindex_type.go before exceeding 200 lines.
Check registration with `./server ops batch --help`; without --execute,
require a printed dry run without writes.

- [ ] Require `make build && make test-unit && make test-integration`, including
  cli_execute_gate_test.go, and commit:

```bash
git commit -S -m "Add ops batch search-reindex to backfill Meilisearch from FoundationDB (TACK-509)"
```

## Task 11: Generate search checks and release TACK-509

Add `func (g *Generator) verifySearchFindsIssue(ctx context.Context, token string,
workspace WorkspaceIdentity, reference, titleWord string) error` in
generate_search_checks.go. Query tack_search up to 20 times, 500 ms apart,
and report the reference on failure. Invoke verifySearchFindsIssue once after
issue creation per project using the first issue name's first word.
Run Task 6's local datagen, commit, open a PR, and merge after CI.
Read production logs since restart for `meilisearch.connected` or
`meilisearch.setup_failed` and record the observed cause on TACK-509.
On QA, run `docker compose run --rm app ops batch search-reindex --execute`
and require backup matches. Deploy production, repeat the command and query,
and set the ticket Done.

## Task 12: Seed organization synonym nodes

Update domain/node/types.go and service/seed.go:14-29,75+,306-318. Add
integration/mcp_synonym_set_test.go. Define node type synonym_set, list token
synonym_sets, feature has_synonyms, and a text terms property applicable to
that feature. Terms stores comma-separated equivalents, such as `db, database`.

- [ ] Add the failing unknown-tool regression:

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

- [ ] Add `FeatureHasSynonyms Feature = "has_synonyms"`,
  `nodeTypeSynonymSet = "synonym_set"`, and workspace containment for that type.
  Add the seed row and definition:

```go
		{"synonym_set", "synonym_sets", "Synonym Set", nodeTypeSynonymSet, node.Features{node.FeatureHasSynonyms}, scopedName, nil, []string{nodeTypeWorkspace}, nil},
```

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

Find existing-org SeedOrg invocation through search_code. If absent, add
`ops batch seed-orgs` in ops/seed_orgs.go using listOrgIDs and
`service.NewSeeder(env.Stores.PropertyDefs, env.Stores.NodeTypes).SeedOrg`.
Seeder.SeedOrg permits identical reruns.

- [ ] Require success and commit:

```bash
git commit -S -m "Add synonym_set node type with a terms property to the org seed (TACK-510)"
```

## Task 13: Expand synonym queries

Create tools/search_synonyms.go and adapters/search/meilisearch_multi.go.
Update the Searcher interface, Noop, integration noop_searcher.go, and
tools/search.go:55-65. Add integration/mcp_search_synonyms_test.go.
Declare `Searcher.SearchVariants(ctx context.Context, collection string, queries []string, filters map[string]string, limit int) ([]NodeDoc, error)`.

- [ ] Add these tests. Require TestSearchUsesOrgSynonyms to fail initially.
  Ensure each NewMCPHarness creates a distinct org, correcting shared bootstrap
  if necessary:

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

- [ ] Implement bounded expansion:

```go
package tools

// maxQueryVariants limits the number of synonym queries per search.
const maxQueryVariants = 8

// expandQuery adds whole-word substitutions up to maxQueryVariants.
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

Implement `loadSynonymSets(ctx, resolver, workspace) ([][]string, error)`.
Select types in resolver.typeIndex with FeatureHasSynonyms and call ListPage
for each with parent_id equal to the workspace and Limit maxListLimit.
Select the org property definition with has_synonyms in AppliesToFeatures.
Split its value on commas, trim spaces, and omit multi-word terms because
expandQuery replaces single words.

- [ ] Implement SearchVariants and extract buildFilter/decodeHits from Search
  into the same file. Both search methods use those helpers. Noop.SearchVariants
  returns ErrUnavailable:

```go
// SearchVariants submits variants together; Meilisearch merges and ranks hits.
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

Replace tack_search's searcher.Search invocation with:

```go
			synonymSets, err := loadSynonymSets(ctx, resolver, ws)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			docs, err := searcher.SearchVariants(ctx, "nodes", expandQuery(query, synonymSets), filters, maxListLimit)
```

Use Task 3's limit rather than maxListLimit if public search gains paging there.

- [ ] Require `make build && make test-unit && make test-integration` success
  and commit:

```bash
git commit -S -m "Expand tack_search queries with org synonym sets through federated search (TACK-510)"
```

## Task 14: Generate synonym checks and release TACK-510

Add verifySynonymSearch in generate_search_checks.go. Create `db, database`
synonyms per workspace and an issue `Database datagen check`. Require a db
query to find it within ten seconds. Explain synonym-set matching and
`tack_create_synonym_set` in getting_started.md.tmpl's Search section.
Run local datagen, commit, open a PR, and merge after CI. Reseed QA orgs with
Task 12, create a synonym set, and verify search. Deploy production, reseed,
repeat the check, and set TACK-510 Done.
