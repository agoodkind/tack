# Search Runtime Cutover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable durable OpenSearch workers and remove the complete Meilisearch runtime in one application cutover.

**Architecture:** The application starts the official client independently of engine readiness. Bounded worker loops consume FoundationDB work. Search outages return explicit errors while source mutations remain available and durable.

**Tech Stack:** Go, FoundationDB, OpenSearch Go client v4.7.3, Docker Compose.

**Spec:** [Durable work and state lifecycle](../specs/2026-09-19-search-acceptance.md#durable-work-and-state-lifecycle).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Delete Meilisearch instead of migrating, adapting, querying, or preserving it. Keep model and index provisioning in audited operator commands, not application startup.

## Review Focus

Test startup with an unavailable engine, graceful shutdown, worker restart, invalid configuration, pending work during outage, and complete Meilisearch removal.

---

### Task 8: Replace runtime assembly and preserve public errors

**Files:**

- Create: `internal/runtime/search.go`
- Create: `internal/config/search.go`
- Create: `internal/test/integration/search_runtime_test.go`
- Modify: `internal/runtime/graph.go`
- Modify: `internal/config/config.go`
- Modify: `internal/service/node.go`
- Modify: `internal/adapters/mcp/server.go`
- Delete: `internal/adapters/mcp/tools/search.go`
- Modify: `docker-compose.yml`
- Delete: `internal/adapters/search/meilisearch.go`
- Delete: `internal/adapters/search/meilisearch_batch.go`
- Delete: `internal/adapters/search/nodes_index.go`
- Delete: Meilisearch testenv, configuration, module, datagen, operator, and recovery paths found by the final scan.

**Interfaces:**

- Consumes: `WorkStore`, `Worker`, `Ranker`, `SessionStore`, and Task 9 `Rebuilder`.
- Produces: `searchRuntime` and `searchDependencies` for MCP.

```go
type searchRuntime struct { cancel context.CancelFunc; done chan struct{} }
type searchDependencies struct { Ranker search.Ranker; Sessions search.SessionStore; Access searchaccess.AccessPolicy }
func buildSearchRuntime(context.Context, *config.Config, *foundationdb.Stores) (searchRuntime, searchDependencies, error)
func (r searchRuntime) Close()
func waitAfterWork(context.Context, error, time.Duration) error
```

- [ ] **Step 1: Add the failing outage recovery test.**

```go
func TestSearchRuntimeUnavailable(t *testing.T) {
    fixture := newStoppedSearchRuntime(t)
    nodeID := createNodeThroughMCP(t, fixture, "committed during outage")
    requireSearchUnavailable(t, fixture, "committed during outage")
    fixture.StartOpenSearch(t)
    requireEventuallySearchResult(t, fixture, "committed during outage", nodeID)
}
```

- [ ] **Step 2: Run the outage test and record the current failure.**

Run: `go test ./internal/test/integration -run '^TestSearchRuntimeUnavailable$' -count=1`

Expected: FAIL because the current runtime substitutes a Meilisearch no-op and does not drain durable work.

- [ ] **Step 3: Add and validate OpenSearch configuration.**

Add required `OPENSEARCH_URLS`, `OPENSEARCH_USERNAME`, `OPENSEARCH_PASSWORD`, and `OPENSEARCH_CA_FILE` fields. Add positive bounded values for page bytes, query bytes, request timeout, worker counts, claim time, and backoff. `SearchClientConfig` reads the CA file and returns `opensearch.Config` with one stable endpoint, TLS verification, retry statuses, timeout retries, metrics, and request timeout.

```go
func SearchClientConfig(cfg *Config) (opensearch.Config, error) {
    ca, err := os.ReadFile(cfg.OpenSearchCAFile)
    if err != nil { return opensearch.Config{}, fmt.Errorf("read OpenSearch CA: %w", err) }
    return opensearch.Config{Addresses: cfg.OpenSearchURLs, Username: cfg.OpenSearchUsername,
        Password: cfg.OpenSearchPassword, // gitleaks:allow because this reads configuration rather than embedding a secret
        CACert: ca, RetryOnStatus: []int{502, 503, 504},
        EnableRetryOnTimeout: true, MaxRetries: 3, RequestTimeout: cfg.OpenSearchRequestTimeout,
        EnableMetrics: true}, nil
}
```

- [ ] **Step 4: Assemble the official client without provisioning.**

Create the client after configuration validation. Do not require engine health at startup. Resolve the active index for search requests. Return `search.ErrUnavailable` when the endpoint cannot serve a query. Leave source mutations and durable scheduling available.

- [ ] **Step 5: Start bounded worker and cleanup loops.**

Start separate configured counts for live mutation, cleanup, metadata rescan, rebuild, and session cleanup work. Each iteration claims one item and invokes one bounded slice. Record an error, wait through capped context-aware backoff, then claim again.

```go
for {
    work, err := store.Claim(ctx, class, owner, claimDuration)
    if err == nil { err = worker.RunOne(ctx, work) }
    if err := waitAfterWork(ctx, err, backoff); err != nil { return }
}
```

`waitAfterWork` returns immediately after success, waits for the configured capped backoff after an error, and returns the context error after cancellation. It uses one timer and always stops that timer before returning.

- [ ] **Step 6: Implement bounded session cleanup.**

Mark an expired session closing, persist its deletion cursor, close its PIT, and delete token chunks, visited IDs, and replay records in bounded batches. Renewal conflicts with closing. Resume after restart. Treat an absent PIT as already closed. Delete the header last.

- [ ] **Step 7: Close search before storage.**

```go
func (r searchRuntime) Close() { r.cancel(); <-r.done }
```

Add the runtime to `Graph`. Call `search.Close()` before FoundationDB, audit, and SQL shutdown. Remove the old searcher argument from `NodeService` after every source mutation schedules FDB work.

- [ ] **Step 8: Activate ranked search and delete the entire Meilisearch application stack.**

Register Task 7's ranked handler in MCP assembly and delete the old `search.go`. Remove both environment variables, the client dependency, adapter, `NodeDoc`, synchronous writes, no-op fallback, Compose service and volume, testenv helper, datagen checks, operator paths, credentials, and recovery instructions. Run `go mod tidy`. Do not inspect or convert the existing index or volume.

- [ ] **Step 9: Prove no live Meilisearch path remains.**

Run: `rg -n -i 'meili|MEILI_' --glob '!docs/superpowers/**' .`

Expected: no runtime, configuration, dependency, container, secret, volume, test, or runbook match. Historical migration records may remain only when required by repository policy.

- [ ] **Step 10: Run the complete task checks.**

Run: `go test ./internal/test/integration -run '^TestSearchRuntime' -count=1`

Run: `make build`

Run: `make check`

Expected: PASS with an explicit outage error and automatic backlog recovery.

- [ ] **Step 11: Commit the task.**

```sh
git add -A
git commit -S -m "Remove Meilisearch and enable durable OpenSearch workers" -m "Co-authored-by: Codex <noreply@openai.com>"
```
