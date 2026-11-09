# Run search acceptance tests

Use disposable FoundationDB, YugabyteDB, and OpenSearch services for local acceptance through authenticated public MCP requests with real audit writes. Run QA checks only in the selected QA environment. Production tests require explicit operator authorization. Local results do not establish deployed acceptance or production capacity.

## Prepare the runner

1. Use a Docker daemon with an `arm64` or `amd64` architecture. The runner installs the matching FoundationDB client. The SQL fixture selects its YugabyteDB platform from the daemon architecture and requires SQL health before use. Do not force an amd64 fixture on an arm64 daemon.
2. Reserve resources before starting a test binary. The OpenSearch memory allocations differ between ordinary and three-member fixtures. Run heavy groups serially and keep throughput measurements separate from other fixtures, builds, and database probes.

   OpenSearch memory allocations:

   | Ordinary fixture | Three-member fixture |
   | --- | --- |
   | 8 GiB. | 24 GiB plus runner and database resources. |

3. Record revision and dirty files. The runner mounts the working tree. A revision alone does not identify uncommitted source. Set the runner inputs and build its image.

   ```sh
   git status --short
   export TACK_TEST_SOURCE_REVISION="$(git rev-parse HEAD)"
   export TACK_SEARCH_INTEGRATION=1
   export TACK_SEARCH_CLUSTER=1
   export TACK_TEST_ROOT="$PWD"
   docker compose -f docker-compose.test.yml --profile runner build tests
   ```

4. Record runner and engine image digests and architecture. For each application image, the workflow combines native amd64 and arm64 builds into one manifest. Its configuration does not prove that a particular image build passed.
5. On macOS, build the native executable before the measurement window. Select an existing immutable local image ID with `docker image inspect tack-tests:latest --format '{{.Id}}'`. Set `MAC_RUNNER_IMAGE` to that ID and set `MAC_RUNNER_EVIDENCE_DIR` to a new absolute path outside the checkout for each invocation. The native runner checks fixture cleanup before it exits. It does not build or pull images.

   ```sh
   make build BINARY=testenv CMD=./cmd/testenv
   ```

## Run a focused behavior

1. Select the public behavior. The process tests require a 40-character revision and build actual server processes against a fresh unprefixed FoundationDB fixture. Two in-process handlers do not satisfy that check. Each expression must produce the stated observations.

   Behavior expressions:

   | Expression | Required observation |
   | --- | --- |
   | `^TestSearchCursorActualOSProcesses$` | Distinct server PIDs alternate pages, resume after process replacement, and reject revoked membership. |
   | `^TestSearchActualOSProcessThroughput$` | Fixed clients complete indexed mutations and full sessions without errors. The two-process gain exceeds unchanged-control variation. |
   | `^TestSearchCluster` | A real proxy distributes requests. Replica changes preserve semantic fields. Independent member stops preserve required public availability. |

2. Run the selected expression. This container example checks separate server processes.

   ```sh
   docker compose -f docker-compose.test.yml --profile runner run --rm tests \
       test -count=1 -timeout 30m -v \
       -run '^TestSearchCursorActualOSProcesses$' ./internal/test/integration
   ```

   This macOS example uses an existing image and a new evidence directory.

   ```sh
   ./dist/testenv run-mac \
       --root "$PWD" --image "sha256:<local-runner-image-id>" \
       --evidence-dir "/private/tmp/tack-search-$(date +%Y%m%d-%H%M%S)" -- \
       -run '^TestSearchCursorActualOSProcesses$' -count 1 -timeout 30m
   ```

3. Save complete output and terminal exit code. Record compiled source, server PIDs, image identities, fixture identities, measurements, and cleanup. Preserve the first public error. Diagnostics collected afterward do not establish its cause.

## Run every required group

Before running the host inventory commands below, install the Go toolchain selected by [go.mod](../../../go.mod), a C compiler for cgo, and the host architecture's FoundationDB client headers and library compatible with [the test image](../../../Dockerfile.test). Enable cgo and make the library available to the host linker and loader. Building the container installs its client only inside the image.

1. Run the following pipelines in a shell with `pipefail` enabled and stop on any command error. Require a nonempty compiled inventory before comparing the groups. Use the `runtimeaudit` tag for feature partitions. When listing tests, supply `-run '^TestSearch'`. A listing without it can start unrelated Mailpit fixtures. `go test -list` ignores `-skip`, so apply exclusions to the resulting lists.

   ```sh
   go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch' | grep '^TestSearch' | sort > all.txt
   go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch(ContinuationTraversesDuplicateHeavyCorpus|ActualOSProcessThroughput)$|^TestSearch(Split|Restore|Rebuild|Cluster)' | grep '^TestSearch' | sort > dedicated-behavior-tests.txt
   go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch[A-M]' | grep '^TestSearch' | grep -v -E '^(TestSearchContinuationTraversesDuplicateHeavyCorpus|TestSearchActualOSProcessThroughput|TestSearchSplit.*|TestSearchRestore.*|TestSearchRebuild.*|TestSearchCluster.*)$' | sort > features-a-through-m.txt
   go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch($|[^A-M])' | grep '^TestSearch' | grep -v -E '^(TestSearchContinuationTraversesDuplicateHeavyCorpus|TestSearchActualOSProcessThroughput|TestSearchSplit.*|TestSearchRestore.*|TestSearchRebuild.*|TestSearchCluster.*)$|^TestSearch(RuntimeAudit|SessionAudit)' | sort > remaining-features.txt
   go test -tags=runtimeaudit ./internal/test/integration -run '^TestSearch' -list '^TestSearch(RuntimeAudit|SessionAudit)' | grep '^TestSearch' | sort > runtime-and-session-audit.txt
   sort dedicated-behavior-tests.txt features-a-through-m.txt remaining-features.txt runtime-and-session-audit.txt | uniq -d
   sort dedicated-behavior-tests.txt features-a-through-m.txt remaining-features.txt runtime-and-session-audit.txt | diff - all.txt
   ```

2. Compare group membership with the compiled search test inventory from the current [Makefile](../../../Makefile). Require an empty duplicate list and an exact union with all search tests.
3. Run `make test-integration`, then run the search groups in dependency order. Correct a reproduced failure at its owning implementation before repeating the affected groups. Preserve accepted assertions and bounds.

   1. Test the native client, model, mapping, and chunks.
   2. Test metadata, readers, and work.
   3. Test stale writers and retirement.
   4. Test ranking, permissions, and continuation.
   5. Test replacement, runtime, and refresh.
   6. Test public datagen and cluster behavior.

4. Run the complete groups below. The current target sets each selection and timeout. A group with a skipped required scenario does not supply acceptance evidence. The ordinary group budget uses 1.5 times the slowest complete CI measurement, rounded up to five minutes. The Makefile sets the replacement group to 45 minutes and the remaining feature and audit groups to 75 minutes.

   Each group uses the indicated runner and time limit.

   Required groups:

   | Group | Runner | Runs | Limit | Selection |
   | --- | --- | --- | --- | --- |
   | `continuation-with-duplicate-pages` | Container | 1 | 60m | `^TestSearchContinuationTraversesDuplicateHeavyCorpus$` |
   | `throughput-with-server-processes` | Mac | 1 | 30m | `^TestSearchActualOSProcessThroughput$` |
   | `rebuild-split-and-restore` | Container | 1 | 45m | `^TestSearch(Split\|Restore\|Rebuild)` |
   | `cluster-failures-and-recovery` | Mac | 1 | 25m | `^TestSearchCluster`, except `^TestSearchClusterModelRepair` |
   | `features-a-through-m` | Container | 1 | 75m | `^TestSearch[A-M]`, except `SEARCH_SEPARATE_FEATURE_TESTS` |
   | `remaining-features` | Container | 1 | 75m | `^TestSearch($\|[^A-M])`, except `SEARCH_SEPARATE_FEATURE_TESTS` and `SEARCH_RUNTIME_AND_SESSION_AUDIT_TESTS` |
   | `runtime-and-session-audit` | Container | 1 | 75m | `^TestSearch(RuntimeAudit\|SessionAudit)` |
   | `model-repair-enablement` | Mac | 1 | 5m | `^TestSearchClusterModelRepair` |

   ```sh
   make test-search-group GROUP=continuation-with-duplicate-pages
   make test-search-group GROUP=rebuild-split-and-restore
   make test-search-group GROUP=features-a-through-m
   make test-search-group GROUP=remaining-features
   make test-search-group GROUP=runtime-and-session-audit
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

5. Run the repeated concurrency and replacement tail after the corresponding correction. Require every group to complete the runs in the table. Record actual durations against the limits below. The limits are estimates from three times the slowest single run and a factor of 1.5. The Makefile sets `SEARCH_REPEAT_REPLACEMENT_TESTS` and `SEARCH_REPEAT_ACCESS_CURSOR_WORKER_TESTS` for the repeated replacement and access, cursor, and worker groups. Each repeated group selects the corresponding tests and time limit.

   Repeated groups:

   | Group | Tests per run | Runs | Executions | Limit |
   | --- | ---: | ---: | ---: | --- |
   | Continuation | 1 | 3 | 3 | 180m |
   | Replacement | 7 | 3 | 21 | 85m |
   | Access, cursor, and worker | 18 | 3 | 54 | 50m |
   | All groups | 26 | 3 | 78 | Each group's limit applies. |

   ```sh
   make test-search-group GROUP=repeat-continuation-with-duplicate-pages
   make test-search-group GROUP=repeat-rebuild-split-and-restore
   make test-search-group GROUP=repeat-access-cursors-and-workers
   ```

6. Require identical result sets, absence of duplicates and forbidden nodes, preservation of committed work, and removal of owned test resources. Save corrections, exact rerun commands, and final commits. Require all groups on the implementation revision that merges.

The aggregate `TACK_SEARCH_CLUSTER=1 make test-search` target supplies revision and integration opt-in with a 90-minute timeout. Its host equivalent, `TACK_SEARCH_CLUSTER=1 make test-search-host`, also needs the FoundationDB client library. Neither aggregate replaces the required groups and their limits.

The throughput test requires a Mac run and a separate QA two-process measurement. Each must show improvement for indexing and public search, with baseline spread, p95, backlog, CPU count, memory, and cgroup limits. Hosted CI measurements did not establish that improvement. Neither this test nor ordinary soak measures search capacity.

Before independent member stops and after each restart, verify deployed predictors on every eligible member and matching cached worker and target identities. Preserve the first public error even when a later request succeeds.

## Isolate dependency-layer failures

Run the smallest failing layer against the same disposable fixtures and revision before repeating the complete groups. Use these commands for the focused checks.

```sh
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearchNative' ./internal/test/integration
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(Projection|Reader|Work)' ./internal/test/integration
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(DelayedWriter|RevisionCleanup|WorkerFairness|Recovery)' ./internal/test/integration
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(SemanticPairs|ContinuationTraversesDuplicateHeavyCorpus|FiltersForbiddenPagesBeforeRanking|FinalCheckRejectsCorruptIndexedAccess|RevokedAccessRejectsLaterResultsAndReplay|AuthenticatedResults|UnavailableWhileDisabled|Cursor|EmptyQuery)' ./internal/test/integration
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearch(Rebuild|Split|Restore|Runtime|Metadata|Access|Datagen)' ./internal/test/integration
TACK_TEST_ROOT="$PWD" docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 30m -run '^TestSearchCursorActualOSProcesses$' ./internal/test/integration
```

## Prepare the QA cohorts

1. Select a QA or local application environment with an existing datagen seed workspace and fixture creator. Set `TACK_DATAGEN_ALLOW_TARGET` to `qa` or `local`. The production-target guard must reject production before fixture metadata and node writes.
2. Set `TACK_DATAGEN_SEED` to the existing workspace seed. Set `OPERATOR_ID`, `OPERATOR_EMAIL`, and `OPERATOR_NAME` to the audited operator identity. Use the checked-out [semantic corpus](../../../internal/test/integration/testdata/search_semantic_corpus.json).
3. Export the stored fixture identities. Public search may remain disabled. Preparation does not start workers or send search requests.

   ```sh
   docker compose run --rm \
       -v "$PWD/internal/test/integration/testdata/search_semantic_corpus.json:/manifests/search_semantic_corpus.json:ro" \
       app --execute --output json \
       --operator-id "$OPERATOR_ID" --operator-email "$OPERATOR_EMAIL" \
       --operator-name "$OPERATOR_NAME" ops qa datagen search \
       --prepare-only --commit --seed "$TACK_DATAGEN_SEED" \
       --corpus /manifests/search_semantic_corpus.json > search-manifest.json
   ```

4. Read `result.manifest`. Verify actual stored IDs, opaque types, projections, and expected case IDs. Verify each cohort's count. Preserve partial output after an error. Preparation does not establish relevance, continuation, final-page behavior, or capacity.

   QA cohort counts:

   | Semantic nodes | Continuation nodes | Final-page nodes |
   | ---: | ---: | ---: |
   | 162 | 31 | 1 |

5. After public search activates, supply `AUDIT_READER_DSN` to the ordinary committed verification command. Run the guarded public checks and inspect the permanent ledger for the session verbs and completed deletion of the abandoned session header.
6. Exercise the datagen driver's cancellation and dry-run paths. Check its request IDs and attempted-call count. Verify that authenticated JSON-RPC calls use the session headers and that JSON and SSE responses stay within the byte limit.
7. Shorten a searchable value. Require the old term to disappear and the new term to appear. Delete the node and require it to disappear from results. Verify that searching for explicitly excluded text does not return the node containing it.
8. Traverse every continuation in the prepared cohort. Reject repeated cursors and duplicate IDs. Check every page against the configured byte and result limits, with at most 25 IDs per page. Compare the complete distinct ID set with the manifest.
9. Request five rows from a generated collection with more than five matching nodes. Use the printed cursor to request the next page and reject any repeated reference.
10. In the disposable outage test, verify the unavailable response and recovery under the [durable work acceptance criteria](../../reference/search/2026-09-19-search-acceptance.md#durable-work-and-state-lifecycle).
11. Exercise the production-target guard in a disposable fixture. Require rejection before any fixture write and verify that the fixture organization does not exist.

## Remove abandoned fixtures

1. Record the resource identities from the interrupted run. Normal teardown removes only that process's owned resources.
2. Run `make test-env-down` against the local test environment. It removes engines that `cmd/testenv` started and resources of test processes that have exited. It preserves active test-process resources, resources without owner labels, and resources for which it cannot verify owner exit. It removes the shared network only when containers no longer use it.
3. Verify that the recorded abandoned containers and processes no longer exist. Report cleanup failures. Do not run this procedure against QA or production services.
