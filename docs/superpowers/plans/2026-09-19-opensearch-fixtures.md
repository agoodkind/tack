# Search Test Fixture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Test search with real storage and arbitrary metadata without loading product seeds.

**Architecture:** Store tests construct only the records they need. Public acceptance tests create real credentials and use the production authenticated MCP handler through the existing datagen driver.

**Tech Stack:** Go, FoundationDB, SQL authentication, MCP.

**Spec:** [Evidence and opaque metadata](../specs/2026-09-19-search-acceptance.md#opaque-metadata).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). No fixtures replace production dependencies. These steps are part of the reader and MCP tasks, not a separate deployment.

## Review Focus

Exercise unfamiliar property types, unrelated names, no product seed, real bearer validation, and both JSON/SSE tool responses.

---

## Reader fixture for Task 2

Create the fixture file listed in the reader task. It consumes the existing
`clearPrefix`, testenv FoundationDB, and the new declaration types. It produces
the two functions used by the reader and work-store tests.

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

- [ ] Run the reader and work-store tests with real FDB. Assert that renaming the generated type and property keys cannot change projected text. Do not use `t.Parallel` with the process-global prefix.

## Authenticated calls for Tasks 7 and 10

Create `internal/datagen/driver_raw.go`. This method consumes the existing
Driver.send and decodeResponse implementations, preserving actual authentication,
session headers, response bounds, and JSON/SSE decoding. It produces
`Driver.CallRaw(context.Context, string, string, map[string]json.RawMessage) (Result, error)`.

- [ ] Implement the generic call method:

```go
func (d *Driver) CallRaw(ctx context.Context, token, tool string, arguments map[string]json.RawMessage) (Result, error) {
    if err := ctx.Err(); err != nil { return Result{}, err }
    encoded, err := json.Marshal(arguments)
    if err != nil { return Result{}, err }
    identity := d.sessionID + ":" + d.requestToken(token) + ":" + tool + ":"
    digest := sha256.Sum256(append([]byte(identity), encoded...))
    requestID := "datagen-" + hex.EncodeToString(digest[:12])
    d.callCount.Add(1)
    if d.dryRun { return syntheticResult(requestID), nil }
    request := struct {
        JSONRPC string `json:"jsonrpc"`
        ID string `json:"id"`
        Method string `json:"method"`
        Params struct {
            Name string `json:"name"`
            Arguments map[string]json.RawMessage `json:"arguments"`
        } `json:"params"`
    }{JSONRPC: "2.0", ID:requestID, Method:"tools/call"}
    request.Params.Name, request.Params.Arguments = tool, arguments
    body, err := json.Marshal(request)
    if err != nil { return Result{}, err }
    response, err := d.send(ctx, token, body, true)
    if err != nil { return Result{}, err }
    return decodeResponse(ctx, tool, response)
}
```

- [ ] Build `newSearchMCP(t *testing.T, pageBytes int) (*runtime.Graph, *datagen.Driver, string, uuid.UUID, string)` in the MCP fixture file. Return graph, driver, raw token, entry node ID, and the metadata-derived entry parameter. Load test engine configuration, use production auth, initialize opaque metadata and a metadata-defined entry node, then build the production Graph. Provision real audit dependencies using the existing audit integration setup. Register Graph.Close, SQL identity removal, and FDB prefix cleanup. Do not disable audit to make the fixture start.
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

- [ ] Run `^TestSearchEmptyQuery$` before and after query implementation, then run the complete search suite. Include these fixture files in their owning reader or MCP commit.

## Delayed-write test for Task 5

This test consumes the real stores, native client, and page worker. It delays an
already registered request until deletion has completed, then sends that request
to OpenSearch. Add it to the worker's recovery test file.

- [ ] Add and run this test before implementing retirement; require it to fail when an old request can restore text.

```go
func TestSearchDelayedWriter(t *testing.T) {
    ctx := t.Context()
    stores := newSearchStore(t)
    client, err := searchadapter.NewOpenSearch(testenv.OpenSearch(t))
    if err != nil { t.Fatal(err) }
    model, err := client.Provision(ctx)
    if err != nil { t.Fatal(err) }
    index := "delayed-" + uuid.Must(uuid.NewV7()).String()
    if err := client.CreateIndex(ctx, index, model, 3); err != nil { t.Fatal(err) }
    t.Cleanup(func() { _, err := client.JSON(context.Background(), "DELETE", "/"+index, nil); if err != nil { t.Error(err) } })
    if err := stores.SearchWork.InitializeIndex(ctx, index); err != nil { t.Fatal(err) }
    id := putSearchText(t, stores, "obsolete text")
    oldWork, err := stores.SearchWork.Claim(ctx, "old-worker", time.Minute)
    if err != nil { t.Fatal(err) }
    page, err := stores.Views.Content(ctx, node.ContentRequest{NodeID:id, MaxBytes:128})
    if err != nil { t.Fatal(err) }
    oldRequest, err := stores.SearchWork.Register(ctx, oldWork, page)
    if err != nil { t.Fatal(err) }
    if err := stores.Nodes.Delete(ctx, id, id); err != nil { t.Fatal(err) }
    if err := stores.SearchWork.Release(ctx, oldWork, "request delayed"); err != nil && !errors.Is(err, search.ErrWorkChanged) { t.Fatal(err) }
    deletion, err := stores.SearchWork.Claim(ctx, "new-worker", time.Minute)
    if err != nil { t.Fatal(err) }
    if !deletion.Deleted { t.Fatal("deletion not scheduled") }
    worker := service.Worker{Reader:stores.Views, Work:stores.SearchWork, Writer:client, PageBytes:128}
    if err := worker.RunOne(ctx, deletion); err != nil { t.Fatal(err) }
    if err := client.Put(ctx, oldRequest); err == nil { t.Fatal("obsolete write accepted") }
    raw, err := client.JSON(ctx, "GET", "/"+index+"/_doc/"+url.PathEscape(oldRequest.DocumentID), nil)
    if err != nil { t.Fatal(err) }
    var result struct { Source map[string]json.RawMessage `json:"_source"` }
    if err := json.Unmarshal(raw, &result); err != nil { t.Fatal(err) }
    if string(result.Source["retired"]) != "true" || len(result.Source["page_text"]) != 0 { t.Fatal("deleted text restored") }
}
```

- [ ] Run `^TestSearchDelayedWriter$` after retirement implementation. Require the retained version-2 record and rejected delayed request, not merely an empty MCP response.
