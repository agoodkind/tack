# Meilisearch Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the unused Meilisearch stack while preserving the `tack_search` tool as a clear temporary outage.

**Architecture:** The MCP server keeps the existing tool name and input schema. Its handler returns one fixed recoverable error before resolving any argument. Node writes stop calling a search adapter. Runtime startup, repair tools, datagen, test infrastructure, and deployment configuration stop constructing or requiring Meilisearch. The existing data volume remains untouched until a separate operation is authorized.

**Tech Stack:** Go, MCP, Docker Compose, Ansible, OpenTofu.

**Spec:** [Meilisearch removal and temporary search outage](../specs/2026-09-19-search-acceptance.md#meilisearch-removal-and-temporary-search-outage).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Do not inspect or reuse any Meilisearch data. Do not delete a volume. Do not add OpenSearch code in this release.

## Review Focus

Require the exact unavailable response for every search request. Require all other MCP tools and source writes to keep working. Require normal startup without search credentials, a search service, or a search adapter.

---

### Task 1: Replace public search with the temporary outage

**Files:**

- Modify: `internal/adapters/mcp/tools/search.go`
- Create: `internal/adapters/mcp/tools/search_test.go`
- Modify: `internal/adapters/mcp/server.go`
- Test: `internal/test/integration/mcp_search_test.go`

**Interface:** Keep `tack_search` and its current schema. Every valid or invalid call returns exactly `Search is temporarily unavailable.` as a recoverable tool error. The response contains no engine name, implementation status, or availability estimate.

- [ ] Add an integration test that calls `tack_search` with an ordinary query, an exact node reference, filters, an invalid reference, and omitted arguments. Require the exact message in every response.
- [ ] Replace the existing handler body with an immediate recoverable error. Remove exact-reference lookup, scope resolution, indexed-result loading, rendering, and the `Searcher` argument.
- [ ] Remove the MCP server's search dependency and register the fixed handler during normal startup.
- [ ] Run the focused MCP tests that cover registration and the exact response.

### Task 2: Remove application indexing and the Meilisearch adapter

**Files:**

- Delete: `internal/adapters/search/meilisearch.go`
- Delete: `internal/adapters/search/meilisearch_batch.go`
- Delete: `internal/adapters/search/nodes_index.go`
- Delete: `internal/adapters/search/search.go`
- Delete: `internal/domain/search/searcher.go`
- Delete: `internal/service/search_doc.go`
- Modify: `internal/service/node.go`
- Modify: Node service constructor call sites and tests found by `rg 'NewNodeService|Searcher|SearchDocFromView'`
- Modify: `internal/runtime/graph.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] Remove `Searcher` from `NodeService`, its constructor, and runtime dependencies. Remove synchronous indexing after writes and search deletion after node deletion.
- [ ] Delete the old projection-by-property-type code. Do not preserve its property choices for the OpenSearch design.
- [ ] Remove Meilisearch client construction, index setup, fallback behavior, startup logs, and the SDK dependency.
- [ ] Test node create, edit, and delete through public boundaries. Require the FoundationDB and SQL effects to succeed without any search service.

### Task 3: Remove operations, datagen, and local test infrastructure

**Files:**

- Delete: `internal/ops/search_reindex.go`
- Modify: `internal/ops/repair_console.go`
- Modify: `internal/ops/cli_act_as.go`
- Modify: `internal/ops/backup_restore_drill.go`
- Delete: `internal/testenv/meilisearch.go`
- Modify: `internal/testenv/testenv.go`
- Modify: `cmd/testenv/main.go`
- Delete: `internal/datagen/generate_search_checks.go`
- Modify: `internal/datagen/generate_issues.go`
- Modify: `internal/datagen/guard.go`
- Modify: `internal/datagen/guard_test.go`
- Delete: `internal/test/integration/search_reindex_test.go`
- Modify: `internal/test/integration/mcp_harness_helpers_test.go`
- Delete or rewrite: old Meilisearch-specific integration tests found by `rg -l -i 'meili|meilisearch' internal cmd`

- [ ] Remove the old reindex operation. Remove search adapters from the repair console and act-as factories while preserving their source-data behavior.
- [ ] Remove the Meilisearch test environment, CLI subcommand, harness fields, search-only generated checks, and assertions that require successful search.
- [ ] Add representative integration coverage for every registered non-search MCP tool. Reuse the normal MCP harness with no search container or search credentials.
- [ ] Update backup and recovery comments so they state only current source-system behavior.

### Task 4: Remove local service configuration and documentation

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `.env.example`
- Modify: `docker-compose.yml`
- Delete: `docs/runbooks/meilisearch.md`
- Modify: `docs/runbooks/recovery.md`
- Modify or delete: stale Meilisearch references found outside `docs/superpowers`

- [ ] Remove Meilisearch environment fields, validation, examples, container, health checks, dependencies, ports, and named-volume declarations.
- [ ] Do not run `docker volume rm` or any equivalent command. An orphaned existing volume is allowed.
- [ ] Delete instructions that operate or restore Meilisearch. Keep source-data recovery instructions accurate.
- [ ] Run `rg -n -i 'meili|meilisearch' --glob '!docs/superpowers/**' .`. Every remaining match must explain the deliberate absence of Meilisearch rather than configure, call, test, or operate it.

### Task 5: Prove and commit the Tack removal release

- [ ] Run the focused behavior tests from Tasks 1 through 4.
- [ ] Run `make build` once. Fix every failure without editing a lint baseline or accepting new findings.
- [ ] Run the MCP integration test with no search service. Require every search request to return the exact unavailable message. Require node writes and every representative non-search tool to succeed.
- [ ] Review `git diff --check` and the complete Tack diff.
- [ ] Create one signed Tack commit:

```sh
git add -A
git commit -S -m "Remove the Meilisearch application path" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 6: Remove deployed Meilisearch configuration

**Repository:** Configs. Start this task from the Tack removal commit and the current Configs default branch. Do not apply or deploy.

**Files:** Find the exact owned files with `rg -n -i 'meili|meilisearch' .` before editing.

- [ ] Remove the deployed service, health checks, proxy entries, credentials, environment values, firewall rules, monitoring, and documentation that exist only for Meilisearch.
- [ ] Remove configuration that injects Meilisearch values into Tack. Do not add OpenSearch values yet.
- [ ] Preserve any existing Meilisearch data volume or disk. Do not declare its deletion in OpenTofu, Ansible, shell, or Docker cleanup.
- [ ] Run the Configs repository's focused unit checks for the edited roles and `tofu validate` for each edited stack.
- [ ] Review the planned infrastructure changes. Require no data-volume deletion and no unrelated resource replacement.
- [ ] Create one signed Configs commit with the Codex coauthor trailer.

The first phase of the [release plan](2026-09-19-opensearch-release.md) deploys these two commits. Search remains unavailable until the later OpenSearch activation phase passes its acceptance checks.
