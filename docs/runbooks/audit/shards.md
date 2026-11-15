# Change the audit shard count

Changing `ShardCount` stops the app and its MCP interface from step 2 until
the deployment in step 4 restarts the app. The app
computes each event's shard before publishing it to Kafka. The consumer
recomputes the shard before writing the event to the ledger. Drain Kafka before
deploying the new count to prevent the consumer from assigning queued events
to different shards.

Existing rows retain their shards and verifiable hash chains. Only later events
use the new count; this procedure does not rewrite ledger rows.

## Prerequisites

- Choose a power of two for the new count.
- Announce the outage before stopping the app and its MCP interface.
- Supply `--operator-id` and `--operator-email` for every `ops` and `audit`
  command below.

## Steps

1. Set `ShardCount` in [canonical.go](../../../internal/audit/canonical.go) to
   the new count. Update the expected shard for the test pair in
   [canonical_test.go](../../../internal/audit/canonical_test.go) and merge
   through the normal pull request process.
2. Stop the app from the install directory on the app guest:

   ```bash
   docker compose stop app
   ```

3. Read the consumer's lag gauge from its network namespace. The consumer image
   does not include a shell. Substitute its `AUDIT_CONSUMER_METRICS_ADDR` value:

   ```bash
   nsenter -t "$(docker inspect --format '{{.State.Pid}}' tack-audit-consumer-1)" -n \
     curl -s http://<AUDIT_CONSUMER_METRICS_ADDR>/debug/vars | jq '.tack_audit_consumer_lag_messages'
   ```

   Wait until every partition reports 0. A positive value indicates queued
   events that the new consumer could assign to different shards.
4. Deploy the merged build through the normal deploy process. The deploy
   recreates the app and consumer with the new image and restarts service.
5. Read the app's start time, then query events after that time through the
   recorded break-glass command. Run both commands from the install directory:

   ```bash
   docker inspect tack-app-1 --format '{{.State.StartedAt}}'
   docker compose run --rm tack-ops ops db sql \
     --statement "SELECT min(shard), max(shard), count(*) FROM audit.events WHERE event_time > '<the StartedAt value>'" \
     --reason "read back the shard space after the ShardCount change" \
     --operator-id <operator-id> --operator-email <operator-email> --execute
   ```

   Confirm that the maximum shard is below the new count. The time filter
   excludes rows that retain shards from the old count.
6. Export an org with events before and after the change, then verify the
   bundle:

   ```bash
   bundle=$(mktemp -d)
   docker compose run --rm -v "${bundle}:/bundle" app audit export \
     --org <org id> --oldest 2026-01-01T00:00:00Z --latest <now, RFC 3339> --out /bundle \
     --operator-id <operator-id> --operator-email <operator-email> --execute
   docker compose run --rm -v "${bundle}:/bundle" app audit verify --bundle /bundle \
     --operator-id <operator-id> --operator-email <operator-email> --execute
   ```

   Confirm that the report shows 0 chain breaks and 0 gaps. Verification checks
   each chain under its recorded shard, including shards from the old count.
   Interpret failures using [Tack recovery](../recovery.md).

Use this procedure for either an increase or a decrease in `ShardCount`.
