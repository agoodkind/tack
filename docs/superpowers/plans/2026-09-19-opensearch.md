# OpenSearch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the unused Meilisearch path first, then build and activate durable semantic search from FoundationDB without an intermediate search stack.

**Architecture:** The first release keeps `tack_search` registered and returns one fixed unavailable response while every other operation continues. Later vertical slices add the official OpenSearch client, explicit metadata, durable page indexing, ranked public search, refresh, replacement, QA coverage, and deployment configuration. FoundationDB remains authoritative throughout.

**Tech Stack:** Go, FoundationDB, OpenSearch 3.8.0, `github.com/opensearch-project/opensearch-go/v4` v4.7.3, ML Commons, Docker SDK, MCP, Ansible, OpenTofu.

**Spec:** [Search architecture](../specs/2026-09-19-search-design.md) and [acceptance criteria](../specs/2026-09-19-search-acceptance.md).

**Validation record:** [OpenSearch prototypes and experiments](2026-09-19-opensearch-validation.md).

## Global Constraints

- Execute the plans below in order. Each plan requires committed interfaces from earlier plans.
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
- Text projection changes schedule bounded content updates against the serving physical index. Regenerate embeddings only for affected pages. Do not start index replacement.
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
- This plan authorizes submitting the listed implementation pull requests through their specified workflows. It does not authorize a merge, deployment, ruleset change, shared-history rewrite, or volume deletion.

## Review Focus

1. Every temporary-outage search call returns the exact public message while non-search operations continue.
2. A node edit between page reads returns an explicit revision error and never mixes revisions.
3. Delayed content, access, and retirement writes cannot replace a newer generation.
4. OpenSearch filters forbidden candidates before ranking, and FoundationDB still rejects stale or corrupt indexed access.
5. Duplicate-heavy nodes cannot prevent continuation from returning other eligible nodes.
6. A rebuild or native split includes every concurrent mutation before alias activation.
7. Single-node QA and production outages preserve source writes and durable search work.
8. Permission-policy transitions update opaque access values without reading text, invoking the model, or replacing the index.
9. Text projection changes reread and reembed only affected pages without replacing the physical index.

---

## Serial execution order

The Tack and Configs removal pull requests are merged. Complete the removal release before creating the OpenSearch stack. Luna creates the dependent Tack slices as one Graphite stack. Sol validates the stack tip and applies each correction to the branch that owns the behavior.

1. [x] [Remove Meilisearch and preserve the public outage contract](2026-09-20-meilisearch-removal.md). Ticket: TACK-541.
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
12. Activate OpenSearch in QA and production through the second phase of the [release plan](2026-09-19-opensearch-release.md). Ticket: TACK-544.

The [fixture reference](2026-09-19-opensearch-fixtures.md) defines shared real-dependency setup. It does not define another execution step. TACK-524 and TACK-525 remain separate storage work that implements the reader interface. Their implementation reruns the same search suite at 128 KiB, 1 MiB, 8 MiB, over 100 MB, and larger than worker memory.

## Pull request split and Graphite execution

Use the `split-to-prs`, `graphite`, and `pr` skills during implementation. Reuse an open implementation pull request only when it already contains the same slice and preserves all unique work. The current design pull request is documentation and cannot become an implementation branch.

The independent removal pull requests are complete:

| Repository | Pull request | Status |
| --- | --- | --- |
| Tack | [#271](https://github.com/agoodkind/tack/pull/271) | Merged |
| Configs | [#479](https://github.com/agoodkind/configs/pull/479) | Merged |

Complete the remaining deployment and live verification work in release Phase A. Then start the following Tack stack from the updated `origin/main`:

| Stack position | Pull request | Plan |
| --- | --- | --- |
| 1 | `[TACK-530] Add native OpenSearch client and provisioning` | Native control |
| 2 | `[TACK-542] Add explicit search projection metadata` | Metadata |
| 3 | `[TACK-531] Add durable paginated OpenSearch indexing` | Index pipeline, including TACK-533 and TACK-534 |
| 4 | `[TACK-535] Add ranked authorized OpenSearch queries` | Query pipeline, including TACK-536 |
| 5 | `[TACK-532] Add search metadata and permission refresh` | Refresh |
| 6 | `[TACK-537] Add durable OpenSearch index replacement` | Rebuild and native split |
| 7 | `[TACK-538] Add guarded OpenSearch QA verification` | QA datagen |
| 8 | `[TACK-539] Add the scalable OpenSearch container and provisioning contract` | Tack deployment files |

These dependencies are real. Each higher slice calls interfaces or production paths created by the slice below it. Each branch must pass `make build` at its stack position. Keep every test in the branch that adds the behavior.

Create `[TACK-540] Provision one initial OpenSearch guest per environment` as one independent Configs pull request after the Tack container contract is committed. A branch in another repository cannot join the Tack Graphite stack.

Before creating the stack, save dirty work under `refs/backup/`, fetch `origin`, identify the worktree that owns `main`, and run Graphite MCP `state --no-interactive` with the absolute Tack worktree path. Do not move `main` from another worktree. Confirm `commit.gpgSign=true` and `rebase.gpgSign=true` before Graphite creates or rewrites commits.

Create the slices from bottom to top with Graphite MCP `create`. Pass the exact multiline message from each slice, including its blank line and `Co-authored-by: Codex <noreply@openai.com>` trailer. Stage only the files or hunks listed by the current slice. Do not use `git commit`, `git push`, `git add .`, or `git add -A` for stack work. Preview with `submit --stack --dry-run --no-interactive`, inspect every create or update action, then run `submit --stack --no-interactive`.

Use the `pr` skill to write every title and body after Graphite assigns pull request numbers. Add `(PR x/N)` only when the numbers are not consecutive in bottom-to-top order. Mark the pull requests ready after their bodies are complete.

Sol validates the stack tip with the independent Configs pull request. Route a correction to its owning branch with Graphite MCP `modify` or a reviewed `absorb --dry-run` followed by `absorb --force`. Restack from the corrected branch through its upstack, verify every rewritten signature, preview the stack submission, and submit it again. Do not merge the stack until the final validation plan passes. Merge it bottom to top through Graphite after separate authorization.

## Durable interface boundaries

| Slice | Production entry point | Durable output |
| --- | --- | --- |
| Removal | MCP registration and normal graph startup | Exact unavailable response with no Meilisearch dependency |
| Native control | Registered `ops search provision` and `ops search verify` | Pinned client, model, mapping, alias, and topology checks |
| Metadata | Property-definition writes and registered expiring backfill | Complete explicit projection declarations |
| Indexing | Node mutations and runtime worker loops | Bounded page documents and resumable work in FoundationDB |
| Query | Registered `tack_search` handler | Ranked nodes and durable continuation sessions |
| Refresh | Metadata and permission writes | Projection-triggered content refresh and access-only transitions |
| Replacement | Registered search reindex operation | Validated alias replacement and retired-index state |
| QA verification | Guarded QA datagen operation | Public behavior evidence |

Each slice owns all files needed by its production entry point. Later slices extend these interfaces without replacing them.

## Repository boundaries

- Tack owns application code, OpenSearch client behavior, public tools, workers, operator commands, tests, the pinned container, and application configuration.
- Configs owns guests, TLS, credentials, inventory, the hypervisor proxy, deployed environment values, and OpenTofu resources.
- The removal release edits both repositories but does not delete the existing data volume.
- The deployment plan prepares configurations without applying them. The release plan owns every authorized apply and deployment.

## Commit and validation procedure

Luna writes the tests required by each coding task but does not run the live dependency suite. Luna runs `make build` once after each task and creates or modifies its Graphite slice only after that command passes. The independent Meilisearch removal release runs its public behavior tests and `make build`. Before each stack submission, fetch the remote, inspect `origin/main..HEAD`, verify every commit with `git verify-commit`, and confirm every raw commit object contains a `gpgsig` header. Repeat this verification after every restack.

The final validation plan starts the real dependencies, corrects failures in the owning slice, reruns the affected tail, runs the complete search suite, and finishes with one clean `make build`. The release plan records provisioning, deployment, activation, live acceptance, capacity, and cleanup as separate facts.
