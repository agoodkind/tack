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
   | `^TestSearchActualOSProcessThroughput$` | Fixed clients complete indexed mutations and search sessions with zero errors; the measured two-process gain exceeds unchanged-control variation. |
   | `^TestSearchCluster` | Real proxy distribution, unchanged semantic fields after replica changes, engine recovery, and strict public search availability during each independent member stop. |

2. Run the selected expression with the environment from the preparation step.
   This example validates separate server processes.

   ```sh
   docker compose -f docker-compose.test.yml --profile runner run --rm tests \
       test -count=1 -timeout 30m -v \
       -run '^TestSearchCursorActualOSProcesses$' ./internal/test/integration
   ```

   On macOS, use an existing local runner image and save each invocation in a
   new evidence directory. The command runs the current checkout and checks
   fixture cleanup before it exits.

   ```sh
   uv run --with pydantic --python 3.14 python scripts/test-search-mac.py \
       --root "$PWD" --image "sha256:<local-runner-image-id>" \
       --run '^TestSearchCursorActualOSProcesses$' --count 1 --timeout 30m \
       --evidence-dir "/private/tmp/tack-search-$(date +%Y%m%d-%H%M%S)"
   ```

   Get the immutable image ID with
   `docker image inspect tack-tests:latest --format '{{.Id}}'` and replace the
   placeholder. The command does not build or pull an image. Create the evidence
   directory outside the checkout.

3. Save the complete output and terminal exit code. Record the compiled source,
   image digests and architecture, server PIDs, fixture identities, and cleanup
   result. Save the original public error when a test fails. Resource and
   model diagnostics collected after an error do not establish its cause.

The process tests require a 40-character source revision and build the actual
server binary. They use a fresh, unprefixed FoundationDB fixture. They do not
replace a second server process with a second in-process handler.

Verify deployed predictors on every eligible cluster member and matching cached
worker and target identities before the first member stop and after each restart.
Shard health alone does not satisfy this validation requirement. Save the first
public error during a member stop, even if a later request succeeds.

## Run the complete acceptance gates

1. Follow the ordered groups, complete suite, and repeated tail in the
   [final validation plan](../superpowers/plans/2026-09-19-opensearch-final-validation.md).
   Use its timeouts and count requirements unchanged, and allow no skipped test.
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

## Prepare the required QA cohorts

1. Select a QA or local application environment with an existing datagen seed
   workspace and the published fixture creator. Set `TACK_DATAGEN_ALLOW_TARGET`
   to `qa` or `local` in that environment. The command rejects configured
   production endpoints before loading the corpus or creating fixture nodes.
2. Set `TACK_DATAGEN_SEED` to that workspace's seed and set `OPERATOR_ID`,
   `OPERATOR_EMAIL`, and `OPERATOR_NAME` to the audited operator identity.
   Use the approved [semantic corpus](../../internal/test/integration/testdata/search_semantic_corpus.json)
   from the checked-out source.
3. Export the stored fixture identities. Public search may remain disabled;
   preparation does not start workers or send search requests.

   ```sh
   docker compose run --rm \
       -v "$PWD/internal/test/integration/testdata/search_semantic_corpus.json:/manifests/search_semantic_corpus.json:ro" \
       app --execute --output json \
       --operator-id "$OPERATOR_ID" --operator-email "$OPERATOR_EMAIL" \
       --operator-name "$OPERATOR_NAME" ops qa datagen search \
       --prepare-only --commit --seed "$TACK_DATAGEN_SEED" \
       --corpus /manifests/search_semantic_corpus.json > search-manifest.json
   ```

4. Read `result.manifest` in the saved response. Use its stored node IDs,
   opaque types, projection definitions, and expected case IDs for the isolated
   semantic, continuation, and final-page checks. Require 162, 31, and one node
   in the respective cohorts. A prepared manifest does not establish public
   relevance, continuation, or capacity acceptance. Save any partial manifest
   when the command exits with an error.

## Remove abandoned fixtures

1. Check that every active test binary has exited. Normal test teardown removes
   only the resources owned by that binary.
2. Run `make test-env-down` only after confirming that no other test binary owns
   an active fixture. This command removes all test environment engines and their
   network, including resources from other interrupted test runs.
3. Verify that the recorded fixture containers and processes are absent. Do not
   run this procedure against production or QA services.
