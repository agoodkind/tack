# How audit partitions stay available

`audit.events` stores events in weekly partitions selected by `event_time`. A valid event cannot enter the ledger when its timestamp lacks a partition. Partition maintenance must preserve coverage for the current week and a future buffer without deleting permanent audit history.

## Registration and existing partitions

pg_partman manages the native weekly partitions. Migration 004 registers `audit.events` and adopts its existing children. Registration preserves attached data rather than copying or rehashing ledger rows. Maintenance continues to create future partitions even when recent partitions did not receive rows.

The migration checks target-name conflicts before YugabyteDB performs renames. It changes the original `events_YYYY_MM_DD` child names to `events_pYYYY_MM_DD`, checks any existing registration for compatible native weekly configuration, and parses existing upper bounds. It starts creation at the later of existing coverage and the current week. This prevents overlapping ranges during adoption.

The initial migration configured twelve future weeks and disabled automatic retention for `audit.events` by setting `retention` to `NULL`. Migration 014 sets the number of future weeks to two. For each future child beyond the current week and the next two weeks, it counts that child's rows and drops the table only if it is empty. It preserves past and current partitions and any future partition containing rows.

The headroom gauge counts partitions that start after the observation time. This count differs from the configured premake interval: maintenance can report three future partitions in the middle of a week with a two-week premake setting. An operator must compare observed coverage with the current migration and observation time rather than require the original twelve-week value.

## Privileges

The consumer uses the audit writer role without general `CREATE` permission on the audit schema. A `SECURITY DEFINER` function runs maintenance with its owner's privileges. The function pins its search path, restricts maintenance to `audit.events`, revokes public execution, and grants execution to the writer role. Its owner needs permission to use the partman schema and execute the maintenance function.

Normal migrations use the nonsuperuser migration owner. The audit and partman schema guard must remain enabled. Handwritten schema changes must not bypass the reviewed migration or maintenance functions. Existing children must use the pg_partman naming form; one unexpected child name can prevent maintenance from parsing its partition dates.

## Scheduling and observations

`PartitionManager` runs inside the audit consumer. It runs once at startup and then on its configured interval, which defaults to 24 hours. Startup maintenance attempts to restore future coverage after downtime before the first scheduled tick. Scheduling does not require pg_cron or a database restart. Each consumer runs its passes in sequence. Separate consumer replicas start separate workers.

Each pass checks for unexpected child names, attempts maintenance, and reads actual headroom. It updates the gauge when the headroom query succeeds, including after a maintenance failure. It counts successful and failed maintenance attempts separately. It reports each condition separately:

- The manager reports maintenance failures.
- The manager reports headroom query failures.
- The manager reports low headroom.
- The manager reports unexpected child names.

A failure does not terminate the consumer; later passes retry.

The manager shares the consumer pool and does not close it. Shutdown stops the worker. Repeated shutdown calls do not close its stop channel again.

External operations require a durable outbox for intent and observed results. The production consumer instead connects partition maintenance directly to the relay's synchronous Kafka producer. The worker reads the initial headroom, then publishes the operation intent and planned step before it calls the database maintenance function. A failed initial read or a refusal to record either event prevents that call. After successful maintenance it reads headroom again for the step result. The producer publishes step and operation results without creating database outbox entries. A later recording failure cannot undo maintenance that already ran.

After the maintenance SQL commits, the worker can still fail to reset the connection's search path or to read the new headroom. In either case, if publication succeeds, the service records a failed step. Its summary treats that step as finished without a committed effect, and the service can publish `failed` for the parent operation. The recording contract reserves `failed` for an operation that did not commit effects. An operator must inspect the actual partition bounds before concluding that maintenance did not change them or deciding to run it again.

`AUDIT_CONSUMER_PARTITION_PERIOD` controls how often the worker calls the maintenance function. That function runs pg_partman for `audit.events` and creates weekly partitions from the current week through the configured number of future weeks. The worker logs an error when it counts fewer than two future weekly partitions.

## Evidence required for changes

QA must validate adoption against both an existing layout where the last partition no longer covers the current week and a fresh environment. Acceptance must establish each registration result:

- Registration preserves existing data.
- Registration avoids range overlap.
- Registration creates current and future coverage.
- Registration retains disabled history deletion.

A real writer-role maintenance run must succeed through the privilege wrapper.

Regression coverage must exercise each worker condition:

- Startup catch-up.
- Recurring execution.
- Failed maintenance.
- Headroom observations.
- Low-headroom alerts.
- Unexpected child names.
- Shutdown.

A real database test must remove future coverage in its disposable fixture, run production maintenance, and prove that events in the current week and the newly covered future week can commit. A helper-only or stub-only check cannot establish that database behavior.

Deployment acceptance requires evidence from the installed environment for each check:

- pg_partman registration.
- Disabled retention.
- Current and future partition bounds.
- Worker headroom.
- A successful audited product operation.
