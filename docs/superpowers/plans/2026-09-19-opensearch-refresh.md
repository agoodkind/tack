# Search Metadata Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply metadata changes to search without restarting Tack.

**Architecture:** FoundationDB stores one projection epoch per organization. Metadata writes increment that epoch. MCP server assembly keys its generated type and tool state by the epoch and rebuilds that state after a mismatch.

**Tech Stack:** Go, FoundationDB, MCP, existing metadata stores.

**Spec:** [Opaque metadata](../specs/2026-09-19-search-acceptance.md#opaque-metadata).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Use exported metadata operations. Do not add a search-specific property registry or infer behavior from seeded identifiers.

## Review Focus

Test new opaque types, changed projections, concurrent requests, a failed metadata read, and a deleted type.

---

### Task 10: Refresh search metadata after writes

**Files:**

- Create: `internal/adapters/foundationdb/search_projection_epoch.go`
- Create: `internal/test/integration/search_metadata_refresh_test.go`
- Modify: `internal/domain/node/reader.go`
- Modify: `internal/adapters/foundationdb/property.go`
- Modify: `internal/adapters/foundationdb/node_type.go`
- Modify: `internal/adapters/mcp/server.go`

**Interfaces:**

- Consumes: Task 2 projection declarations, Task 7 public search, `PropertyDefStore.Set`, and `NodeTypeStore.Set`.
- Produces: `ProjectionVersion(context.Context, uuid.UUID) (string, error)` and epoch-aware MCP registration.

- [ ] **Step 1: Add the failing authenticated refresh test.**

```go
func TestSearchMetadataAfterStartup(t *testing.T) {
    graph, driver, token, entryID, parameter := newSearchMCP(t, 128)
    t.Cleanup(graph.Close)
    firstType, firstProperty := putOpaqueSearchDefinition(t, graph, true)
    firstNode := createOpaqueNode(t, driver, token, entryID, parameter, firstType, firstProperty, "amber telescope")
    requireSearchResult(t, driver, token, entryID, parameter, "amber telescope", firstNode)

    secondType, secondProperty := putOpaqueSearchDefinition(t, graph, true)
    secondNode := createOpaqueNode(t, driver, token, entryID, parameter, secondType, secondProperty, "violet turbine")
    requireSearchResult(t, driver, token, entryID, parameter, "violet turbine", secondNode)
}
```

- [ ] **Step 2: Record the deferred failure contract.**

Task 13 runs `^TestSearchMetadataAfterStartup$` against the completed branch. The test must fail when a running MCP server retains stale metadata, publishes partial metadata, or requires restart. Do not start live dependencies during this coding task.

- [ ] **Step 3: Store and increment the organization projection epoch.**

Add the epoch key to the central FoundationDB key catalog. Increment it in the same transaction as every property definition, node type, or relationship change that alters projected text or indexed access fields. Return the version as a stable decimal string.

```go
func (s *ViewStore) ProjectionVersion(ctx context.Context, nodeID uuid.UUID) (string, error) {
    resolved, err := s.Resolve(ctx, nodeID)
    if err != nil { return "", err }
    epoch, err := s.projectionEpoch(ctx, resolved.OrgID)
    if err != nil { return "", err }
    return strconv.FormatUint(epoch, 10), nil
}
```

- [ ] **Step 4: Rebuild MCP metadata after an epoch change.**

Cache the generated tool server with its organization epoch. Before dispatch, read the current epoch. Reuse the server only when the epochs match. Build the replacement from a complete metadata read, then atomically replace the cached entry. Preserve membership checks on every request.

```go
if cached != nil && cached.ProjectionVersion == currentVersion {
    return cached.Server, nil
}
replacement, err := s.buildOrgServer(ctx, orgID, currentVersion)
if err != nil { return nil, fmt.Errorf("reload metadata: %w", err) }
s.servers.Store(orgID, replacement)
return replacement.Server, nil
```

- [ ] **Step 5: Prove failures do not become empty metadata.**

Inject a real FDB read failure by stopping the disposable FDB dependency after the epoch changes. Require the request to return an explicit error. Restart FDB and require the new type to appear. Do not retain or publish an empty successful server.

- [ ] **Step 6: Add concurrent and deletion coverage.**

Add a test that searches while updating one projection from excluded to included and deleting another opaque type. Each completed request may use one complete epoch. No request may mix old type registration with new projection rules. Task 13 executes this test.

- [ ] **Step 7: Run the serial coding checks.**

Run: `go test ./internal/test/integration -run '^$' -count=1`

Run: `make check`

Expected: PASS after compiling the integration package without executing its tests. Task 13 runs metadata refresh and failure recovery.

- [ ] **Step 8: Commit the task.**

```sh
git add internal/domain/node/reader.go internal/adapters/foundationdb internal/adapters/mcp/server.go internal/test/integration/search_metadata_refresh_test.go
git commit -S -m "Refresh search metadata after declaration changes" -m "Co-authored-by: Codex <noreply@openai.com>"
```
