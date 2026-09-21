# OpenSearch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the unused Meilisearch path first, then build and activate durable semantic search from FoundationDB without an intermediate search stack.

**Architecture:** The first release keeps `tack_search` registered and returns one fixed unavailable response while every other operation continues. Later vertical slices add the official OpenSearch client, explicit metadata, durable page indexing, ranked public search, refresh, replacement, QA coverage, and deployment configuration. FoundationDB remains authoritative throughout.

**Tech Stack:** Go, FoundationDB, OpenSearch 3.8.0, `github.com/opensearch-project/opensearch-go/v4` v4.7.3, ML Commons, Docker SDK, MCP, Ansible, OpenTofu.

**Spec:** [Search architecture](../specs/2026-09-19-search-design.md) and [acceptance criteria](../specs/2026-09-19-search-acceptance.md).

**Validation record:** [OpenSearch prototypes and experiments](2026-09-19-opensearch-validation.md).

## Global Constraints

- Execute the plans below in order. Each plan consumes committed interfaces from earlier plans.
- Ship the Meilisearch removal as an independent first release. Keep `tack_search` registered and return exactly `Search is temporarily unavailable.` for every call, including exact references.
- Keep every other MCP tool and every FoundationDB or SQL source write operational during the temporary outage.
- Do not read, inspect, migrate, translate, export, import, or reuse Meilisearch data, schema, settings, synonyms, rankings, results, snapshots, dumps, or volumes.
- Treat deletion of an old Meilisearch volume as a separately authorized operation.
- Create the first OpenSearch index empty and rebuild it only from FoundationDB, including mutations committed during the outage.
- Do not activate the OpenSearch public handler until the empty rebuild and required acceptance checks pass.
- Use the official stable Go client for all transport. Use typed APIs for core operations and narrow concrete request types through `opensearch.Do` only where v4.7.3 lacks an API or response field.
- Do not add another HTTP client, generic method-and-path transport, retry loop, connection pool, error decoder, OpenSearch plugin, fork, ingest pipeline, tokenizer, or external inference service.
- Use `opensearchproject/opensearch:3.8.0` and `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0.
- Tack enforces a 4,096-byte UTF-8 reader-page bound. OpenSearch owns character chunking and model tokenization.
- Node types, property types, property names, permission types, roles, groups, organizations, and scopes remain opaque to search code.
- Every applicable property definition explicitly includes or excludes search. Never derive search behavior from identifiers, types, seeds, or the FoundationDB `Indexed` flag.
- Store only `access.versions`, `access.keys`, and `access.generation` for permission filtering. FoundationDB performs the final current authorization check.
- Principal membership changes alter query keys only. Resource visibility changes schedule bounded access-only updates that preserve text and embeddings.
- Persist work, sessions, rebuild state, cursors, generations, visited node IDs, and query tokens in FoundationDB. Keep request handlers stateless.
- Process at most 32 reader pages or 5 MiB per worker slice. Process at most 100 cleanup or access IDs per slice.
- Return at most 25 distinct nodes per public response. Read at most four OpenSearch batches of 100 page matches per response. Continuation must still return every eligible node.
- QA and production start with one combined-role OpenSearch guest and zero replicas. Production uses normal cluster discovery from its first start.
- Each model guest has at least 8 GiB memory, 2 CPU cores, 40 GiB fast storage, and a 2 GiB JVM heap.
- Keep at least 6.26 GiB available on `suburban` throughout the complete QA workload.
- Keep every new or edited file within 200 lines and split files by responsibility.
- Use real dependencies and public boundaries. Do not use mocks, stubs, recorded responses, private helpers, or product seeds as acceptance evidence.
- Before the first code edit, run `make build` on the exact base. Run `make build` after every coding task and before its signed commit. Fix every failure in that task.
- Keep every new production declaration reachable from a real production entry point in the task that adds it. Do not add future-only interfaces, adapters, constructors, helpers, or exports.
- Do not edit lint baselines or run an `accept-new` baseline target. New lint, complexity, strict-analyzer, and dead-code findings must remain zero.
- Use concrete types, injected clocks, contextual logging, returned errors, and recovered goroutines. Do not introduce `any`, empty interfaces, `panic`, direct `time.Now`, `context.TODO`, unprotected goroutines, or `//nolint`.
- Include `Co-authored-by: Codex <noreply@openai.com>` in every signed commit.
- No plan authorizes a push, merge, deployment, ruleset change, shared-history rewrite, or volume deletion.

## Review Focus

1. Every temporary-outage search call returns the exact public message while non-search operations continue.
2. A node edit between page reads returns an explicit revision error and never mixes revisions.
3. Delayed content, access, and retirement writes cannot replace a newer generation.
4. OpenSearch filters forbidden candidates before ranking, and FoundationDB still rejects stale or corrupt indexed access.
5. Duplicate-heavy nodes cannot prevent continuation from returning other eligible nodes.
6. A rebuild or native split includes every concurrent mutation before alias activation.
7. Single-node QA and production outages preserve source writes and durable search work.
8. Permission-policy transitions update opaque access values without reading text, invoking the model, or replacing the index.

---

## Serial execution order

The first plan produces a separately reviewable and deployable outage release. Later Tack branches may be prepared in one Luna run, but each branch starts from the preceding committed branch and remains independently reviewable. Do not squash unrelated tickets into the removal release. Sol performs the final live validation and corrections after the coding and configuration plans finish.

1. [Remove Meilisearch and preserve the public outage contract](2026-09-20-meilisearch-removal.md). Ticket: TACK-541.
2. Merge and deploy that removal release through the first phase of the [release plan](2026-09-19-opensearch-release.md). Search remains temporarily unavailable.
3. [Add the official client, native semantic mapping, and audited control operations](2026-09-19-opensearch-native.md). Tickets: TACK-530 and TACK-539.
4. [Add explicit projection metadata and the expiring backfill](2026-09-19-opensearch-metadata.md). Ticket: TACK-542.
5. [Build the complete durable indexing pipeline](2026-09-20-opensearch-index-pipeline.md). Tickets: TACK-531, TACK-533, and TACK-534.
6. [Build ranked, authorized public search](2026-09-20-opensearch-query-pipeline.md). Tickets: TACK-535 and TACK-536.
7. [Add metadata and permission refresh](2026-09-19-opensearch-refresh.md). Ticket: TACK-532.
8. [Add rebuild, native split, restore, and alias recovery](2026-09-19-opensearch-rebuild.md). Ticket: TACK-537.
9. [Add guarded public QA coverage](2026-09-19-opensearch-datagen.md). Ticket: TACK-538.
10. [Prepare the initial guests, stable endpoints, and scalable configuration](2026-09-19-opensearch-deployment.md). Tickets: TACK-539 and TACK-540.
11. Give the completed branches to Sol for the [live validation and correction plan](2026-09-19-opensearch-final-validation.md). Tickets: TACK-518, TACK-519, and TACK-520.
12. Activate OpenSearch in QA and production through the second phase of the [release plan](2026-09-19-opensearch-release.md). Tickets: TACK-536 and TACK-541.

The [fixture reference](2026-09-19-opensearch-fixtures.md) supplies shared real-dependency setup. It does not define another execution step. TACK-524 and TACK-525 remain separate storage work behind the reader interface. Their implementation reruns the same search suite at 128 KiB, 1 MiB, 8 MiB, over 100 MB, and larger than worker memory.

## Durable interface boundaries

| Slice | Production entry point | Durable output |
| --- | --- | --- |
| Removal | MCP registration and normal graph startup | Exact unavailable response with no Meilisearch dependency |
| Native control | Registered `ops search provision` and `ops search verify` | Pinned client, model, mapping, alias, and topology checks |
| Metadata | Property-definition writes and registered expiring backfill | Complete explicit projection declarations |
| Indexing | Node mutations and runtime worker loops | Bounded page documents and resumable work in FoundationDB |
| Query | Registered `tack_search` handler | Ranked nodes and durable continuation sessions |
| Refresh | Metadata and permission writes | Projection epochs and access-only transitions |
| Replacement | Registered search reindex operation | Validated alias replacement and retired-index state |
| QA verification | Guarded QA datagen operation | Public behavior evidence |

Each slice owns all files needed by its production entry point. Later slices extend these interfaces without replacing them.

## Repository boundaries

- Tack owns application code, OpenSearch client behavior, public tools, workers, operator commands, tests, the pinned container, and application configuration.
- Configs owns guests, TLS, credentials, inventory, the hypervisor proxy, deployed environment values, and OpenTofu resources.
- The removal release edits both repositories but does not delete the existing data volume.
- The deployment plan prepares configurations without applying them. The release plan owns every authorized apply and deployment.

## Commit and validation procedure

Run the exact test named by a task when that task changes behavior. Run `make build` once after the task is complete. Commit only after both pass. Before any push, fetch the remote, inspect `origin/main..HEAD`, verify every commit with `git verify-commit`, and confirm every raw commit object contains a `gpgsig` header.

The final validation plan starts the real dependencies, corrects failures in the owning slice, reruns the affected tail, runs the complete search suite, and finishes with one clean `make build`. The release plan records provisioning, deployment, activation, live acceptance, capacity, and cleanup as separate facts.
