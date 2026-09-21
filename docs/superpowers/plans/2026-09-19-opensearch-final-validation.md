# OpenSearch Live Validation and Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Validate the completed OpenSearch implementation against real dependencies, correct every failure, and leave one reviewed branch ready for authorized QA deployment.

**Architecture:** Luna completes the serial coding and configuration plans without running the live suite. Sol starts the pinned OpenSearch and FoundationDB environment, validates each dependency layer in order, corrects failures at the owning layer, then repeats the affected tail and complete suite. The release plan separately validates deployed QA and production.

**Tech Stack:** Go, Docker Compose test runner, FoundationDB 7.4.6, OpenSearch 3.8.0, official OpenSearch Go client v4.7.3, RSpec, OpenTofu.

**Spec:** [Search acceptance](../specs/2026-09-19-search-acceptance.md).

## Global Constraints

Start only after every preceding serial plan is committed on the linear Tack and configs branches and both worktrees are clean. Require `CONFIGS_ROOT` to identify the reviewed configs checkout. Use real dependencies and public boundaries. Do not use mocks, skip tests, weaken assertions, raise accepted limits, or replace the selected design to make a test pass. Correct implementation defects directly. Stop with evidence when a failure requires a design or acceptance change. Do not apply OpenTofu, deploy QA or production, delete volumes, or change branch rules. Record both starting commits, exact commands, image digests, failures, corrections, measurements, and final commits for the pull request.

## Review Focus

Test complete Unicode page coverage, stale content and access writers, forbidden
matches before ranking, corrupt indexed access with final authorization,
permission-version transition without inference or replacement, duplicate-heavy
traversal, lost responses, replacement during mutations, engine outage recovery,
metadata refresh, and single-node restart.

---

### Task 1: Run live validation and correct the completed implementation

**Files:**

- Modify: only the Tack or configs files owned by the preceding serial plans when a reproduced failure requires a correction.
- Test: `internal/test/integration/search_native_test.go`
- Test: `internal/test/integration/search_projection_backfill_test.go`
- Test: `internal/test/integration/search_reader_test.go`
- Test: `internal/test/integration/search_work_test.go`
- Test: `internal/test/integration/search_recovery_test.go`
- Test: `internal/test/integration/search_ranking_test.go`
- Test: `internal/test/integration/search_permission_filter_test.go`
- Test: `internal/test/integration/search_auth_test.go`
- Test: `internal/test/integration/search_cursor_test.go`
- Test: `internal/test/integration/search_rebuild_test.go`
- Test: `internal/test/integration/search_runtime_test.go`
- Test: `internal/test/integration/search_metadata_refresh_test.go`
- Test: `internal/test/integration/search_access_refresh_test.go`
- Test: `internal/test/integration/search_datagen_test.go`
- Test: `internal/test/integration/search_cluster_test.go`

**Interfaces:**

- Consumes: the committed outputs and authored tests from every preceding serial plan.
- Produces: a corrected signed branch, complete real-dependency results, resource measurements, and the evidence the release plan requires.

- [ ] **Step 1: Record the exact starting state and clear stale test services.**

```sh
git fetch origin
git status --short
git rev-parse HEAD
git log --oneline --decorate origin/main..HEAD
: "${CONFIGS_ROOT:?set CONFIGS_ROOT to the reviewed configs checkout}"
git -C "$CONFIGS_ROOT" fetch origin
git -C "$CONFIGS_ROOT" status --short
git -C "$CONFIGS_ROOT" rev-parse HEAD
make test-env-down
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner build tests
```

Require an empty status before testing. Record the test-runner image digest and the OpenSearch 3.8.0 image digest after the build.

- [ ] **Step 2: Validate the official client, model, mapping, and complete page embedding.**

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearchNative' ./internal/test/integration
```

Require the pinned hashes, typed client operations, strict generic access mapping,
bulk partial update with external versioning, full 4,096-byte Unicode source,
generated final chunk, finite sparse weights, access-only success with the model
undeployed, explicit engine failures, 8 GiB success, and preserved 4 GiB
circuit-breaker regression.

- [ ] **Step 3: Validate metadata, pagination, and durable work.**

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(Projection|Reader|Work)' ./internal/test/integration
```

Require explicit declarations, retry-safe backfill, complete paginated text,
revision-bound cursors, opaque identifiers, generic versioned access keys,
content and access work kinds, one monotonic generation, bounded scans, durable
claims, restart recovery, and bounded key cleanup.

- [ ] **Step 4: Validate indexing, retirement, and stale-write rejection.**

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(DelayedWriter|RevisionCleanup|WorkerFairness|Recovery)' ./internal/test/integration
```

Require older content, access, and retirement writes to fail after a newer
generation, every partial bulk failure to retain pending work, access-only work
to read no page text, and large nodes to yield to live work.

- [ ] **Step 5: Validate ranking, access filtering, authorization, and continuation.**

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(SemanticRelevance|DistinctNodes|PermissionFilter|Auth|Authorization|Cursor|EmptyQuery)' ./internal/test/integration
```

Require one query prediction, every accepted relevance target within its bound,
all 1,501 nodes exactly once with 36,000 duplicate pages, active-version and
opaque-key filtering before ranking, final FoundationDB authorization after a
corrupt indexed key or revoked membership, exact replay, and complete
continuation.

- [ ] **Step 6: Validate replacement, runtime recovery, metadata refresh, and public QA checks.**

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(Rebuild|Split|Restore|Runtime|Metadata|Access|Datagen)' ./internal/test/integration
```

Require mutation catch-up before alias switch, crash recovery in every replacement
phase, unchanged sparse weights after native split, explicit outage errors,
automatic backlog drain, metadata changes without restart, membership-only
changes with zero document writes, resource access updates with zero content
reads, a restartable dual-version transition on one physical index, byte-identical
semantic fields with the model undeployed, real JSON and SSE decoding, and
production guard rejection.

- [ ] **Step 7: Validate disposable cluster configuration and scale-out behavior.**

```sh
git -C "$CONFIGS_ROOT" diff --check
(cd "$CONFIGS_ROOT" && bundle exec rspec spec/ansible/tack_search_spec.rb spec/ansible/tack_search_proxy_spec.rb)
(cd "$CONFIGS_ROOT" && ./configsctl tofu validate)
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearchCluster' ./internal/test/integration
```

Require one stable client endpoint, one-member restart recovery, later member joining without a new bootstrap cluster, proxy distribution, model placement, replica allocation, and one-member failure behavior. Do not connect to live Proxmox or apply a plan.

- [ ] **Step 8: Correct each reproduced failure at its owning layer.**

For each failure, rerun the smallest exact test until it fails consistently. Trace the production path. Correct the files owned by the responsible serial plan. Add a regression only when the existing test does not identify the failure. Run the exact test, the current step's group, every later affected group, and then Step 9. Commit each coherent correction with `git commit -S` and the Codex trailer. Do not edit an assertion or threshold unless the specification changed first.

- [ ] **Step 9: Run the complete search suite from a clean test environment.**

```sh
make test-env-down
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 45m -run '^TestSearch' ./internal/test/integration
make build
```

Expected: every search test passes without a skip, and `make build` passes every repository gate from fresh sources.

- [ ] **Step 10: Repeat the concurrency and replacement tail.**

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=3 -timeout 45m -run '^TestSearch(DistinctNodes|PermissionFilter|Access|Cursor|DelayedWriter|RuntimeUnavailable|Rebuild|Split|Restore)' ./internal/test/integration
make test-env-down
```

Require identical result sets, no duplicate node, no leaked forbidden node, no lost committed work, and no retained test service.

- [ ] **Step 11: Verify Meilisearch removal and branch integrity.**

```sh
rg -n -i 'meili|MEILI_' --glob '!docs/superpowers/**' .
git status --short
git -C "$CONFIGS_ROOT" status --short
git log --show-signature --oneline origin/main..HEAD
git -C "$CONFIGS_ROOT" log --show-signature --oneline origin/main..HEAD
```

Require no live Meilisearch code, dependency, configuration, test environment, container, credential injection, mounted volume, fallback, or operator path. An unmounted preserved old volume may exist and must remain unread. Require clean worktrees. Run `git verify-commit` and inspect `git cat-file commit` for a raw `gpgsig` header on every commit in both `origin/main..HEAD` ranges.

- [ ] **Step 12: Report evidence for release review.**

Report the starting and final commits, every command and exit code, image digests, corrected failures and commits, relevance ranks, traversal counts, retry counts, peak memory, disk use, latency, throughput, oldest work age, and remaining release checks. Do not claim QA capacity, production capacity, or deployed failover before the release plan records it.
