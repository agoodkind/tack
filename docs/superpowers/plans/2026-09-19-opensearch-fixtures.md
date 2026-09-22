# Search Test Fixture Reference

The implementation plans use these shared real-dependency fixtures. Each owning task creates and commits the listed helper with its production change.

**Goal:** Test search with real storage and arbitrary metadata without loading product seeds.

**Architecture:** Store tests construct only the records they need. Public acceptance tests create real credentials and use the production authenticated MCP handler through the existing datagen driver.

**Tech Stack:** Go, FoundationDB, SQL authentication, MCP, and official OpenSearch Go client v4.7.3.

**Spec:** [Evidence and opaque metadata](../specs/2026-09-19-search-acceptance.md#opaque-metadata).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Luna writes these fixtures only inside the selected Tack worktree and does not run them. The only permitted external writes are the documented branch and pull-request publication operations. No fixtures replace production dependencies. These steps are part of the index and query pipeline tasks, not a separate deployment. Sol runs every real-dependency fixture.

## Review Focus

Exercise unfamiliar property types, unrelated names, no product seed, real bearer validation, and both JSON/SSE tool responses. Every fixture property definition explicitly includes or excludes search. Add one excluded value beside each included fixture and prove that search omits it.

---

## Reader fixture for the index pipeline

Create the fixture file listed in the index pipeline. It uses `clearPrefix`, testenv FoundationDB, and the new declaration types. The reader and work-store tests call its two functions.

- [ ] Implement the fixture before running the reader's failing test:

```go
func newSearchStore(t *testing.T) *foundationdb.Stores {
    t.Helper()
    cluster := testenv.FoundationDB(t)
    prefix := []byte("search-test:" + uuid.Must(uuid.NewV7()).String() + ":")
    foundationdb.SetTestPrefix(prefix)
    t.Cleanup(func() { clearPrefix(t, cluster, prefix); foundationdb.SetTestPrefix(nil) })
    stores, err := foundationdb.NewStores(cluster, nil)
    if err != nil { t.Fatal(err) }
    return stores
}

func putSearchText(t *testing.T, stores *foundationdb.Stores, text string) uuid.UUID {
    t.Helper()
    id := uuid.Must(uuid.NewV7())
    typeKey := "n" + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
    propertyKey := "p" + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
    def := &node.PropertyDef{
        ID: uuid.Must(uuid.NewV7()), OrgID: id, Name: propertyKey,
        Type: node.PropertyType(uuid.Must(uuid.NewV7()).String()),
        Search: &node.SearchProjection{Include:true, Order:0, Rule:node.TextRule{Mode:"scalar"}},
    }
    if err := stores.PropertyDefs.Set(t.Context(), def); err != nil { t.Fatal(err) }
    kind := &node.NodeType{
        ID: uuid.Must(uuid.NewV7()), OrgID:id, Name:typeKey, TypeKey:typeKey,
        Slug:typeKey, PluralSlug:typeKey+"s", PropertyDefIDs:[]uuid.UUID{def.ID},
    }
    if err := stores.NodeTypes.Set(t.Context(), kind); err != nil { t.Fatal(err) }
    raw, err := json.Marshal(text)
    if err != nil { t.Fatal(err) }
    props := map[string]json.RawMessage{propertyKey:raw}
    now := clock.Now().UTC()
    value := &node.Node{ID:id, OrgID:id, NodeType:typeKey, Props:props, CreatedAt:now, UpdatedAt:now}
    view := &node.NodeView{ID:id, OrgID:id, NodeType:typeKey, Props:props, CreatedAt:now, UpdatedAt:now}
    if err := stores.Nodes.Set(t.Context(), value, view); err != nil { t.Fatal(err) }
    return id
}
```

- [ ] Keep the reader and work-store tests ready for final validation. Assert that renaming the generated type and property keys cannot change projected text. Do not use `t.Parallel` with the process-global prefix.

## Authenticated calls for the query pipeline and QA datagen

Create `internal/datagen/driver_raw.go`. Refactor the existing private `Driver.call` into one internal path that accepts typed or raw arguments. Make `Driver.Call` and `Driver.CallRaw` delegate to it. Preserve authentication, context checks, request IDs, call counts, dry-run behavior, JSON-RPC framing, session headers, response bounds, sending, and JSON/SSE decoding in that single path.

- [ ] Implement the public raw entry point without copying the existing call body:

```go
func (d *Driver) CallRaw(ctx context.Context, token, tool string, arguments map[string]json.RawMessage) (Result, error) {
    return d.call(ctx, token, tool, arguments)
}
```

- [ ] Build `newSearchMCP(t *testing.T, pageBytes int) (*runtime.Graph, *datagen.Driver, string, uuid.UUID, string)` in the MCP fixture file. Return graph, driver, raw token, entry node ID, and the metadata-derived entry parameter. Load test engine configuration with `OPENSEARCH_PUBLIC_ENABLED=true`, use production auth, initialize opaque metadata and a metadata-defined entry node, then build the production Graph. Provision real audit dependencies using the existing audit integration setup. Register Graph.Close, SQL identity removal, and FDB prefix cleanup. Do not disable audit to make the fixture start.
- [ ] Create a UUIDv7 user with UserRepo.Create, then add membership with OrgMemberRepo.AddMember using the entry node's organization. Generate a random token and store its hash through TokenRepo.Create. Keep the raw token only in fixture memory. Construct the driver with NewDriver(graph, false, a unique seed).
- [ ] Use the returned entry parameter and ID for public calls. This test pins empty-query validation through the actual HTTP handler:

```go
func TestSearchEmptyQuery(t *testing.T) {
    _, driver, token, entryID, parameter := newSearchMCP(t, 128)
    entry, err := json.Marshal(entryID.String())
    if err != nil { t.Fatal(err) }
    _, err = driver.CallRaw(t.Context(), token, "tack_search", map[string]json.RawMessage{
        parameter: entry, "query": json.RawMessage(`""`),
    })
    if err == nil { t.Fatal("empty query succeeded") }
    if !strings.Contains(err.Error(), "query") { t.Fatalf("unrelated failure: %v", err) }
}
```

- [ ] Include these fixture files in their owning index or query pipeline commit. The final validation plan runs `^TestSearchEmptyQuery$` and the complete search suite.

## Delayed-write test for the index pipeline

This test uses the real stores, native client, and page worker. It delays a registered request until deletion completes, then sends that request to OpenSearch. Add it to the index pipeline recovery test.

- [ ] Add this test before implementing retirement. The final validation plan requires it to fail when an old request can restore text.

```go
func TestSearchDelayedWriter(t *testing.T) {
    ctx := t.Context()
    stores := newSearchStore(t)
    writer, client := newOpenSearchClients(t, "opensearchproject/opensearch:3.8.0", 8<<30)
    model, err := writer.Provision(ctx)
    if err != nil { t.Fatal(err) }
    index := "delayed-" + uuid.Must(uuid.NewV7()).String()
    spec := searchadapter.IndexSpec{Model:model, MappingVersion:"search-v1", Primaries:1, RoutingShards:8}
    if err := writer.CreateIndex(ctx, index, spec); err != nil { t.Fatal(err) }
    t.Cleanup(func() { deleteIndex(t, client, index) })
    if err := stores.SearchWork.InitializeIndex(ctx, index); err != nil { t.Fatal(err) }
    id := putSearchText(t, stores, "obsolete text")
    oldWork, err := stores.SearchWork.Claim(ctx, search.WorkLive, "old-worker", time.Minute)
    if err != nil { t.Fatal(err) }
    page, err := stores.Views.Content(ctx, node.ContentRequest{NodeID:id, MaxBytes:128})
    if err != nil { t.Fatal(err) }
    oldRequest, err := stores.SearchWork.Register(ctx, oldWork, page)
    if err != nil { t.Fatal(err) }
    if err := stores.Nodes.Delete(ctx, id, id); err != nil { t.Fatal(err) }
    if err := stores.SearchWork.Release(ctx, oldWork, "request delayed"); err != nil && !errors.Is(err, search.ErrWorkChanged) { t.Fatal(err) }
    deletion, err := stores.SearchWork.Claim(ctx, search.WorkCleanup, "new-worker", time.Minute)
    if err != nil { t.Fatal(err) }
    if !deletion.Deleted { t.Fatal("deletion not scheduled") }
    worker := service.Worker{Reader:stores.Views, Work:stores.SearchWork, Writer:writer, PageBytes:128}
    if err := worker.RunOne(ctx, deletion); err != nil { t.Fatal(err) }
    if err := writer.Put(ctx, oldRequest); err == nil { t.Fatal("obsolete write accepted") }
    result, err := getDocument(ctx, client, index, oldRequest.DocumentID)
    if err != nil { t.Fatal(err) }
    if string(result.Source["retired"]) != "true" || len(result.Source["page_text"]) != 0 { t.Fatal("deleted text restored") }
}
```

`deleteIndex` and `getDocument` are test helpers that call the official typed `Indices.Delete` and `Document.Get` APIs. They do not add production adapter methods.

- [ ] The final validation plan runs `^TestSearchDelayedWriter$`. Require the retained higher-generation record and rejected delayed request, not merely an empty MCP response.

## Helper contracts used by task plans
Each helper below is test code in the named owning file. It calls real dependencies and production boundaries. No helper replaces a production dependency.
```go
// search_native_test.go
type nativePage struct { Source string; Chunks []string; Weights []map[string]float64; Access node.SearchAccess }
func newOpenSearchClients(t *testing.T, image string, memoryBytes int64) (*searchadapter.Adapter, *opensearchapi.Client)
func createNativeSearchIndex(t *testing.T, adapter *searchadapter.Adapter, model searchadapter.ModelInfo, mappingVersion string, primaries, routing, replicas int) string
func putNativePage(t *testing.T, client *opensearchapi.Client, index, text string)
func getNativePage(t *testing.T, client *opensearchapi.Client, index string) nativePage
func requireCompleteNativeChunks(t *testing.T, page nativePage, original string)
func unicodePage4096() string

// search_projection_backfill_test.go
type projectionBackfillFixture struct { Stores *foundationdb.Stores; Run func(context.Context, []byte, bool) (ops.ProjectionBackfillResult, error) }
func newProjectionBackfillFixture(t *testing.T) projectionBackfillFixture
func (f projectionBackfillFixture) ManifestForEveryMissingDefinition() []byte

// search_store_fixture_test.go
func reopenSearchStore(t *testing.T, current *foundationdb.Stores) *foundationdb.Stores
func putOpaqueSearchDefinition(t *testing.T, graph *runtime.Graph, include bool) (nodeType, propertyName string)
func createOpaqueNode(t *testing.T, driver *datagen.Driver, token string, entryID uuid.UUID, parameter, nodeType, propertyName, text string) uuid.UUID
func requireSearchResult(t *testing.T, driver *datagen.Driver, token string, entryID uuid.UUID, parameter, query string, nodeID uuid.UUID)

// search_ranking_test.go
type rankedCorpus struct { Nodes, DuplicatePages, PrimaryShards int }
func newRankedCorpus(t *testing.T, corpus rankedCorpus) (search.Ranker, search.Query)
func collectDistinctNodes(t *testing.T, ranker search.Ranker, query search.Query, snapshot search.Snapshot) []uuid.UUID

// search_auth_test.go
type permissionNode struct { ID uuid.UUID; Access node.SearchAccess }
type searchPages struct { IDs []uuid.UUID; Cursors []string }
func putPermissionCorpus(t *testing.T, fixture searchMCPFixture, query string) (permissionNode, permissionNode)
func corruptIndexedAccess(t *testing.T, adapter *searchadapter.Adapter, node permissionNode, access node.SearchAccess)
func callEverySearchPage(t *testing.T, fixture searchMCPFixture, query string) searchPages
// search_access_refresh_test.go
func activePhysicalIndex(t *testing.T, fixture searchMCPFixture) string
func readSemanticFields(t *testing.T, adapter *searchadapter.Adapter, index string, nodes []permissionNode) map[string]nativePage
func requireSemanticFieldsEqual(t *testing.T, want, got map[string]nativePage)
func undeploySearchModel(t *testing.T, adapter *searchadapter.Adapter)
func runAccessRollout(t *testing.T, fixture searchMCPFixture, candidateVersion string)
func requireSearchUsesAccessVersion(t *testing.T, fixture searchMCPFixture, version string)
// search_runtime_test.go and search_rebuild_test.go
type searchMCPFixture struct { Graph *runtime.Graph; Driver *datagen.Driver; Adapter *searchadapter.Adapter; Token, EntryParameter string; EntryID uuid.UUID; StartOpenSearch func(*testing.T) }
func newSearchMCPFixture(t *testing.T, pageBytes int) searchMCPFixture
func newStoppedSearchRuntime(t *testing.T) searchMCPFixture
func createNodeThroughMCP(t *testing.T, fixture searchMCPFixture, text string) uuid.UUID
func requireSearchUnavailable(t *testing.T, fixture searchMCPFixture, query string)
func requireEventuallySearchResult(t *testing.T, fixture searchMCPFixture, query string, nodeID uuid.UUID)
type pausedRebuild struct { Resume func(); RequireSuccess func(*testing.T) }
type searchCorpus struct { Present, Deleted, Foreign []uuid.UUID }
func runAuditedReindex(t *testing.T, fixture searchMCPFixture) pausedRebuild
func mutateSearchCorpusWhilePaused(t *testing.T, fixture searchMCPFixture, rebuild pausedRebuild) searchCorpus
func requireSearchCorpus(t *testing.T, fixture searchMCPFixture, expected searchCorpus)
```

`newSearchMCP` remains a small unpacking wrapper around `newSearchMCPFixture` for tests that use the existing five return values. The fixture creates real SQL identity, membership, token, FDB metadata, audit dependencies, OpenSearch, and the production Graph. Access rollout tests register the production organization-scope compiler under `org-scope-v1` and `permission-v2`; all reads and writes still use production `PolicySet` dispatch. `newStoppedSearchRuntime` creates the same graph while the configured engine endpoint is stopped and exposes `StartOpenSearch` through the fixture's real testenv lifecycle.
