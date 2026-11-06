# OpenSearch Live Validation and Correction Implementation Plan

> Agentic workers must implement this plan task by task with the `superpowers:subagent-driven-development` skill (recommended) or the `superpowers:executing-plans` skill. Steps use checkbox (`- [ ]`) syntax for tracking.

This plan validates the completed OpenSearch implementation against real dependencies and corrects every failure on its owning branch. It leaves one reviewed Tack stack and one Configs pull request ready for authorized QA deployment. It validates the implementation against the [search acceptance criteria](../specs/2026-09-19-search-acceptance.md).

Luna completes and submits the Tack Graphite stack plus the independent Configs pull request without running the live suite. Sol starts the pinned OpenSearch and FoundationDB environment from the stack tip, validates each dependency layer in order, corrects failures on the owning Graphite branch, restacks its descendants, then repeats the affected tail and complete suite. The release plan separately validates deployed QA and production.

The work uses Go, the Docker Compose test runner, FoundationDB 7.4.6, OpenSearch 3.8.0, the official OpenSearch Go client v4.7.3, RSpec, and OpenTofu.

## Global Constraints

Start only after Graphite has submitted every Tack slice, the independent Configs pull request exists, and both worktrees are clean. Require `CONFIGS_ROOT` to identify the reviewed Configs checkout. Use real dependencies and public boundaries. Do not use mocks, skip tests, weaken assertions, raise accepted limits, or replace the selected design to make a test pass. Correct implementation defects directly. Stop with evidence when a failure requires a design or acceptance change. Do not apply OpenTofu, deploy QA or production, delete volumes, or change branch rules. Record the starting stack, Configs commit, exact commands, image digests, failures, corrections, measurements, and final commits for every affected pull request.

## Review Focus

Test complete Unicode page coverage, stale content and access writers, forbidden
matches before ranking, corrupt indexed access with final authorization,
permission-version transition without inference or replacement, duplicate-heavy
traversal, lost responses, replacement during mutations, engine outage recovery,
metadata refresh, and single-node restart.

---

### Task 1: Run live validation and correct the completed implementation

Modify a Tack or Configs file only when a reproduced failure requires a correction, and only in a file that a preceding serial plan owns. This task runs the tests in these files:

- `internal/test/integration/search_native_test.go`
- `internal/test/integration/search_projection_backfill_test.go`
- `internal/test/integration/search_reader_test.go`
- `internal/test/integration/search_work_test.go`
- `internal/test/integration/search_recovery_test.go`
- `internal/test/integration/search_ranking_test.go`
- `internal/test/integration/search_permission_filter_test.go`
- `internal/test/integration/search_auth_test.go`
- `internal/test/integration/search_cursor_test.go`
- `internal/test/integration/search_rebuild_test.go`
- `internal/test/integration/search_runtime_test.go`
- `internal/test/integration/search_metadata_refresh_test.go`
- `internal/test/integration/search_access_refresh_test.go`
- `internal/test/integration/search_datagen_test.go`
- `internal/test/integration/search_cluster_test.go`
- `internal/test/integration/search_os_process_test.go`
- `internal/test/integration/search_os_process_throughput_test.go`

This task has these prerequisites and outputs:

- This plan requires the committed code and authored tests from every preceding serial plan.
- This plan leaves a corrected signed Tack stack, a corrected Configs pull request, complete real-dependency results, resource measurements, and the evidence the release plan requires.

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
export TACK_TEST_SOURCE_REVISION="$(git rev-parse HEAD)"
export TACK_SEARCH_INTEGRATION=1
export TACK_SEARCH_CLUSTER=1
make test-env-down
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner build tests
```

Require an empty status before testing. Record the test-runner image digest and the OpenSearch 3.8.0 image digest after the build.

Confirm that no other test binary owns an active fixture before each
`make test-env-down`. Run heavy groups serially. Export the same source
revision and opt-ins for every runner command below. The actual server process
tests require the revision and use a fresh, unprefixed FoundationDB fixture.

- [ ] **Step 2: Validate the official client, model, mapping, and complete page embedding.**

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearchNative' ./internal/test/integration
```

Require the pinned hashes, typed client operations, strict generic access mapping,
access-only scripted bulk update ordered by `search_generation`, full 4,096-byte Unicode source,
generated final chunk, finite sparse weights, access-only success with the model
undeployed, explicit engine failures, and 8 GiB success. Require the 8 GiB
model-guest minimum in the
[Configs search guest settings](https://github.com/agoodkind/configs/blob/main/ansible/inventory/group_vars/all/search_cluster.yml)
for the QA and production guests. The original validation recorded a memory
circuit breaker at 4 GiB. That failure is historical evidence for the 8 GiB
minimum. Current 4 GiB workloads complete without a breaker response, and no
test covers the 4 GiB case.

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
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(SemanticPairs|ContinuationTraversesDuplicateHeavyCorpus|FiltersForbiddenPagesBeforeRanking|FinalCheckRejectsCorruptIndexedAccess|RevokedAccessRejectsLaterResultsAndReplay|AuthenticatedResults|UnavailableWhileDisabled|Cursor|EmptyQuery)' ./internal/test/integration
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

Run the actual server process checks against fresh disposable fixtures. Require
distinct PIDs, alternating cursor continuation, process replacement, and revoked
membership rejection. Measure throughput separately with fixed clients and
unchanged controls; require zero errors and a gain above control variation.

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearchCursorActualOSProcesses$' ./internal/test/integration
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 45m -run '^TestSearchActualOSProcessThroughput$' ./internal/test/integration
```

Do not overlap throughput measurements with another engine fixture, build, or
database probe. Save the complete output and terminal exit code.

Run the throughput test on the Mac runner, not in CI. A hosted CI VM runs both
Tack processes, FoundationDB, and OpenSearch on one machine. On that VM the
second process cut the time to indexed completion from about 15 s to 3.5 s and
raised request p95 from about 640 ms to 840 ms, and the total rate stayed flat
(CI jobs 110765693590 and 110795755897). CI job 110795755897 records that hosted-runner
limitation and is not a pass. Moving the test off CI does not close throughput
acceptance. Acceptance requires two results: the unchanged throughput test on
the Mac runner and the QA two-process measurement. Each must show that the added
Tack process improves both indexing and public search, and each must record the
baseline spread, request p95, search work backlog, and the machine CPU count,
memory, and cgroup limits.

- [ ] **Step 7: Validate disposable cluster configuration and scale-out behavior.**

```sh
git -C "$CONFIGS_ROOT" diff --check
(cd "$CONFIGS_ROOT" && bundle exec rspec spec/ansible/tack_search_spec.rb spec/ansible/tack_search_proxy_spec.rb)
(cd "$CONFIGS_ROOT" && ./configsctl tofu validate)
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearchCluster' ./internal/test/integration
```

Require one stable client endpoint, one-member restart recovery, later member joining without a new bootstrap cluster, proxy distribution, model placement, replica allocation, and one-member failure behavior. Do not connect to live Proxmox or apply a plan.

Verify that every eligible predictor is deployed. Verify that cached worker and
target identities match before the first independent member stop and after
each restart. Save the first public error during each member stop, even if later
requests succeed.

- [ ] **Step 8: Correct each reproduced failure at its owning layer.**

For each failure, rerun the smallest exact test until it fails consistently. Trace the production path. Check out the Graphite branch that owns the behavior. Correct only that slice and add a regression only when the existing test does not identify the failure. Stage the named files. Use Graphite MCP `modify` for a direct correction. Use `absorb --dry-run` and review the destination before `absorb --force` when one change spans existing slices. Restack from the corrected branch through its upstack. Verify every rewritten signature. Preview and submit the complete stack again. Run the exact test, the current step's group, every later affected group, and then Step 9. Correct Configs failures in its independent pull request through the normal signed-commit workflow. Do not edit an assertion or threshold unless the specification changed first.

- [ ] **Step 9: Run the complete search suite from a clean test environment.**

Run the suite as eight fixed groups. Set each group limit to its slowest complete CI measurement times 1.5, rounded up to 5 minutes. Use 45 minutes for `rebuild-split-and-restore` because the Makefile sets that limit and the table measurement predates it. Keep the 75 minute limits for `remaining-features` and `runtime-and-session-audit`, although the formula gives 90 minutes for their measurements.

The measurements come from these OpenSearch CI jobs:

- 110765693590
- 109857411134
- 117516233280
- 123145940911
- 123131420136
- 123132129545
- 123110954409
- 123140740521
- 123142813381
- 123071806333
- The completed groups of 110045603547

| Group | Selection | Tests | Slowest measurement | Limit |
| --- | --- | --- | --- | --- |
| continuation-with-duplicate-pages | `^TestSearchContinuationTraversesDuplicateHeavyCorpus$` | 1 | 2388.1 s (110045603547) | 60m |
| throughput-with-server-processes | `^TestSearchActualOSProcessThroughput$` | 1 | 1062.7 s (110045603547) | 30m |
| rebuild-split-and-restore | `^TestSearch(Split|Restore|Rebuild)` | 20 | 1122.4 s (110765693590) | 45m |
| cluster-failures-and-recovery | `^TestSearchCluster`, skipping `^TestSearchClusterModelRepair` | 6 | 955.4 s on the Mac runner | 25m |
| features-a-through-m | `^TestSearch[A-M]`, skipping `SEARCH_SEPARATE_FEATURE_TESTS` | 87 | 2862.2 s (123145940911) | 75m |
| remaining-features | `^TestSearch($|[^A-M])`, skipping `SEARCH_SEPARATE_FEATURE_TESTS` and `SEARCH_RUNTIME_AND_SESSION_AUDIT_TESTS` | 120 (121 listed; `TestSearchSDKCompatibility` skips in the group run) | 59:47 job time; 59:21 `go test` step time in CI run 41510801397. | 75m |
| runtime-and-session-audit | `^TestSearch(RuntimeAudit|SessionAudit)` | 76 | 58:06 job time; 57:44 `go test` step time in CI run 41510801397, after two tests moved to remaining-features. | 75m |
| model-repair-enablement | `^TestSearchClusterModelRepair` | 1 | 129.67 s on the Mac runner (2026-10-02, head 650f19ac) | 5m |

The feature partitions select every search test outside the dedicated continuation, throughput, rebuild, split, restore, cluster recovery, and model repair groups. `features-a-through-m` selects A through M immediately after `TestSearch`. `runtime-and-session-audit` selects names that start with `TestSearchRuntimeAudit` or `TestSearchSessionAudit`. `remaining-features` selects every other feature test. A new search test runs in one feature partition according to its name unless a change assigns it to a dedicated group.

CI runs the continuation group, the rebuild, split, and restore group, and every feature partition as parallel jobs. Run `throughput-with-server-processes`, `cluster-failures-and-recovery`, and `model-repair-enablement` on the Mac runner. Acceptance requires those Mac runs on the head that merges, with `TACK_SEARCH_CLUSTER=1` for both cluster groups.

```sh
make test-env-down
make test-search-group GROUP=continuation-with-duplicate-pages
make test-search-group GROUP=rebuild-split-and-restore
make test-search-group GROUP=features-a-through-m
make test-search-group GROUP=remaining-features
make test-search-group GROUP=runtime-and-session-audit
make build BINARY=testenv CMD=./cmd/testenv
make test-search-group TEST_SEARCH_RUNNER=mac GROUP=throughput-with-server-processes \
    MAC_RUNNER_IMAGE="sha256:<local-runner-image-id>" \
    MAC_RUNNER_EVIDENCE_DIR="/private/tmp/tack-search-throughput-with-server-processes-$(date +%Y%m%d-%H%M%S)"
TACK_SEARCH_CLUSTER=1 make test-search-group TEST_SEARCH_RUNNER=mac GROUP=cluster-failures-and-recovery \
    MAC_RUNNER_IMAGE="sha256:<local-runner-image-id>" \
    MAC_RUNNER_EVIDENCE_DIR="/private/tmp/tack-search-cluster-failures-and-recovery-$(date +%Y%m%d-%H%M%S)"
TACK_SEARCH_CLUSTER=1 make test-search-group TEST_SEARCH_RUNNER=mac GROUP=model-repair-enablement \
    MAC_RUNNER_IMAGE="sha256:<local-runner-image-id>" \
    MAC_RUNNER_EVIDENCE_DIR="/private/tmp/tack-search-model-repair-enablement-$(date +%Y%m%d-%H%M%S)"
make build
```

Before the first group run, confirm group membership against the compiled package. `go test -list` ignores `-skip`. Build the lists with the `runtimeaudit` tag, which the feature partitions use. Pass `-run '^TestSearch'` with `-list` because a listing without `-run` starts the Mailpit containers in `TestMain`. Apply `SEARCH_SEPARATE_FEATURE_TESTS` to both alphabetical feature lists, remove `SEARCH_RUNTIME_AND_SESSION_AUDIT_TESTS` from the remaining-feature list, and select the runtime and session audit tests separately.

```sh
go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch' | grep '^TestSearch' | sort > all.txt
go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch(ContinuationTraversesDuplicateHeavyCorpus|ActualOSProcessThroughput)$|^TestSearch(Split|Restore|Rebuild|Cluster)' | grep '^TestSearch' | sort > dedicated-behavior-tests.txt
go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch[A-M]' | grep '^TestSearch' | grep -v -E '^(TestSearchContinuationTraversesDuplicateHeavyCorpus|TestSearchActualOSProcessThroughput|TestSearchSplit.*|TestSearchRestore.*|TestSearchRebuild.*|TestSearchCluster.*)$' | sort > features-a-through-m.txt
go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch($|[^A-M])' | grep '^TestSearch' | grep -v -E '^(TestSearchContinuationTraversesDuplicateHeavyCorpus|TestSearchActualOSProcessThroughput|TestSearchSplit.*|TestSearchRestore.*|TestSearchRebuild.*|TestSearchCluster.*)$|^TestSearch(RuntimeAudit|SessionAudit)' | sort > remaining-features.txt
go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch(RuntimeAudit|SessionAudit)' | grep '^TestSearch' | sort > runtime-and-session-audit.txt
sort dedicated-behavior-tests.txt features-a-through-m.txt remaining-features.txt runtime-and-session-audit.txt | uniq -d
sort dedicated-behavior-tests.txt features-a-through-m.txt remaining-features.txt runtime-and-session-audit.txt | diff - all.txt
```

Require an empty duplicate list and an empty difference: every search test runs in exactly one group.

Require every search test to pass without a skip. Require `make build` to pass every repository gate from fresh sources.

- [ ] **Step 10: Repeat the concurrency and replacement tail.**

Run the same 26 tests as three groups, each with count 3. These limits are derived, not measured: each is three times the slowest single CI run of the group's tests, times 1.5. Record the actual duration of each group in the first local tail run against its limit.

| Group | Tests | Slowest single CI run | Limit |
| --- | --- | --- | --- |
| repeat-continuation-with-duplicate-pages | `TestSearchContinuationTraversesDuplicateHeavyCorpus` | 2388.1 s (110045603547) | 180m |
| repeat-rebuild-split-and-restore | The seven `TestSearchRebuild`, `TestSearchRestore`, and `TestSearchSplit` tail tests | 1122.4 s (110765693590) | 85m |
| repeat-access-cursors-and-workers | The 18 remaining tail tests | 632.7 s (110765693590) | 50m |

Use `SEARCH_REPEAT_REPLACEMENT_TESTS` and `SEARCH_REPEAT_ACCESS_CURSOR_WORKER_TESTS` for the exact repeated replacement and access, cursor, and worker selections.

```sh
make test-search-group GROUP=repeat-continuation-with-duplicate-pages
make test-search-group GROUP=repeat-rebuild-split-and-restore
make test-search-group GROUP=repeat-access-cursors-and-workers
make test-env-down
```

Require all 26 selected tests to complete three times, for 78 executions.
Require identical result sets, no duplicate node, no leaked forbidden node, no lost committed work, and no test service left running.

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
