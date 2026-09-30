# Run search acceptance tests

Run search acceptance against disposable FoundationDB, YugabyteDB, and OpenSearch
services. The tests use authenticated public MCP requests and real audit writes.
Local results do not establish deployed QA acceptance or production capacity.

## Prepare the native runner

1. Use a Docker daemon with an `arm64` or `amd64` architecture. The test runner
   installs the matching FoundationDB client. The SQL fixture selects the
   YugabyteDB platform from the daemon architecture and requires a successful SQL
   health check before use. Do not force an amd64 fixture on an arm64 daemon.
2. Reserve the engine resources before starting a test binary. Ordinary search
   fixtures allocate an 8 GiB OpenSearch container. The three-member cluster
   fixture allocates 24 GiB, plus the runner and database services. Run heavy
   groups serially. Do not overlap a throughput measurement with another engine
   fixture, a build, or a database probe.
3. Record the source revision and dirty files. The runner mounts the current
   working tree; the revision alone does not identify uncommitted source.

   ```sh
   git status --short
   export TACK_TEST_SOURCE_REVISION="$(git rev-parse HEAD)"
   export TACK_SEARCH_INTEGRATION=1
   export TACK_SEARCH_CLUSTER=1
   export TACK_TEST_ROOT="$PWD"
   docker compose -f docker-compose.test.yml --profile runner build tests
   ```

The image build workflow builds amd64 and arm64 images on matching native Linux
runners and publishes a manifest from both image digests. A configured workflow
does not establish that a particular image build passed.

## Run a focused group

1. Select the public behavior under test.

   | Test expression | Required evidence |
   | --- | --- |
   | `^TestSearchCursorActualOSProcesses$` | Two distinct server processes alternate cursor pages, resume after a process replacement, and reject revoked membership. |
   | `^TestSearchCluster` | Real proxy distribution, unchanged semantic fields after replica changes, engine recovery, and strict public search availability during each independent member stop. |

2. Run the selected expression with the environment from the preparation step.
   This example validates separate server processes.

   ```sh
   docker compose -f docker-compose.test.yml --profile runner run --rm tests \
       test -count=1 -timeout 30m -v \
       -run '^TestSearchCursorActualOSProcesses$' ./internal/test/integration
   ```

3. Save the complete output and terminal exit code. Record the compiled source,
   image digests and architecture, server PIDs, fixture identities, and cleanup
   result. Preserve the original public error when a test fails. Resource and
   model diagnostics collected after an error do not establish its cause.

The process tests require a 40-character source revision and build the actual
server binary. They use a fresh, unprefixed FoundationDB fixture. They do not
replace a second server process with a second in-process handler.

Verify deployed predictors on every eligible cluster member and matching cached
worker and target identities before the first member stop and after each restart.
Shard health alone does not satisfy this validation requirement. Preserve the
first public-error failure during a member stop even if a later request succeeds.

## Run the complete acceptance gates

1. Follow the ordered groups, complete suite, and repeated tail in the
   [final validation plan](../superpowers/plans/2026-09-19-opensearch-final-validation.md).
   Preserve its timeouts, count requirements, and prohibition on skipped tests.
2. Use `TACK_SEARCH_CLUSTER=1 make test-search` for the standard container search
   target. This target supplies the source revision and search integration
   opt-in, but its 90-minute timeout does not replace the plan's 45-minute gate.
   The host equivalent is `TACK_SEARCH_CLUSTER=1 make test-search-host`; it also
   requires the local FoundationDB client library.
3. Verify the terminal results for every required group. Compilation, a skipped
   cluster group, or a successful startup does not establish search acceptance.
   Require fixed-client throughput measurements with indexed mutations and
   complete search sessions, zero errors, and a two-process gain above
   unchanged-control variation before claiming a speedup.

## Remove abandoned fixtures

1. Check that every active test binary has exited. Normal test teardown removes
   only the resources owned by that binary.
2. Run `make test-env-down` only after confirming that no other test binary owns
   an active fixture. This command removes all test environment engines and their
   network, including resources from other interrupted test runs.
3. Verify that the recorded fixture containers and processes are absent. Keep
   production and QA services outside this cleanup procedure.
