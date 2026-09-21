# OpenSearch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

The native sparse indexing and ranking configuration passed local engine validation. The implementation tasks must repeat that behavior through Tack's public boundaries.

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
- Do not use Meilisearch documents, schema, settings, synonyms, ranking rules,
  results, or code as inputs to OpenSearch provisioning, fixtures, or validation.
- Create the first OpenSearch index empty and rebuild it only from FoundationDB.
- Tack neither loads a tokenizer nor counts model tokens. OpenSearch remains unmodified.
- Custom plugins, forks, and external inference are excluded.
- Node types, property types, and property names are opaque identifiers.
- Keep indexed access fields and OpenSearch candidate filters behind one permission boundary. The current implementation uses organization and scope. A future permission model must add a selective pre-ranking filter instead of relying on FoundationDB post-filtering.
- A future permission model may add versioned mapping fields and rebuild the index. It must not change page reads, durable work, session formats, ranked continuation, or result grouping. FoundationDB still authorizes every returned node.
- Every applicable property definition explicitly includes or excludes search.
  Do not infer that decision from identifiers, types, or the FDB `Indexed` flag.
- The container image is `opensearchproject/opensearch:3.8.0`.
- Use `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0 and nested `rank_features`.
- Reader parts contain at most 4,096 UTF-8 bytes; queries contain at most 126 UTF-8 bytes.
- Map page text as a native `semantic` field. OpenSearch performs fixed-character chunking, sparse encoding, pruning, and point-in-time pagination.
- Pin the latest stable Go client, v4.7.3. Task 1 must keep its core API compatibility test against the exact OpenSearch 3.8.0 image because the client documents later 3.x releases as best effort.
- Use typed client APIs for core index creation, settings, split, bulk, point-in-time, alias, document, health, block, and statistics operations. Build searches with the typed request API and decode the few response fields the typed response omits with `opensearch.Do`. Define narrow request types for ML Commons operations that stable v4 does not include, then use `opensearch.Do` and `opensearch.ParseError`. Do not add a generic method-and-path JSON API or import the temporary v5 preview package.
- Bulk requests contain at most 500 page documents and 5 MiB of encoded data, including action lines.
- A result page has at most 25 nodes within Tack's response-byte budget.
- Continuation can reach every matching node. Engine batches contain at most 100 matches; responses scan at most four batches.
- Configure one environment search endpoint in the official client. Its connection
  pool owns retries, TLS, and transport metrics. The hypervisor proxy owns backend
  health and selection. OpenSearch owns shard and ML worker selection.
- QA and production each start with one LXC guest and zero replicas. Production forms a normal one-member cluster and must not use `discovery.type: single-node`. Every combined-role guest has at least 8 GiB memory, 2 CPU cores, 40 GiB storage, and a 2 GiB JVM heap.
- Keep at least 6.26 GiB of suburban host memory available throughout the complete QA workload. The one-node CPU and fast-storage projections pass. QA does not claim OpenSearch node failover or horizontal scale.
- Adding Tack processes, FoundationDB capacity, OpenSearch ML nodes, data nodes, replicas, or coordinating endpoints must not require application code or stored-format changes. A higher primary-shard count uses native splitting along its reserved routing path and a full replacement otherwise.
- Persist all search sessions and work in FoundationDB. Distribute their keys across stable hash buckets. Do not require sticky requests, a process-local cache, a global sequence, or one claim range.
- Worker claims, cleanup, sessions, rebuilds, and physical indexes have explicit work and lifetime bounds.
- Each physical index stores primary and reserved routing-shard counts. Every shard increase creates and validates a replacement through native splitting or a full FoundationDB rebuild.
- All product state and search progress use FoundationDB. SQL remains authentication and audit only.
- Reads use `NodeReader`. Configuration uses environment variables through `caarlos0/env`.
- Tests use real dependencies and public boundaries. No mocks, product seeds, or production tokenizer dependency establish acceptance.
- Keep each new or edited file within 200 lines. Use focused files instead of adding to an oversized file.
- Execute Tasks 1 through 12 once, in number order. Tasks 1 through 11 use one linear Tack branch. Task 12 commits its Tack change, then its configs change on one linear configs branch. Do not parallelize, skip ahead, or rewrite an earlier task's interface in a later task.
- Each coding task authors its real-dependency tests, compiles the integration package with an empty test selection, runs `make check`, and commits. It does not start OpenSearch, FoundationDB, Traefik, or Proxmox.
- Task 13 runs the real dependencies, corrects failures, repeats affected tests, and runs the full search suite. Task 14 performs authorized QA and production operations.
- Include `Co-authored-by: Codex <noreply@openai.com>` in every signed commit.
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

## Permission filtering contract

The current policy returns version `org-scope-v1` and fields `org_id:A` and `scope_ids:[B, ancestor IDs]`. Task 5 stores them inside the strict `access` object. A scoped query returns the exact `access.org_id:A` and `access.scope_ids:B` clauses. Task 6 inserts those opaque clauses into the OpenSearch Boolean filter before lexical or sparse scoring. Task 7 reads the current node from FoundationDB and authorizes it again before rendering.

A future permission model changes only these owned surfaces:

1. Extend the Task 3 access policy so indexed fields and caller clauses use the same rules.
2. Add the new strict `access` mapping fields and increment the access version.
3. Keep the Task 5 field copier and Task 6 clause inserter unchanged.
4. Bump the projection and mapping version, then use Task 8 to rebuild from FoundationDB.
5. Add one raw-ranker test dominated by forbidden matches and one corrupt-index public test.

The extension cannot change content pages, document identity, durable work, sessions, continuation, result grouping, or the final FoundationDB authorization check.

## Review Focus

1. An edit between content reads must produce an explicit revision error, never a mixed document. Reader and worker tasks test it.
2. A paused old writer must not restore deleted text after another worker completes cleanup. Worker tasks test actual delayed requests.
3. A long uninterrupted Unicode string must not disappear inside model truncation. Native coverage tests inspect the semantic field's generated chunks and embeddings.
4. A byte-limited response must retain the first result it cannot render. Query tasks test continuation with large names.
5. A metadata or ancestry change during an index replacement must appear after the alias switch. Recovery tasks test concurrent public changes.
6. A large node, cleanup, or rebuild must yield before it starves live mutation work. Worker and capacity tasks measure every work class.
7. Session and rebuild cleanup must bound the number and lifetime of retained physical indexes.
8. Each initial environment must recover durable work after its only search node restarts. Production must pass cluster join, proxy distribution, replica placement, and one-member failure checks before it claims multi-node availability.
9. A future permission model must exclude most forbidden matches in OpenSearch before ranking. The final FoundationDB check preserves correctness but cannot establish search latency by itself.

---

## Execution order

Assign Tasks 1 through 12 to one Luna run with `superpowers:executing-plans`. Each task consumes only committed outputs from earlier numbered tasks. Run one task at a time. Keep one linear branch in each repository. The [fixture code](2026-09-19-opensearch-fixtures.md) supplies real-store setup and authenticated calls, but Tasks 1 through 12 only compile those tests. Assign Task 13 to Sol for live execution and corrective commits. Task 14 owns authorized deployment.

1. Complete the [native coverage task](2026-09-19-opensearch-native.md). Implement the validated sparse engine configuration and its regression tests.
2. Complete the [projection rollout task](2026-09-19-opensearch-metadata.md). Add declaration types, new-definition values, and the expiring manifest backfill.
3. Complete the [paginated reader task](2026-09-19-opensearch-reader.md), including the shared organization and scope access policy.
4. Complete the [durable work task](2026-09-19-opensearch-worker.md).
5. Complete the [bounded indexing task](2026-09-19-opensearch-indexing.md).
6. Complete the [ranked query task](2026-09-19-opensearch-query.md). It proves that OpenSearch applies the access filter before ranking.
7. Complete the [authorized public search task](2026-09-19-opensearch-public-search.md) behind an inactive registration.
8. Complete the [index replacement task](2026-09-19-opensearch-rebuild.md) against Tasks 3 through 7.
9. Complete the [runtime cutover task](2026-09-19-opensearch-recovery.md). It activates the replacement and deletes the application Meilisearch path.
10. Complete the [metadata refresh task](2026-09-19-opensearch-refresh.md) against the replacement runtime.
11. Complete the [public QA data task](2026-09-19-opensearch-datagen.md).
12. Complete the [cluster configuration task](2026-09-19-opensearch-deployment.md) without applying it.
13. Give the completed branch to Sol and execute the [live validation and correction plan](2026-09-19-opensearch-final-validation.md).
14. Apply the [QA and production release plan](2026-09-19-opensearch-release.md) only after deployment authorization.

## Delivery tickets

| Plan scope | Ticket |
| --- | --- |
| [Task 1: Native sparse indexing](2026-09-19-opensearch-native.md) | TACK-530 |
| [Task 2: Explicit projection rollout and backfill](2026-09-19-opensearch-metadata.md) | TACK-542 |
| [Task 3: Paginated node content reads](2026-09-19-opensearch-reader.md) | TACK-531 |
| [Task 4: Durable search work](2026-09-19-opensearch-worker.md) | TACK-533 |
| [Task 5: Bounded indexing and retirement](2026-09-19-opensearch-indexing.md) | TACK-534 |
| [Task 6: Ranked continuation](2026-09-19-opensearch-query.md) | TACK-535 |
| [Task 7: Authorized public search](2026-09-19-opensearch-public-search.md) | TACK-536 |
| [Task 8: Rebuild and restore](2026-09-19-opensearch-rebuild.md) | TACK-537 |
| [Task 9: Runtime and Meilisearch removal](2026-09-19-opensearch-recovery.md) | TACK-536 |
| [Task 10: Metadata refresh](2026-09-19-opensearch-refresh.md) | TACK-532 |
| [Task 11: QA datagen coverage](2026-09-19-opensearch-datagen.md) | TACK-538 |
| [Task 12, Tack: Containers and provisioning operations](2026-09-19-opensearch-deployment.md) | TACK-539 |
| [Task 12, configs: One initial guest per environment and scalable rendered configuration](2026-09-19-opensearch-deployment.md) | TACK-540 |
| [Task 13: Live validation and correction](2026-09-19-opensearch-final-validation.md) | TACK-518, TACK-519, TACK-520 |
| [Task 14: QA, production, and Meilisearch deployment removal](2026-09-19-opensearch-release.md) | TACK-541 |

TACK-518, TACK-519, and TACK-520 retain the cross-cutting scalability,
isolation, and semantic acceptance. TACK-524 and TACK-525 remain separate
storage work.

The native task repeats the successful engine tests through the production adapter. Preserve reproductions of any regression. Do not substitute truncation, a Tack tokenizer, an OpenSearch modification, or an external service.

## File responsibilities

| Responsibility | Change location |
| --- | --- |
| Reader contracts and access representation | Extend [NodeReader](../../../internal/domain/node/reader.go); create the content and access files specified in the reader task. |
| Search projection declarations | Add the property definition fields and validation specified in the metadata task. |
| Transactional scheduling and revision identity | Extend the node, relationship, and metadata stores; add dedicated search storage files. |
| Page indexing and native model setup | Add a focused official-client adapter, native semantic mapping, typed bulk and search operations, and concrete ML Commons requests; delete all Meilisearch adapter code and its module dependency. |
| Worker ownership and recovery | Add search worker, cleanup, and rebuild files under the existing service and FDB adapter packages. |
| Authentication and rendered results | Replace [MCP search](../../../internal/adapters/mcp/tools/search.go); reuse response-byte enforcement. |
| Runtime and operator entry points | Update [graph assembly](../../../internal/runtime/graph.go) and [search reindexing](../../../internal/ops/search_reindex.go). |
| Local proof and QA coverage | Replace the Meilisearch test environment and checks with real OpenSearch integration and datagen coverage. |
| Containers and environment configuration | Delete Meilisearch services, volumes, variables, and secrets from [Compose](../../../docker-compose.yml) and configs; prepare LXC, TLS, inventory, and Ansible changes for OpenSearch. |

## Commands and commit procedure

Tasks 1 through 12 run only their stated compile, offline render, static, and build checks. Task 13 runs every real-dependency command and owns corrections. Stage only the files changed by each task. The native task's commit command is:

```sh
git commit -S -m "Add native OpenSearch sparse indexing validation" -m "Co-authored-by: Codex <noreply@openai.com>"
```

Use each subsequent task's specified subject with that same trailer. Before any
later push, fetch and verify every signature
and raw `gpgsig` header in `origin/main..HEAD`.

## Storage expansion boundary

The current node storage model remains unchanged. Tests use smaller byte bounds
on the real reader to exercise successive parts within current storage limits.
Search depends only on the reader contract, including explicit completion.

TACK-524 and TACK-525 implement storage changes separately. Their acceptance must rerun this search suite with 128 KiB, 1 MiB, 8 MiB, over 100 MB, and nodes larger than worker memory. They replace the reader's storage implementation. They must not alter the worker loop, mapping, page IDs, stable search endpoint, or result grouping.
