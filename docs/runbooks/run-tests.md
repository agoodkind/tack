# Run tests

Run tests in QA or Docker containers. Production tests require explicit operator
authorization. Start Docker before running local container tests.

## Run container tests

1. Read the package selections in the [Makefile](../../Makefile) and the runner
   mounts in [docker-compose.test.yml](../../docker-compose.test.yml).
2. Run `make test-unit` for the selected ops, PostgreSQL adapter, audit, and
   service packages.
3. Run `make test-integration` for the integration and other store packages.
   The target builds the runner image and runs tests inside the container.
4. Inspect the command result and test failures before treating the run as
   successful.

The runner connects to FoundationDB through its Docker network. Docker
Desktop's macOS port forwarder drops FoundationDB's connection-packet exchange.
The standard local FoundationDB helper starts a single-node store; tests that
need several processes request their own cluster. These test networks do not
validate the production IPv6-only bridge.

`make test` excludes the integration package. CI runs that package separately
with `make test-store-host-shard` and an explicit shard selection from the
Makefile. The host target requires the FoundationDB client library and Docker.

## Remove engines after an interrupted test

The [test environment helpers](../../internal/testenv/) start the required
stores and remove engines that the test process owns when the process exits.
Ownership labels distinguish engines of active tests from engines of exited
processes.

1. After an interrupted runner leaves engines behind, run `make test-env-down`.
2. Inspect cleanup errors. The command preserves engines of running tests and
   engines without ownership labels.

## Exercise a changed user path on QA

Add generator coverage in the same change when a node type, property type,
relationship shape, audit verb, or user-visible operation changes. Run the
generator in a one-shot `app` container for its FoundationDB connection. Supply
the generator's audit credentials to that command, including `AUDIT_READER_DSN`
for the committed seed's ledger checks. Do not add ledger reader credentials
to the running application.

1. Confirm the QA or local target and its credentials. Set
   `TACK_DATAGEN_ALLOW_TARGET` to exactly `qa` or `local` for writes. Leave the
   production marker empty.
2. Supply the actual operator identity on each command with `--operator-id`,
   `--operator-email`, and `--operator-name`. Agent commands also require
   `--operator-service` and `--operator-session`. A one-shot container does not
   inherit the running app's `serve` arguments.
3. Review the command dry run without `--execute`. To inspect the generator's
   planned data, use `--execute` without `--commit`. Neither mode requires the
   product-write marker.
4. Run `ops qa datagen seed` through the one-shot `app` service with
   `--scale small`, `--execute`, and `--commit` after confirming the target.
5. Run `ops qa datagen soak` through that service with `--duration 60s`,
   `--rate 3`, `--execute`, and `--commit`.
6. Inspect the recorded outcomes and permanent audit evidence required by the
   changed operation. A successful process exit alone does not establish audit
   coverage.
