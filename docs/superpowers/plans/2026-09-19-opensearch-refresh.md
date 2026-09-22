# Search Metadata and Access Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply metadata and permission-policy changes without restarting Tack or replacing the physical index. Regenerate embeddings only for pages changed by text projection updates. Permission-policy changes regenerate no embeddings.

**Architecture:** FoundationDB stores one projection epoch per organization and one durable access-policy rollout per authoritative permission root. Metadata writes refresh MCP registration and schedule bounded content work against the serving index. Workers reread and reembed only affected pages, then retire obsolete page documents. Permission changes update query keys or schedule access-only document work. A candidate policy writes beside the active version until every current page is verified, then FoundationDB activates it atomically.

**Tech Stack:** Go, FoundationDB, MCP, existing metadata stores, and the completed OpenSearch index and query pipelines.

**Spec:** [Opaque metadata](../specs/2026-09-19-search-acceptance.md#opaque-metadata) and [permission expansion](../specs/2026-09-19-search-acceptance.md#permission-expansion).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Perform this Luna task only inside the selected Tack worktree. The only permitted external writes are the documented branch and pull-request publication operations. Use exported metadata operations. Do not start or query FoundationDB, OpenSearch, MCP, Docker, or another service during this coding task. Do not add a search-specific property registry or inspect permission node and relationship types in search code. Keep the physical index, document IDs, text projection, semantic source, chunks, and sparse weights unchanged throughout an access-policy transition.

## Review Focus

Test new opaque types, changed projections, bounded content refresh, unchanged pages, membership-only changes, resource grants, restart during every rollout phase, stale content and access writes, active old-version sessions, failed metadata reads, and deleted types.

---

### Task 1: Refresh metadata and access policy after writes

**Files:**

- Create: `internal/domain/search/access_rollout.go`
- Create: `internal/adapters/foundationdb/search_access_rollout.go`
- Create: `internal/service/search_access_rollout.go`
- Create: `internal/test/integration/search_access_refresh_test.go`
- Create: `internal/adapters/foundationdb/search_projection_epoch.go`
- Test: `internal/test/integration/search_metadata_refresh_test.go`
- Modify: `internal/domain/node/reader.go`
- Modify: `internal/adapters/foundationdb/property.go`
- Modify: `internal/adapters/foundationdb/node_type.go`
- Modify: `internal/adapters/mcp/server.go`
- Modify: `internal/runtime/graph.go`

**Interfaces:**

- This plan requires projection declarations, `PolicySet`, `AccessStateReader`, durable content scans, durable access work, access-only writes, session presence keys, `PropertyDefStore.Set`, and `NodeTypeStore.Set`.
- This plan implements `ProjectionVersion`, durable access-policy activation, and restartable access-key cleanup for rebuild and release.

```go
type AccessPhase string
const (
    AccessStable AccessPhase = "stable"
    AccessBackfill AccessPhase = "backfill"
    AccessVerifying AccessPhase = "verifying"
    AccessRetiring AccessPhase = "retiring"
)
type AccessRollout struct {
    AuthorityID uuid.UUID
    ActiveVersion, CandidateVersion, PreviousVersion string
    WriteVersions []string
    Phase AccessPhase
    ScanCursor, VerifyCursor, RetireCursor string
    Generation, PermissionEventVersion int64
}
type BeginAccessRollout struct { AuthorityID uuid.UUID; CandidateVersion string; ExpectedGeneration, PermissionEventVersion int64 }
type AccessRolloutStore interface {
    searchaccess.AccessStateReader
    Current(context.Context, uuid.UUID) (AccessRollout, error)
    Begin(context.Context, BeginAccessRollout) (AccessRollout, error)
    Checkpoint(context.Context, AccessRollout) error
    Activate(context.Context, AccessRollout) (AccessRollout, error)
    BeginRetire(context.Context, AccessRollout) (AccessRollout, error)
    Complete(context.Context, AccessRollout) (AccessRollout, error)
}
type AccessVersionSessions interface {
    HasActiveAccessVersion(context.Context, uuid.UUID, string) (bool, error)
}
```

- [ ] **Step 1: Add failing metadata and access refresh tests.**

```go
func TestSearchAccessVersionWithoutRebuild(t *testing.T) {
    fixture := newSearchMCPFixture(t, 128)
    nodes := putPermissionCorpus(t, fixture, "needle")
    index := activePhysicalIndex(t, fixture)
    before := readSemanticFields(t, fixture.Adapter, index, nodes)
    undeploySearchModel(t, fixture.Adapter)
    runAccessRollout(t, fixture, "permission-v2")
    if got := activePhysicalIndex(t, fixture); got != index { t.Fatalf("index = %q, want %q", got, index) }
    requireSemanticFieldsEqual(t, before, readSemanticFields(t, fixture.Adapter, index, nodes))
    requireSearchUsesAccessVersion(t, fixture, "permission-v2")
}
```

Keep the authenticated metadata test. It must create a property definition after startup and find an affected node through that new definition without restarting Tack or changing the physical index. It must prove that affected pages receive new semantic fields and unaffected pages preserve their semantic fields byte for byte.

- [ ] **Step 2: Record the deferred failure contract.**

The final validation plan runs `^TestSearch(MetadataAfterStartup|AccessVersionWithoutRebuild|AccessMembershipQueryOnly|AccessResourceRefresh|AccessRolloutRecovery)$` against the completed branch. The tests must fail when runtime metadata refresh, serving-index content refresh, access-only updates, exact verification, version activation, or restart recovery is absent. Do not start live dependencies during this coding task.

- [ ] **Step 3: Store and increment the organization projection epoch.**

Add the epoch key to the central FoundationDB key catalog. Increment it in the same transaction as every property definition or node type change that alters projected text. Schedule a bounded content scan for the affected nodes in that transaction. The existing index workers write the changed pages to the serving physical index, run inference for those pages, and retire obsolete document IDs. Return the version as a stable decimal string. A permission node change does not increment the text projection epoch.

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

Cache the generated tool server with its organization epoch. Before dispatch, read the current epoch. Reuse the server only when the epochs match. Build the replacement from one complete metadata read, then atomically replace the cached entry. A failed read returns an explicit error and cannot publish empty metadata.

- [ ] **Step 5: Persist one access rollout state machine.**

Extend the index pipeline's stable access state into this rollout state. Initialize each current organization authority with active and write version `org-scope-v1`. `Begin` requires an authority ID and a distinct nonempty candidate for which `PolicySet.Supports` returns true. In one transaction, set `CandidateVersion`, preserve `PreviousVersion`, set sorted `WriteVersions` to active plus candidate, increment `Generation`, record the authority's current permission-event boundary, enter `backfill`, and schedule a bounded access scan for that authority. Reject another candidate for that authority until it returns to `stable`; unrelated authorities can transition concurrently.

- [ ] **Step 6: Keep permission mutation effects explicit.**

The permission service classifies its own node and relationship mutations. A principal membership mutation changes query evaluation only and schedules no OpenSearch work. A direct or inherited resource grant increments the authority's permission-event version and calls the index pipeline's transaction-bound access scheduler for the affected node or bounded scan root in the same transaction. Search storage never switches on permission type identifiers.

- [ ] **Step 7: Backfill and verify every current page.**

New content and access writes use every `WriteVersions` entry. Resume the durable access scan from `ScanCursor`. Each scheduled node receives a new per-node search generation; rollout `Generation` only guards rollout checkpoints. The index pipeline partially updates only `search_generation` and `access`. After the scan finishes, enter `verifying`. Read current issued document IDs from FoundationDB in bounded batches and fetch those exact documents through the official client. Require each source to contain the candidate version and current per-node generation. Missing, retired, or stale IDs schedule current content work and keep verification pending. Compare the permission-event boundary when verification completes. Repeat the affected scan and verification when a concurrent resource permission mutation advanced it.

- [ ] **Step 8: Activate the candidate and retire old keys.**

After exact verification, one FoundationDB transaction compares the verified permission-event boundary, makes the candidate active, and records the previous version. New sessions use the candidate; established sessions retain their stored authority, version, and point in time. Before removal, require `HasActiveAccessVersion(authorityID, previous) == false`. Enter `retiring`, set `WriteVersions` to the active version, record a new permission-event boundary, and run another bounded access scan for that authority. Exact verification and a matching boundary must prove that every current page contains only the active version before `Complete` clears rollback state and returns to `stable`.

Before retirement begins, rollback atomically restores `PreviousVersion` as active because every current page still contains both versions. After retirement begins, rollback requires another ordinary candidate transition; it never requires index replacement.

- [ ] **Step 9: Add restart, race, and failure coverage.**

Final-validation coverage stops FoundationDB after a projection epoch changes and requires an explicit metadata error, then restarts it and requires the new definition to appear. Search while changing one projection and deleting another type; each completed request must use one complete epoch. Require the same physical index and alias. Require affected pages to contain new semantic fields. Require unaffected pages to preserve semantic fields byte for byte. Require workers to retire obsolete affected-page document IDs. Stop and reopen FoundationDB and Tack during `backfill`, `verifying`, activation, and `retiring`. Fail one partial bulk item and one exact document read. Pause older content and access requests across activation. Mutate a resource grant during verification and require the changed event boundary to repeat the affected scan before activation. Run different candidate versions for two authorities concurrently and require independent state and session counts. Require one active version per authority, resumable cursors, no stale overwrite, no alias change, no physical-index creation, and no semantic-field change during access transitions. Change a principal membership and require zero document writes. Change a resource grant and require access updates for every current page but zero content reads. Sol runs this coverage. Luna does not start its dependencies.

- [ ] **Step 10: Register the refresh services.**

Construct the metadata refresh and access-rollout services in `internal/runtime/graph.go`. Register their bounded worker loops and shutdown with the existing search runtime. This task must not leave a constructor, store, or service reachable only from tests.

- [ ] **Step 11: Run the serial coding checks.**

Run: `make build`

Expected: PASS. The final validation plan runs metadata refresh and the complete access transition with real dependencies.

- [ ] **Step 12: Create the next Graphite slice.**

```sh
git add internal/domain/search/access_rollout.go internal/domain/node/reader.go internal/adapters/foundationdb/search_access_rollout.go internal/adapters/foundationdb/search_projection_epoch.go internal/adapters/foundationdb/property.go internal/adapters/foundationdb/node_type.go internal/adapters/mcp/server.go internal/service/search_access_rollout.go internal/runtime/graph.go internal/test/integration/search_access_refresh_test.go internal/test/integration/search_metadata_refresh_test.go
```

Run Graphite MCP `create` from stack position 4 with this exact message:

```text
Refresh search metadata and access keys without rebuilding

Co-authored-by: Codex <noreply@openai.com>
```

This branch is stack position 5.
