# Change the audit shard count

The audit ledger writes each org's events across `ShardCount` parallel hash
chains, and an event's shard is a checksum of its actor and event ids masked
down to that count. Raising or lowering the count is forward-only: rows already
written keep their shard, their chains stay closed and verifiable, and later
events land in the new shard space. No row is rewritten.

The app computes an event's shard when it writes the event to Kafka, and the
audit consumer recomputes it when it projects the event into the ledger, so the
two must never run different counts against the same in-flight events. The
steps below drain Kafka between the old build and the new one.

## Prerequisites

- A power of two for the new count.
- An announced window. The app is stopped from step 2 until step 4, and the
  MCP surface is down for that time.
- An operator identity for the recorded commands: pass `--operator-id` and
  `--operator-email` on every `ops` and `audit` command below.

## Steps

1. Set `ShardCount` in [canonical.go](../../../internal/audit/canonical.go) to
   the new count, set the pinned shard in
   [canonical_test.go](../../../internal/audit/canonical_test.go) to the value the
   new count gives the test pair, and merge through the normal pull request
   path.
2. Stop the app, from the install directory on the app guest:

   ```
   docker compose stop app
   ```

3. Drain the consumer. Read its lag gauge until every partition reports 0. The
   consumer image has no shell, so read the gauge from its network namespace,
   substituting the consumer's `AUDIT_CONSUMER_METRICS_ADDR` value:

   ```
   nsenter -t "$(docker inspect --format '{{.State.Pid}}' tack-audit-consumer-1)" -n \
     curl -s http://<AUDIT_CONSUMER_METRICS_ADDR>/debug/vars | jq '.tack_audit_consumer_lag_messages'
   ```

   A partition above 0 is an in-flight event that the new build would project
   under the wrong count. Wait for 0 on every partition.
4. Deploy the merged build through the normal deploy path. The deploy recreates
   the app and the consumer on the new image, and the app serves again.
5. Read the shard space of the rows written since the new app started. Take
   the boundary from the app container itself, then read the ledger through
   the recorded break-glass command, both from the install directory:

   ```
   docker inspect tack-app-1 --format '{{.State.StartedAt}}'
   docker compose run --rm tack-ops ops db sql \
     --statement "SELECT min(shard), max(shard), count(*) FROM audit.events WHERE event_time > '<the StartedAt value>'" \
     --reason "read back the shard space after the ShardCount change" \
     --operator-id <your id> --operator-email <your email> --execute
   ```

   Every row after that boundary was produced under the new count, so the
   maximum is below the new count; rows before it keep shards from the old
   space and are excluded by the boundary whichever way the count moved.
6. Export one org that has rows on both sides of the change and verify the
   bundle:

   ```
   bundle=$(mktemp -d)
   docker compose run --rm -v "${bundle}:/bundle" app audit export \
     --org <org id> --oldest 2026-01-01T00:00:00Z --latest <now, RFC 3339> --out /bundle \
     --operator-id <your id> --operator-email <your email> --execute
   docker compose run --rm -v "${bundle}:/bundle" app audit verify --bundle /bundle \
     --operator-id <your id> --operator-email <your email> --execute
   ```

   The report's chain-break and gap counts read 0, because every chain, under
   the old count and the new one, verifies from its own head. To learn what a
   break or a gap means, see [Tack recovery](../recovery.md).

Lowering the count later is the same change run with the same steps; a lower
value is a new shard space like any other.
