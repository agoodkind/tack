# Durable OpenSearch Index Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert every source mutation into complete, bounded, retry-safe OpenSearch page documents through one production-connected slice.

**Architecture:** FoundationDB records desired search work in each source transaction. Runtime workers read one revision-bound page at a time, write it with external versioning, checkpoint progress, and yield after bounded work. The content reader uses explicit projection metadata. The permission policy produces opaque access keys. Tack never tokenizes text or interprets permission types.

**Tech Stack:** Go, FoundationDB, official OpenSearch Go client v4.7.3.

**Spec:** [Searchable content](../specs/2026-09-19-search-design.md#searchable-content), [durable indexing](../specs/2026-09-19-search-design.md#durable-indexing-and-bounded-work), and [permission expansion](../specs/2026-09-19-search-acceptance.md#permission-expansion).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Complete the reader, work store, writer, worker, scheduler, configuration, and runtime registration in one task. No declaration may exist only for a later task.

## Review Focus

Test complete text, UTF-8 boundaries, edits between page reads, failed source transactions, restarts, delayed writes, access-only updates, deletions, partial bulk failures, work-class fairness, and constant memory.

---

### Task 1: Build and connect the complete index pipeline

**Files:**

- Create: `internal/domain/node/content.go`
- Create: `internal/domain/node/search_projection_emit.go`
- Create: `internal/domain/search/work.go`
- Create: `internal/domain/search/writer.go`
- Create: `internal/searchaccess/access.go`
- Create: `internal/adapters/foundationdb/node_content.go`
- Create: `internal/adapters/foundationdb/node_content_cursor.go`
- Create: `internal/adapters/foundationdb/search_revision.go`
- Create: `internal/adapters/foundationdb/search_access_state.go`
- Create: `internal/adapters/foundationdb/search_work.go`
- Create: `internal/adapters/foundationdb/search_schedule.go`
- Create: `internal/adapters/foundationdb/search_scan.go`
- Create: `internal/adapters/search/opensearch_pages.go`
- Create: `internal/adapters/search/opensearch_bulk.go`
- Create: `internal/service/search_worker.go`
- Create: `internal/service/search_cleanup.go`
- Modify: `internal/domain/node/types.go`
- Modify: `internal/domain/node/reader.go`
- Modify: `internal/adapters/foundationdb/keys.go`
- Modify: `internal/adapters/foundationdb/node.go`
- Modify: `internal/adapters/foundationdb/node_delete.go`
- Modify: `internal/adapters/foundationdb/relationship.go`
- Modify: `internal/adapters/foundationdb/property.go`
- Modify: `internal/adapters/foundationdb/node_type.go`
- Modify: `internal/config/config.go`
- Modify: `internal/runtime/graph.go`
- Test: `internal/test/integration/search_reader_test.go`
- Test: `internal/test/integration/search_work_test.go`
- Test: `internal/test/integration/search_recovery_test.go`
- Test: `internal/test/integration/search_fairness_test.go`
- Test: `internal/test/integration/search_runtime_test.go`

**Core interfaces:**

```go
type ContentRequest struct { NodeID uuid.UUID; Cursor, ProjectionConfig string; AccessVersions []string; MaxBytes int; SearchGeneration int64 }
type ContentPage struct { NodeID uuid.UUID; NodeType, Revision, ProjectionVersion, Name, Text, NextCursor string; Access SearchAccess; Ordinal uint64; OverlapBytes int; Done bool }
type ContentReader interface { Content(context.Context, ContentRequest) (ContentPage, error); ScanSearch(context.Context, string, int) (SearchScan, error) }
type IndexAccessPolicy interface { Supports(string) bool; ResourceAuthority(context.Context, uuid.UUID) (uuid.UUID, error); Index(context.Context, IndexAccessRequest) (node.SearchAccess, error) }
type WorkStore interface { Claim(context.Context, WorkClass, string, time.Duration) (Work, error); Register(context.Context, Work, node.ContentPage) (WriteIntent, error); CompletePage(context.Context, WriteIntent, string, bool) error; Yield(context.Context, Work) error; Release(context.Context, Work, string) error }
type PageWriter interface { Put(context.Context, WriteIntent) error; UpdateAccess(context.Context, AccessIntent) error; Refresh(context.Context, string) error; Retire(context.Context, []WriteIntent) error }
```

- [ ] **Establish the failing public-boundary tests.** Add tests for multi-page Unicode text, complete unique text after removing overlap, revision changes, durable restart, stale owners, delayed content and access writes, retirement, partial bulk failure, and fairness. The tests use real FoundationDB and OpenSearch through production stores and adapters.

- [ ] **Emit text only from stored projection declarations.** Implement `EmitSearchText` with the node name first. Sort included definitions by `SearchProjection.Order`, then definition UUID. Decode scalar, array, declared object fields, and sorted object values recursively. Emit one newline per leaf. Reject missing declarations, malformed values, wrong shapes, and undeclared labels. Never use property names, property types, seeds, or `Indexed` to choose content.

- [ ] **Return one stable UTF-8 page per read.** Enforce at most 4,096 bytes and reserve at most one quarter for overlap. Encode node ID, revision, projection epoch, projection configuration hash, pagination version, byte bound, next unique offset, and ordinal in every cursor. Validate the cursor and current revision in one FoundationDB transaction. Return `ErrContentChanged` after an edit and `ErrNotFound` after deletion. Every nonfinal cursor advances.

- [ ] **Keep the storage interface ready for multipart nodes.** The worker calls `Content` repeatedly until `Done`. Production may return one page today or several pages at any time. No caller assumes one page, a total byte count, or that the complete node fits in one FoundationDB value or in worker memory.

- [ ] **Compile indexed access as opaque values.** Register `org-scope-v1` through `PolicySet`. `Index` returns sorted versions and keys for a resource. `EncodeKey` length-prefixes the version and byte parts, hashes them with SHA-256, and returns `version + ":" + base64url(hash)`. Search code never decodes a key. A later policy registers another compiler without changing documents, mapping fields, or work records.

- [ ] **Store bounded work in source transactions.** Add desired generation, event, claim, cursor, error, issued-ID, scan, and class-age keys to the central catalog. Stable hash buckets distribute organizations and nodes. Node create, edit, ancestry change, and delete schedule content work in the caller's transaction. Resource visibility changes schedule access work. Principal membership changes schedule no document work. Metadata and policy changes schedule bounded scans.

- [ ] **Claim work safely after failures.** Verify desired generation, owner, lease, revision, projection version, access-state generation, write versions, and target index when registering each page. Return `ErrWorkChanged` for stale state. Keep live, access, cleanup, rescan, and rebuild work in separate classes. Remove obsolete claims, cursors, errors, events, and issued IDs after convergence.

- [ ] **Write deterministic page documents.** Derive the document ID from organization, node, revision, projection version, and page ordinal with length-prefixed bytes and base64url encoding. Copy opaque access values without interpretation. Reject invalid ordering, duplicates, empty access values, generation mismatches, and text above 4,096 bytes.

- [ ] **Use bounded typed bulk requests.** Encode NDJSON before admission. Flush before 500 documents or 5 MiB. Use the official typed bulk API. Set every index, update, and retirement action to the FoundationDB generation with `version_type:external_gte`. Inspect every item. Checkpoint only the contiguous successful prefix. Convert lower-generation conflicts to `ErrObsoleteWrite`. Never enable automatic index creation.

- [ ] **Process bounded worker slices.** Stop before another remote call after 32 pages, 5 MiB, or two seconds. Allow one admitted request to use its ten-second timeout. Require a successful write and checkpoint before reading the next page. Yield unfinished work. Refresh in a resumable phase after the final page.

- [ ] **Update access without embedding text.** Read at most 100 issued document IDs. Submit partial updates containing only `search_generation` and `access`. Do not call `Content`, send text, or invoke the model. Undeploy the model in the integration test and require access updates to preserve source text, chunks, and sparse weights byte for byte.

- [ ] **Retire replaced and deleted pages.** Replace each issued document with a text-free `retired:true` record at the current generation. Process at most 100 IDs per cleanup slice. Keep retirement records until the physical index is deleted. Require every delayed older write to conflict.

- [ ] **Register production workers.** Add explicit page-size, lease, timeout, concurrency, and class scheduling configuration. Construct the reader, policy set, work store, writer, cleanup service, and worker loops in `internal/runtime/graph.go`. Recover every worker goroutine, return startup errors, use the injected clock, and stop all loops on context cancellation. Source writes remain available when OpenSearch is unavailable because failed work remains pending.

- [ ] **Prove scale behavior.** Run live, access, cleanup, rescan, and rebuild work together. Require every class to progress. Record page reads, encoded bytes, slice duration, oldest work age, and peak memory. Increase workers under fixed load and require throughput to increase without a stored-format change.

- [ ] **Run and commit the slice.** Run the focused integration tests. Run `make build` once and fix every failure. Review `git diff --check` and the complete diff. Commit all files together because the production entry point depends on the complete slice:

```sh
git add internal
git commit -S -m "Add the durable OpenSearch index pipeline" -m "Co-authored-by: Codex <noreply@openai.com>"
```

The final validation plan repeats these tests with 128 KiB, 1 MiB, 8 MiB, over 100 MB, and larger-than-worker-memory source nodes after TACK-524 and TACK-525 implement multipart FoundationDB reads.
