# OpenSearch implementation plan

The native sparse indexing and ranking configuration passed local engine validation. The implementation tasks must repeat that behavior through Tack's public boundaries.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Search every accepted node through a paginated reader and return authorized, distinct nodes ranked by OpenSearch.

**Architecture:** The reader returns one bounded part per call. Bounded worker slices index and retire parts. OpenSearch performs text splitting, local sparse encoding, and inverted-index ranking. Durable sessions continue across every ranked page match.

**Tech Stack:** Go, FoundationDB, OpenSearch 3.8.0, `github.com/opensearch-project/opensearch-go/v4` v4.7.3, ML Commons, Docker SDK, MCP, Ansible, OpenTofu.

**Spec:** [Search architecture](../specs/2026-09-19-search-design.md). The [acceptance criteria](../specs/2026-09-19-search-acceptance.md) define release evidence.

**Validation record:** [OpenSearch prototypes and experiments](2026-09-19-opensearch-validation.md).

## Global Constraints

- Multi-page behavior must pass acceptance before the first search release.
- Delete the complete Meilisearch stack, including its client, adapter, runtime
  configuration, test environment, container, volume, credentials, and runbook.
- Do not migrate the Meilisearch index, write to both engines, preserve a fallback,
  retain a compatibility layer, or keep Meilisearch deployment resources.
- Create the first OpenSearch index empty and rebuild it only from FoundationDB.
- Tack neither loads a tokenizer nor counts model tokens. OpenSearch remains unmodified.
- Custom plugins, forks, and external inference are excluded.
- Node types, property types, and property names are opaque identifiers.
- Every applicable property definition explicitly includes or excludes search.
  Do not infer that decision from identifiers, types, or the FDB `Indexed` flag.
- The container image is `opensearchproject/opensearch:3.8.0`.
- Use `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0 and nested `rank_features`.
- Reader parts contain at most 4,096 UTF-8 bytes; queries contain at most 126 UTF-8 bytes.
- Map page text as a native `semantic` field. OpenSearch performs fixed-character chunking, sparse encoding, pruning, and point-in-time pagination.
- Pin the latest stable Go client, v4.7.3. Task 1 must keep its core API compatibility test against the exact OpenSearch 3.8.0 image because the client documents later 3.x releases as best effort.
- Use typed client APIs for core index creation and split, bulk, point-in-time, alias, document, health, block, and statistics operations. Build searches with the typed request API and decode the few response fields the typed response omits with `opensearch.Do`. Define narrow request types for ML Commons operations that stable v4 does not include, then use `opensearch.Do` and `opensearch.ParseError`. Do not add a generic method-and-path JSON API or import the temporary v5 preview package.
- Bulk requests contain at most 500 page documents and 5 MiB of encoded data, including action lines.
- A result page has at most 25 nodes within Tack's response-byte budget.
- Continuation can reach every matching node. Engine batches contain at most 100 matches; responses scan at most four batches.
- Configure one environment search endpoint in the official client. Its connection
  pool owns retries, TLS, and transport metrics. The hypervisor proxy owns backend
  health and selection. OpenSearch owns shard and ML worker selection.
- QA and production start with three LXC guests, with at least 8 GiB memory, 2 CPU cores, 40 GiB storage, and a 2 GiB JVM heap per guest. Three is the release topology, not a capacity ceiling.
- The initial QA host requires at least 64 GB installed memory and 12 logical CPUs. Full scale-out acceptance on the same host requires at least 96 GB installed memory, 16 logical CPUs, and four 40 GiB fast disks. Current suburban hardware cannot satisfy either profile.
- Adding Tack processes, FoundationDB capacity, OpenSearch ML nodes, data nodes, replicas, or coordinating endpoints must not require application code or stored-format changes. A higher primary-shard count uses native splitting along its reserved routing path and a full replacement otherwise.
- Persist all search sessions and work in FoundationDB. Distribute their keys across stable hash buckets. Do not require sticky requests, a process-local cache, a global sequence, or one claim range.
- Worker claims, cleanup, sessions, rebuilds, and physical indexes have explicit work and lifetime bounds.
- Each physical index stores primary and reserved routing-shard counts. Every shard increase creates and validates a replacement through native splitting or a full FoundationDB rebuild.
- All product state and search progress use FoundationDB. SQL remains authentication and audit only.
- Reads use `NodeReader`. Configuration uses environment variables through `caarlos0/env`.
- Tests use real dependencies and public boundaries. No mocks, product seeds, or production tokenizer dependency establish acceptance.
- Keep each new or edited file within 200 lines. Use focused files instead of adding to an oversized file.
- Run `make check` before each signed commit. Include `Co-authored-by: Codex <noreply@openai.com>`.
- This plan does not authorize a push, merge, deployment, ruleset change, or storage-limit removal.

## Reuse boundaries

- Extend the existing FoundationDB key catalog, transaction loops, tuple encoding, retry conventions, telemetry, logger, cancellation, wait groups, and bounded shutdown. Do not create search copies.
- Add search fields to `config.Config`. Convert those fields to `opensearch.Config` in focused validation code. Do not add another environment parser, root configuration object, or default path.
- Refactor `Driver.Call` and `Driver.CallRaw` through one internal call path. Do not duplicate context checks, request IDs, dry-run behavior, JSON-RPC framing, sending, or decoding.
- Register provision and verification operations through the existing `clispec.Operation` and `RegisterCommands` path. Do not add legacy command registration.
- Reuse the existing membership middleware, scope resolver, typed node resolver, and membership checks. Search must not introduce another authorization query, cache, context value, or marker.
- Reuse MCP response byte limits and rendering, FoundationDB telemetry, and OpenSearch client metrics. Do not create search-specific truncation or metric registries.
- Register the one-time metadata migration through the existing `clispec` lifetime,
  dry-run, audit, and build expiration machinery. Do not add another migration CLI.
- Keep search claims, sessions, content cursors, HMAC cursors, rebuild coordination, visited node IDs, and one query embedding per session. OpenSearch does not provide those application guarantees.

## Review Focus

1. An edit between content reads must produce an explicit revision error, never a mixed document. Reader and worker tasks test it.
2. A paused old writer must not restore deleted text after another worker completes cleanup. Worker tasks test actual delayed requests.
3. A long uninterrupted Unicode string must not disappear inside model truncation. Native coverage tests inspect the semantic field's generated chunks and embeddings.
4. A byte-limited response must retain the first result it cannot render. Query tasks test continuation with large names.
5. A metadata or ancestry change during an index replacement must appear after the alias switch. Recovery tasks test concurrent public changes.
6. A large node, cleanup, or rebuild must yield before it starves live mutation work. Worker and capacity tasks measure every work class.
7. Session and rebuild cleanup must bound the number and lifetime of retained physical indexes.
8. Adding each capacity role must increase its measured throughput without changing application code or session behavior.

---

## Execution order

Each linked task document specifies its files, interfaces, tests, and commit boundary.
These are parts of one implementation. None introduces a temporary search design.
Tasks 11 in Tack and configs add inactive OpenSearch components. They do not change
the active application search path. Tasks 7 and 8 perform the only application
cutover. That review enables OpenSearch and deletes Meilisearch together. No task
implements dual writes, a compatibility layer, or an interim search engine.
The [fixture code](2026-09-19-opensearch-fixtures.md) supplies real-store setup and
authenticated calls for the owning tasks.

1. Complete the [native coverage task](2026-09-19-opensearch-native.md). Implement the validated sparse engine configuration and its regression tests.
2. Complete the [projection rollout task](2026-09-19-opensearch-metadata.md). Add explicit declarations for new metadata and the expiring manifest backfill for existing definitions.
3. Implement Task 2 in the [reader tasks](2026-09-19-opensearch-reader.md), including revision identity, bounded pages, and summaries.
4. Implement the [durable indexing tasks](2026-09-19-opensearch-worker.md), including transaction scheduling, retries, and deletion.
5. Complete Task 6 in the [query tasks](2026-09-19-opensearch-query.md). It adds ranking and continuation behind internal boundaries.
6. Complete the [recovery tasks](2026-09-19-opensearch-recovery.md), including full rebuild, native split, restore, and QA generator coverage.
7. Complete Task 11 in the [deployment tasks](2026-09-19-opensearch-deployment.md). It prepares role-specific services and configuration without deploying or changing application search.
8. Complete Tasks 7 and 8 in the query plan as one review and commit. This is the sole application cutover. Complete Task 3's metadata refresh test against this runtime.
9. Complete Task 10's public QA checks, then apply Task 12 only after deployment authorization.

## Delivery tickets

| Plan scope | Ticket |
| --- | --- |
| Task 1: Native sparse indexing | TACK-530 |
| Explicit projection rollout and backfill | TACK-542 |
| Task 2: Paginated node content reads | TACK-531 |
| Task 3: Metadata refresh | TACK-532 |
| Task 4: Durable search work | TACK-533 |
| Task 5: Bounded indexing and retirement | TACK-534 |
| Task 6: Ranked continuation | TACK-535 |
| Tasks 7 and 8: Authorized public search, runtime, and Meilisearch removal | TACK-536 |
| Task 9: Rebuild and restore | TACK-537 |
| Task 10: QA datagen coverage | TACK-538 |
| Task 11, Tack: Containers and provisioning operations | TACK-539 |
| Task 11, configs: Six search guests and rendered configuration | TACK-540 |
| Task 12: QA, production, and Meilisearch deployment removal | TACK-541 |

TACK-518, TACK-519, and TACK-520 retain the cross-cutting scalability,
isolation, and semantic acceptance. TACK-524 and TACK-525 remain separate
storage work.

The native task repeats the successful engine tests through the production adapter. Preserve reproductions of any regression. Do not substitute truncation, a Tack tokenizer, an OpenSearch modification, or an external service.

## File responsibilities

| Responsibility | Change location |
| --- | --- |
| Reader contracts and metadata representation | Extend [NodeReader](../../../internal/domain/node/reader.go); create the domain content and projection files specified in the reader tasks. |
| Transactional scheduling and revision identity | Extend the node, relationship, and metadata stores; add dedicated search storage files. |
| Page indexing and native model setup | Add a focused official-client adapter, native semantic mapping, typed bulk and search operations, and concrete ML Commons requests; delete all Meilisearch adapter code and its module dependency. |
| Worker ownership and recovery | Add search worker, cleanup, and rebuild files under the existing service and FDB adapter packages. |
| Authentication and rendered results | Replace [MCP search](../../../internal/adapters/mcp/tools/search.go); reuse response-byte enforcement. |
| Runtime and operator entry points | Update [graph assembly](../../../internal/runtime/graph.go) and [search reindexing](../../../internal/ops/search_reindex.go). |
| Local proof and QA coverage | Replace the Meilisearch test environment and checks with real OpenSearch integration and datagen coverage. |
| Containers and environment configuration | Delete Meilisearch services, volumes, variables, and secrets from [Compose](../../../docker-compose.yml) and configs; prepare LXC, TLS, inventory, and Ansible changes for OpenSearch. |

## Commands and commit procedure

Run these commands inside the Tack checkout. The integration runner supplies the
FoundationDB client library and real engine connectivity.

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner build tests
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch' ./internal/test/integration
make check
```

Each task first adds its test and runs the matching `-run` expression. A missing
new API must fail compilation; a present but incorrect implementation must fail
the specified assertion. Record the actual failure before implementing the task.
After implementation, require the matching test to pass without skips.

Stage only the files changed by that task. The native task's commit command is:

```sh
git commit -S -m "Add native OpenSearch embedding coverage validation" -m "Co-authored-by: Codex <noreply@openai.com>"
```

Use each subsequent task's specified subject with that same trailer. Before any
later push, fetch and verify every signature
and raw `gpgsig` header in `origin/main..HEAD`.

## Storage expansion boundary

The current node storage model remains unchanged. Tests use smaller byte bounds
on the real reader to exercise successive parts within current storage limits.
Search depends only on the reader contract, including explicit completion.

TACK-524 and TACK-525 implement storage changes separately. Their acceptance must rerun this search suite with 128 KiB, 1 MiB, 8 MiB, over 100 MB, and nodes larger than worker memory. They replace the reader's storage implementation. They must not alter the worker loop, mapping, page IDs, stable search endpoint, or result grouping.
