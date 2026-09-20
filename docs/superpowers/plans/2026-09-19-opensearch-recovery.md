# Search Runtime and Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Assemble the replacement runtime and rebuild search safely from FoundationDB.

**Architecture:** Runtime workers use the durable work store. Rebuilds scan into a fresh physical index, replay changes, and persist an alias-switch state that can recover after interruption. Restored source data always requires a fresh index.

**Tech Stack:** Go, FoundationDB, OpenSearch aliases, existing audited operations and datagen.

**Spec:** [Recovery requirements](../specs/2026-09-19-search-acceptance.md#durable-changes-and-recovery).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Preserve the operations audit entry point. Search outages must not discard committed node changes. No Meilisearch compatibility layer is required.

## Review Focus

Test startup during an engine outage, failure before alias switching, failure after alias switching, concurrent metadata edits, and rebuilding restored source data.

---

## Task 8: Replace runtime assembly and preserve public errors

Create:

```text
internal/runtime/search.go                         worker lifecycle
internal/config/search.go                          search environment validation
internal/test/integration/search_runtime_test.go   restart and outage behavior
```

Modify [runtime graph](../../../internal/runtime/graph.go), [configuration](../../../internal/config/config.go), [NodeService](../../../internal/service/node.go), node creation, MCP dependency assembly, and existing search integration fixtures. Remove the Meilisearch client dependency and adapter files, old NodeDoc conversion, synchronous best-effort search writes, and tests for removed behavior. Preserve unrelated node operations and list pagination.

Interfaces consumed: `search.WorkStore`, `service.Worker`, `search.Ranker`, and `search.SessionStore`. Produce:

```go
type searchRuntime struct { cancel context.CancelFunc; done chan struct{} }
func buildSearchRuntime(ctx context.Context, cfg *config.Config, stores *foundationdb.Stores) (searchRuntime, search.Ranker, error)
func (r searchRuntime) Close()
```

- [ ] Add `TestSearchRuntimeUnavailable`: build the application against a correctly configured but stopped local engine, commit a node mutation through MCP, and require search to return an explicit error. Restart the engine and require automatic indexing. Assert the mutation remained committed during the outage.
- [ ] Run `^TestSearchRuntimeUnavailable$` and record the pre-change failure.
- [ ] Add required environment fields `OPENSEARCH_URLS`, `OPENSEARCH_USERNAME`, `OPENSEARCH_PASSWORD`, `OPENSEARCH_CA_FILE`, `SEARCH_PAGE_BYTES`, and `SEARCH_QUERY_BYTES`; add positive bounded worker-concurrency and timeout fields. Require page bytes between 16 and 4,096 and query bytes between 1 and 126. Do not load runtime JSON/YAML configuration files or log secret values.
- [ ] Construct the verified-TLS client independently of engine readiness. Invalid configuration fails startup. An unavailable engine leaves durable work pending and makes search return ErrUnavailable. Model/index provisioning belongs to the audited operator path, not every application startup.
- [ ] Start bounded worker claim loops and expired-session cleanup. Each worker calls Worker.RunOne and records errors before capped, context-aware backoff. Cleanup atomically marks an expired header closing, persists its deletion cursor, and deletes PIT, visited-node, and replay records in bounded batches. Renewal must conflict with closing. Resume closing sessions after restart, tolerate already absent PITs, and delete the header last. Close cancels new work, waits for in-flight deadlines, releases ownership, and closes storage resources.

```go
func (r searchRuntime) Close() {
    r.cancel()
    <-r.done
}
```

- [ ] Extend Graph.Close to close search before its databases. Remove the old searcher parameter from NodeService after every mutation schedules through FDB. Replace remaining Noop-backed tests with real engine fixtures when they exercise search; unrelated tests must not require a fabricated search dependency.
- [ ] Run all integration tests, `make build`, and `make check`; commit with subject `Replace Meilisearch runtime with durable OpenSearch workers`.

## Task 9: Build and switch a replacement index

Create:

```text
internal/domain/search/rebuild.go                   persistent rebuild state
internal/adapters/foundationdb/search_rebuild.go    scan and journal checkpoints
internal/service/search_rebuild.go                 rebuild coordinator
internal/adapters/search/opensearch_alias.go        physical-index switch
internal/test/integration/search_rebuild_test.go    concurrent mutation and failure
internal/test/integration/search_restore_test.go    real backup and restore
```

Replace the body of [search reindexing](../../../internal/ops/search_reindex.go). Keep its existing audited command registration and dry-run behavior. Consume `NodeReader.ScanSearch`, the same content reader, worker, and page writer used for live changes. Produce:

```go
type Rebuild struct { ID, SourceIndex, TargetIndex, ScanCursor string; Boundary, Applied []byte; State string }
type Rebuilder interface { Run(context.Context) error }
func (c *OpenSearchClient) SwitchAlias(ctx context.Context, alias, oldIndex, newIndex string) error
```

- [ ] Add `TestSearchRebuildDuringChanges`: run the real audited reindex command while MCP creates, edits, deletes, changes metadata, and moves a subtree. Wait for the command's successful completion; search must reflect each completed change and must not return deleted or foreign-scope nodes.
- [ ] Run `^TestSearchRebuildDuringChanges$`; expect the old reindex command to lack a replacement index and persisted handoff.
- [ ] Record an FDB journal boundary before scanning. Retain journal entries needed by the oldest active rebuild. Create a unique physical index with the pinned configuration. Scan node IDs using ScanSearch and run the existing page worker against that target. Persist scan and replay checkpoints independently.
- [ ] Replay mutations after the boundary, including metadata scans and deletions. Enter a durable `switching` state that pauses new worker claims but still permits source mutations to enqueue. Record a final journal boundary and finish the new index through that boundary. Verify refresh and index health before changing the alias.
- [ ] Change the alias in one OpenSearch request:

```json
{"actions":[
  {"remove":{"index":"old-physical-index","alias":"node-pages"}},
  {"add":{"index":"new-physical-index","alias":"node-pages"}}
]}
```

The client substitutes the concrete persisted index names using typed request
fields. Set an explicit write index when required by the alias configuration.

- [ ] After the alias request, persist the new worker target and resume claims. If the process stops before that FDB commit, recovery reads the alias: the old target means retry or cancel the switch; the new target means finish the FDB handoff. A third target is an explicit coordination error. Never assume an HTTP timeout means the alias operation failed.
- [ ] Keep outstanding old workers bound to the old physical index. Mark that index retiring in FDB after switching; SessionStore.Create must reject retiring indexes atomically and retry against the current alias after closing its unused PIT. Maintain per-index session keys on creation, completion, and cleanup. A bounded scan must prove no unexpired, incomplete session remains before audited index deletion. Advancing sessions may retain the index indefinitely. Block writes, revoke write permissions, and disable automatic recreation before deletion. Rebuild and cleanup remain resumable.
- [ ] Fail real scan, embedding, and replay operations separately before switching and assert the serving alias is unchanged. Stop the process immediately after switching and assert restart completes the handoff. A dry run must neither create an index nor alter journal retention.
- [ ] Restore a real FDB backup into a disposable local environment using the existing backup/restore operations. Change the search generation before accepting requests; bind sessions to that generation and reject restored cursors. Rebuild an empty search index and require current nodes, deleted-node absence, relevance, and final-page text. Never reuse a pre-restore index as authoritative.
- [ ] Run `^TestSearch(Rebuild|Restore)` and `make check`; commit with subject `Rebuild OpenSearch with durable catch-up and alias recovery`.

## Task 10: Add public QA generator coverage

Modify [search generator checks](../../../internal/datagen/generate_search_checks.go) and add focused files for opaque metadata fixtures, multi-page checks, and ranked continuation. Use the generic argument map from the MCP task instead of seeded workspace/project/issue argument keys.

Produce `datagen.VerifySearch(ctx context.Context, cfg *config.Config) error` and invoke it through the existing guarded `ops qa datagen` path. It creates its own minimal opaque definitions and authentication through real repositories and invokes public MCP operations for node changes and searches.

- [ ] Add an integration test that runs the real generator operation with `TACK_DATAGEN_ALLOW_TARGET=local` and the smaller reader budget. Require it to exercise semantic relevance, final-page search, a shorter edit, deletion, and distinct continuation.
- [ ] Run `^TestSearchDatagen$` and require failure before the new checks exist.
- [ ] Implement checks with a 10-second deadline for a newly created small node. Record expected node IDs and ranks; return an error for a missing expected result, duplicate continuation result, obsolete lexical match, or unexpected successful query during an engine outage. Keep test phrases as fixture values, never type-dispatch rules.
- [ ] Run the generator test and the full integration suite. Verify the production target guard still rejects generator writes. Run `make check`; commit with subject `Exercise paginated semantic search in QA datagen`.
