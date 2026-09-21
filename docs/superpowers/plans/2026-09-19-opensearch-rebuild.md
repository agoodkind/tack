# OpenSearch Index Replacement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace an index after mapping, model, cleanup, restore, or shard changes without losing concurrent source mutations.

**Architecture:** One persisted coordinator owns one source and one target. Full replacement reads FoundationDB pages. A permitted primary-shard increase uses native split and reuses existing embeddings. Both paths replay the mutation journal and switch one alias atomically.

**Tech Stack:** Go, FoundationDB, OpenSearch Go client v4.7.3 typed split and alias APIs.

**Spec:** [Index replacement lifecycle](../specs/2026-09-19-search-acceptance.md#index-replacement-lifecycle).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Keep at most one serving, one replacement, and one retiring index. Restored source data always requires a full replacement. Native split never replaces journal replay, alias coordination, or session retirement.

## Review Focus

Test concurrent metadata and ancestry edits, failure before and after alias switching, split failure, insufficient disk, restored data, and active old-index sessions.

---

### Task 8: Build and switch replacement indexes

**Files:**

- Create: `internal/domain/search/rebuild.go`
- Create: `internal/adapters/foundationdb/search_rebuild.go`
- Create: `internal/service/search_rebuild.go`
- Create: `internal/adapters/search/opensearch_alias.go`
- Create: `internal/adapters/search/opensearch_split.go`
- Replace: body of `internal/ops/search_reindex.go`
- Test: `internal/test/integration/search_rebuild_test.go`
- Test: `internal/test/integration/search_split_test.go`
- Test: `internal/test/integration/search_restore_test.go`

**Interfaces:**

- Consumes: `ContentReader.ScanSearch`, Task 5 worker and writer, mutation journal, current generation and replica settings.
- Produces: `Rebuilder`, typed split, and atomic alias switching for Tasks 9 and 12.

```go
type ReplacementMode uint8
const ( ReplacementFull ReplacementMode = iota + 1; ReplacementSplit )
type Rebuild struct {
    ID uuid.UUID
    SourceIndex, TargetIndex, ScanCursor, State string
    ProjectionVersion, MappingVersion, ModelID string
    JournalBoundary, ReplayCursor []byte
    Mode ReplacementMode
    PrimaryShards, RoutingShards, Replicas int
}
type Rebuilder interface { Run(context.Context) error }
func (a *Adapter) SplitIndex(context.Context, string, string, int) error
func (a *Adapter) SwitchAlias(context.Context, string, string, string) error
```

- [ ] **Step 1: Add failing concurrent-change tests.**

```go
func TestSearchRebuildDuringChanges(t *testing.T) {
    fixture := newSearchMCPFixture(t, 128)
    started := runAuditedReindex(t, fixture)
    expected := mutateSearchCorpusWhilePaused(t, fixture, started)
    started.Resume()
    started.RequireSuccess(t)
    requireSearchCorpus(t, fixture, expected)
}
```

Add the same test for native split. Create, edit, delete, change metadata, and move a subtree while replacement is paused. Require current nodes only and no foreign-scope result.

- [ ] **Step 2: Record the deferred failure contract.**

Task 13 runs `^TestSearch(Rebuild|Split)DuringChanges$` against the completed branch. The tests must fail when replacement state, journal catch-up, validation, or atomic alias switching is broken. Do not start live dependencies during this coding task.

- [ ] **Step 3: Persist one replacement state machine.**

Acquire one environment lease before target creation. Persist mode, source, target, journal boundary, shard counts, replica count, scan cursor, replay cursor, and state. Use explicit states `creating`, `copying`, `replaying`, `switching`, `retiring`, `complete`, and `failed`. Inspect OpenSearch plus FDB state before every transition.

- [ ] **Step 4: Implement full replacement.**

Choose full replacement for first construction, restore, mapping or model changes, cleanup thresholds, lower shard counts, and targets outside the reserved routing path. Create an empty target. Scan bounded node IDs through `ScanSearch`. Schedule the same page worker against the target. Persist scan and journal replay checkpoints independently.

- [ ] **Step 5: Implement native primary-shard splitting.**

Permit split only when model, mapping, projection version, routing count, and document schema match and the target primary count is a larger permitted multiple. Pause claims, wait for leases, block source writes, call typed `Indices.Split`, clear the target block, wait for green, resume source claims, then replay the split boundary and later journal entries into the target. Existing documents must not run inference again.

```go
_, err := a.client.Indices.Split(ctx, opensearchapi.IndicesSplitReq{
    Index: source, Target: target,
    Body: strings.NewReader(fmt.Sprintf(`{"settings":{"index.number_of_shards":%d,"index.number_of_replicas":0}}`, primaryShards)),
})
```

- [ ] **Step 6: Recover a failed split.**

Keep the alias on the source. Clear its write block. Resume claims. Record the failed target for resumable deletion. A retry reads persisted state, source block, target health, and alias membership before choosing the next operation.

- [ ] **Step 7: Catch up and switch one alias atomically.**

Pause new claims in durable `switching`, record a final journal boundary, and finish the target through that boundary. Refresh and require green health. Submit one typed alias request that removes the old target and adds the new target. If the HTTP result is uncertain, read the alias. Old means retry; new means finish FDB handoff; any third target is a coordination error.

```json
{"actions":[{"remove":{"index":"old-physical-index","alias":"node-pages"}},{"add":{"index":"new-physical-index","alias":"node-pages","is_write_index":true}}]}
```

- [ ] **Step 8: Retire old indexes after sessions finish.**

Bind sessions to physical indexes. Reject new sessions on retiring indexes. Keep old workers and sessions on the old target. After the two-hour absolute deadline and bounded session cleanup prove zero active sessions, block writes, revoke write permissions, disable automatic recreation, and delete the index through typed APIs.

- [ ] **Step 9: Add restore and repeated-split coverage.**

Restore a real FDB backup into a disposable environment, change the search generation, reject restored cursors, and rebuild an empty index. Split one to two, four, then eight primaries with the model undeployed. Require identical stored source and sparse weights, then redeploy and rerun relevance and continuation.

- [ ] **Step 10: Run the serial coding checks.**

Run: `go test ./internal/test/integration -run '^$' -count=1`

Run: `make check`

Expected: PASS after compiling the integration package without executing its tests. Task 13 runs rebuild, split, restore, and injected failures.

- [ ] **Step 11: Commit the task.**

```sh
git add internal/domain/search/rebuild.go internal/adapters/foundationdb/search_rebuild.go internal/service/search_rebuild.go internal/adapters/search/opensearch_alias.go internal/adapters/search/opensearch_split.go internal/ops/search_reindex.go internal/test/integration/search_rebuild_test.go internal/test/integration/search_split_test.go internal/test/integration/search_restore_test.go
git commit -S -m "Replace OpenSearch indexes with durable catch-up and alias recovery" -m "Co-authored-by: Codex <noreply@openai.com>"
```
