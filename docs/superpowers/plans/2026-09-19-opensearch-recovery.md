# Search Runtime and Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Assemble the replacement runtime and replace search indexes without losing committed changes.

**Architecture:** Runtime workers use the durable work store. A full replacement scans FoundationDB when indexed contents must change. A primary-shard-only replacement uses native OpenSearch splitting. Both paths replay changes and use the same recoverable alias switch. Restored source data always requires a full replacement.

**Tech Stack:** Go, FoundationDB, official OpenSearch Go client v4.7.3, typed split and alias APIs, existing audited operations, and datagen.

**Spec:** [Durable work](../specs/2026-09-19-search-acceptance.md#durable-work-and-state-lifecycle) and [index replacement](../specs/2026-09-19-search-acceptance.md#index-replacement-lifecycle).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Preserve the operations audit entry point. Search outages must not discard committed node changes. Delete Meilisearch completely. Do not migrate its index, adapt its behavior, use its data for validation, or preserve a fallback.

## Review Focus

Test startup during an engine outage, failure before alias switching, failure after alias switching, concurrent metadata edits, and rebuilding restored source data.

---

## Task 8: Replace runtime assembly and preserve public errors

Create:

```text
internal/runtime/search.go                         worker lifecycle
internal/config/search.go                          official client conversion and validation
internal/test/integration/search_runtime_test.go   restart and outage behavior
```

Modify [runtime graph](../../../internal/runtime/graph.go), [configuration](../../../internal/config/config.go), [NodeService](../../../internal/service/node.go), node creation, MCP dependency assembly, Compose, testenv, datagen, operator commands, recovery documentation, and existing search integration fixtures. Delete the Meilisearch module dependency, adapter files, NodeDoc conversion, synchronous best-effort writes, no-op fallback, service, volume, environment variables, credentials, test helper, and runbook. Preserve unrelated node operations and list pagination.

Interfaces consumed: `search.WorkStore`, `service.Worker`, `search.Ranker`, and `search.SessionStore`. Produce:

```go
type searchRuntime struct { cancel context.CancelFunc; done chan struct{} }
func buildSearchRuntime(ctx context.Context, cfg *config.Config, stores *foundationdb.Stores) (searchRuntime, search.Ranker, error)
func (r searchRuntime) Close()
```

- [ ] Add `TestSearchRuntimeUnavailable`: build the application against a correctly configured but stopped local engine, commit a node mutation through MCP, and require search to return an explicit error. Restart the engine and require automatic indexing. Assert the mutation remained committed during the outage.
- [ ] Run `^TestSearchRuntimeUnavailable$` and record the pre-change failure.
- [ ] Add required fields to the central `config.Config`: `OPENSEARCH_URLS`, `OPENSEARCH_USERNAME`, `OPENSEARCH_PASSWORD`, `OPENSEARCH_CA_FILE`, `SEARCH_PAGE_BYTES`, and `SEARCH_QUERY_BYTES`; add positive bounded worker-concurrency and timeout fields. Make `internal/config/search.go` validate those fields and return `opensearch.Config`. Do not add another environment parser, root configuration object, default path, or secret log.
- [ ] Remove `MEILI_URL`, `MEILI_MASTER_KEY`, the Meilisearch Compose service and
  volume, its application dependency, the testenv subcommand and helper, datagen
  guards and checks, module entries, operator paths, and recovery instructions.
  Run `go mod tidy`. The application must build and start with OpenSearch
  configuration only.
- [ ] Provision one empty versioned OpenSearch index. Run the audited rebuild from
  FoundationDB before search becomes ready. Never query, export, copy, translate,
  attach, or inspect the existing Meilisearch index or volume. Do not use its schema,
  settings, synonyms, ranking rules, documents, or results as fixtures or validation.
- [ ] Construct the official client independently of engine readiness. Invalid configuration fails startup. An unavailable engine leaves durable work pending and makes search return `ErrUnavailable`. Model and index provisioning belong to the audited operator path, not every application startup.
- [ ] Start bounded claim loops with explicit worker limits for live mutations,
  cleanup, metadata rescans, and rebuild work. Each worker calls one bounded
  `Worker.RunOne` slice and records errors before capped, context-aware backoff.
  Cleanup marks an idle-expired or absolute-expired session closing, persists its
  deletion cursor, and removes PIT, token, visited-node, and replay records in
  bounded batches. Renewal must conflict with closing. Resume cleanup after restart,
  tolerate absent PITs, and delete the header last.

```go
func (r searchRuntime) Close() {
    r.cancel()
    <-r.done
}
```

- [ ] Extend Graph.Close to close search before its databases. Remove the old searcher parameter from NodeService after every mutation schedules through FDB. Replace remaining Noop-backed tests with real engine fixtures when they exercise search; unrelated tests must not require a fabricated search dependency.
- [ ] Inspect the complete Tack and configs diffs for remaining live Meilisearch
  services, secrets, endpoints, dependencies, volumes, and recovery paths. Delete
  Meilisearch in the same review and commit that enables the OpenSearch runtime.
- [ ] Run all integration tests, `make build`, and `make check`; commit with subject `Remove Meilisearch and enable durable OpenSearch workers`.

## Task 9: Build and switch a replacement index

Create:

```text
internal/domain/search/rebuild.go                   persistent rebuild state
internal/adapters/foundationdb/search_rebuild.go    scan and journal checkpoints
internal/service/search_rebuild.go                 rebuild coordinator
internal/adapters/search/opensearch_alias.go        physical-index switch
internal/adapters/search/opensearch_split.go        native primary-shard increase
internal/test/integration/search_rebuild_test.go    concurrent mutation and failure
internal/test/integration/search_split_test.go      split, replay, and recovery
internal/test/integration/search_restore_test.go    real backup and restore
```

Replace the body of [search reindexing](../../../internal/ops/search_reindex.go). Keep its existing audited command registration and dry-run behavior. Consume `NodeReader.ScanSearch`, the same content reader, worker, and page writer used for live changes. Produce:

```go
type ReplacementMode uint8
const (
    ReplacementFull ReplacementMode = iota + 1
    ReplacementSplit
)
type Rebuild struct {
    ID, SourceIndex, TargetIndex, ScanCursor string
    Boundary, Applied []byte
    State string
    Mode ReplacementMode
    PrimaryShards, RoutingShards int
}
type Rebuilder interface { Run(context.Context) error }
func (a *Adapter) SplitIndex(ctx context.Context, source, target string, primaryShards int) error
func (a *Adapter) SwitchAlias(ctx context.Context, alias, oldIndex, newIndex string) error
```

- [ ] Add `TestSearchRebuildDuringChanges` and `TestSearchSplitDuringChanges`. Run the real audited command while MCP creates, edits, deletes, changes metadata, and moves a subtree. Search must reflect each completed change and must not return deleted or foreign-scope nodes.
- [ ] Run `^TestSearch(Rebuild|Split)DuringChanges$`; expect the old reindex command to lack a replacement index and persisted handoff.
- [ ] Acquire one environment-wide rebuild lease before creating an index. Reject or
  queue another rebuild until the first replacement and any prior retiring index
  finish. Persist the mode, source, target, journal boundary, positive primary count,
  and reserved routing count. Require disk for the serving, replacement, and retiring
  indexes before either mode starts.
- [ ] Use `ReplacementFull` for the first index, restore, model or mapping changes,
  cleanup thresholds, lower shard counts, and targets outside the reserved routing
  path. Create one empty target, scan through the bounded page worker, and persist
  scan and replay checkpoints independently.
- [ ] Use `ReplacementSplit` only when the model, mapping, projection, and routing
  count match and the requested primary count is a larger permitted multiple. Record
  the split boundary. Pause new claims, wait for existing claims to finish or expire,
  set `index.blocks.write`, and keep the source alias readable. FoundationDB writes
  continue and append durable work.
- [ ] Call `opensearchapi.Client.Indices.Split` with the persisted physical names and
  target primary count. Set zero replicas during construction and clear the target
  write block. Do not call Reindex, scan FoundationDB, infer existing documents, or
  implement the HTTP endpoint. Wait for a green target, restore source writes, resume
  live source claims, and replay the split-start backlog plus later journal entries
  into the target. Restore the recorded replica count and require green health before
  alias switching.
- [ ] If splitting fails, keep the alias on the source, restore source writes, resume
  claims, and delete the failed target through existing resumable cleanup. Recovery
  must inspect the persisted state, write block, target, and alias before retrying.
- [ ] Start a full replacement when the serving index crosses its retired-page,
  retirement-age, or physical-byte limit because splitting would preserve obsolete
  documents. Insufficient disk leaves new index work pending in FoundationDB.
- [ ] Replay mutations after the boundary, including metadata scans and deletions. Enter a durable `switching` state that pauses new worker claims but still permits source mutations to enqueue. Record a final journal boundary and finish the new index through that boundary. Verify refresh and index health before changing the alias.
- [ ] Change the alias in one OpenSearch request:

```json
{"actions":[
  {"remove":{"index":"old-physical-index","alias":"node-pages"}},
  {"add":{"index":"new-physical-index","alias":"node-pages"}}
]}
```

The adapter substitutes the concrete persisted index names and calls the typed `Aliases` API. Set an explicit write index when required by the alias configuration. Use typed `Indices.Get`, alias get, refresh, health, block, statistics, and delete operations throughout rebuild and retirement.

- [ ] After the alias request, persist the new worker target and resume claims. If the process stops before that FDB commit, recovery reads the alias: the old target means retry or cancel the switch; the new target means finish the FDB handoff. A third target is an explicit coordination error. Never assume an HTTP timeout means the alias operation failed.
- [ ] Keep outstanding old workers bound to the old physical index. Mark that index
  retiring after switching. `SessionStore.Create` must reject retiring indexes and
  retry against the current alias after closing its unused PIT. Maintain per-index
  session keys. The two-hour absolute session deadline guarantees retirement can
  finish. Bounded cleanup must prove no active session remains before audited index
  deletion. Block writes, revoke write permissions, disable automatic recreation,
  and delete obsolete retirement records with the index.
- [ ] At every checkpoint, require no more than one serving, one replacement, and
  one retiring index. Delete failed replacements through resumable cleanup before
  another rebuild starts. Release rebuild journal entries after no active rebuild
  boundary needs them.
- [ ] Add a scale-out case that starts with a normal one-member cluster, joins two
  data and ML members through the existing member, changes replicas from zero to
  one, records that count with the generation, and splits from one to two, four, then
  eight primaries. Require unchanged sparse weights after replica allocation and each
  split. Never repeat initial cluster bootstrap. Undeploy the document model before
  each split.
  Require identical source bytes, generated embeddings, and saved raw-sparse query
  results. Rerun relevance and continuation, then require improved throughput.
- [ ] Fail real scan, inference, split, replay, and alias operations separately. The
  serving alias must remain correct. Stop immediately after switching and require
  restart to complete the handoff. A dry run creates no index or journal state.
- [ ] Restore a real FDB backup into a disposable local environment using the existing backup/restore operations. Change the search generation before accepting requests; bind sessions to that generation and reject restored cursors. Rebuild an empty search index and require current nodes, deleted-node absence, relevance, and final-page text. Never reuse a pre-restore index as authoritative.
- [ ] Reuse existing lifecycle cancellation, bounded close, context-aware backoff, telemetry, logger, and FoundationDB retry conventions. Native split creates index bytes only. It cannot replay FoundationDB changes, preserve sessions, or coordinate the alias, so keep the application replacement state.
- [ ] Run `^TestSearch(Rebuild|Split|Restore)` and `make check`; commit with subject `Replace OpenSearch indexes with durable catch-up and alias recovery`.

## Task 10: Add public QA generator coverage

Modify [search generator checks](../../../internal/datagen/generate_search_checks.go) and add focused files for opaque metadata fixtures, multi-page checks, and ranked continuation. Use the generic argument map from the MCP task instead of seeded workspace/project/issue argument keys.

Produce `datagen.VerifySearch(ctx context.Context, cfg *config.Config) error` and invoke it through the existing guarded `ops qa datagen` path. It creates its own minimal opaque definitions and authentication through real repositories and invokes public MCP operations for node changes and searches.

- [ ] Add an integration test that runs the real generator operation with `TACK_DATAGEN_ALLOW_TARGET=local` and the smaller reader budget. Require it to exercise semantic relevance, final-page search, a shorter edit, deletion, and distinct continuation.
- [ ] Run `^TestSearchDatagen$` and require failure before the new checks exist.
- [ ] Implement checks with a 10-second deadline for a newly created small node. Record expected node IDs and ranks; return an error for a missing expected result, duplicate continuation result, obsolete lexical match, or unexpected successful query during an engine outage. Keep test phrases as fixture values, never type-dispatch rules.
- [ ] Run the generator test and the full integration suite. Verify the production target guard still rejects generator writes. Run `make check`; commit with subject `Exercise paginated semantic search in QA datagen`.
