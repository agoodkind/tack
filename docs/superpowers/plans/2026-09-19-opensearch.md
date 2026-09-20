# OpenSearch Implementation Plan

Stop implementation. The proposed native configuration failed coverage validation. Resolve the failed requirement and revise this plan before executing its tasks.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Search every accepted node through a paginated reader and return authorized, distinct nodes ranked by OpenSearch.

**Architecture:** The reader returns one bounded part per call. The search worker indexes each part and continues until an explicit end marker. OpenSearch performs text splitting, local embedding, and ranking.

**Tech Stack:** Go, FoundationDB, OpenSearch 3.8.0, ML Commons, Docker SDK, MCP, Ansible, OpenTofu.

**Spec:** [Search architecture](../specs/2026-09-19-search-design.md). The [acceptance criteria](../specs/2026-09-19-search-acceptance.md) define release evidence.

## Global Constraints

- Multi-page behavior must pass acceptance before the first search release.
- Tack neither loads a tokenizer nor counts model tokens. OpenSearch remains unmodified.
- Custom plugins, forks, and external inference are excluded.
- Node types, property types, and property names are opaque identifiers.
- The container image is `opensearchproject/opensearch:3.8.0`.
- Use `huggingface/sentence-transformers/all-MiniLM-L6-v2` version 1.0.2 and 384-dimensional vectors.
- The native chunker uses `max_chunk_limit: -1`.
- Bulk requests contain at most 500 page documents and 5 MiB of encoded data, including action lines.
- A result page has at most 25 nodes within Tack's response-byte budget.
- A continuation represents one bounded ranked set of at most 1,000 distinct node IDs.
- QA and production each use three LXC guests, with 4 GB memory, 2 CPU cores, 40 GB storage, and 2 GB JVM heap per guest.
- All product state and search progress use FoundationDB. SQL remains authentication and audit only.
- Reads use `NodeReader`. Configuration uses environment variables through `caarlos0/env`.
- Tests use real dependencies and public boundaries. No mocks, product seeds, or production tokenizer dependency establish acceptance.
- Keep each new or edited file within 200 lines. Use focused files instead of adding to an oversized file.
- Run `make check` before each signed commit. Include `Co-authored-by: Codex <noreply@openai.com>`.
- This plan does not authorize a push, merge, deployment, ruleset change, or storage-limit removal.

## Review Focus

1. An edit between content reads must produce an explicit revision error, never a mixed document. Reader and worker tasks test it.
2. A paused old writer must not restore deleted text after another worker completes cleanup. Worker tasks test actual delayed requests.
3. A long uninterrupted Unicode string must not disappear inside model truncation. Native coverage tests inspect the deployed tokenizer inputs.
4. A byte-limited response must retain the first result it cannot render. Query tasks test continuation with large names.
5. A metadata or ancestry change during a rebuild must appear after the alias switch. Recovery tasks test concurrent public changes.

---

## Execution order

Each linked task document specifies its files, interfaces, tests, and commit boundary.
These are parts of one implementation. None introduces a temporary search design.
The [fixture code](2026-09-19-opensearch-fixtures.md) supplies real-store setup and
authenticated calls for the owning tasks.

1. Complete the [native coverage task](2026-09-19-opensearch-native.md). It establishes whether the unchanged engine can satisfy the approved embedding requirement.
2. Implement Task 2 in the [reader tasks](2026-09-19-opensearch-reader.md), including metadata declarations, revision identity, bounded pages, and summaries.
3. Implement the [durable indexing tasks](2026-09-19-opensearch-worker.md), including transaction scheduling, retries, and deletion.
4. Implement the [query tasks](2026-09-19-opensearch-query.md). Complete native ranking first. Treat MCP integration in Task 7 and runtime assembly in Task 8 as one review and commit unit; neither public path can pass independently. Then complete Task 3's metadata refresh test against that runtime.
5. Complete the remaining [recovery tasks](2026-09-19-opensearch-recovery.md), including rebuild, restore, and QA generator coverage.
6. Prepare and validate the [deployment tasks](2026-09-19-opensearch-deployment.md) in Tack and configs. Apply only after deployment authorization.

The native coverage task is a prerequisite for approving the release configuration.
If it fails, retain its reproducer and report the failing input. Do not substitute
truncation, a Tack tokenizer, an OpenSearch modification, or an external service.
The remaining tasks specify the accepted interfaces; a failed prerequisite does
not authorize implementing a different design.

## File responsibilities

| Responsibility | Change location |
| --- | --- |
| Reader contracts and metadata representation | Extend [NodeReader](../../../internal/domain/node/reader.go); create the domain content and projection files specified in the reader tasks. |
| Transactional scheduling and revision identity | Extend the node, relationship, and metadata stores; add dedicated search storage files. |
| Page indexing and native model setup | Replace the Meilisearch adapter with focused OpenSearch client, model, mapping, bulk, and query files. |
| Worker ownership and recovery | Add search worker, cleanup, and rebuild files under the existing service and FDB adapter packages. |
| Authentication and rendered results | Replace [MCP search](../../../internal/adapters/mcp/tools/search.go); reuse response-byte enforcement. |
| Runtime and operator entry points | Update [graph assembly](../../../internal/runtime/graph.go) and [search reindexing](../../../internal/ops/search_reindex.go). |
| Local proof and QA coverage | Extend the existing testenv, integration, and datagen packages. |
| Containers and environment configuration | Update [Compose](../../../docker-compose.yml); prepare LXC, TLS, inventory, and Ansible changes in configs. |

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

TACK-524 and TACK-525 implement storage changes separately. Their acceptance must
rerun this search suite with 128 KiB, 1 MiB, 8 MiB, over 100 MB, and nodes larger
than worker memory. They replace the reader's storage implementation. They must
not alter the worker loop, mapping, page IDs, retries, or result grouping.
