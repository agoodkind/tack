# Deployment capacity inputs

Capacity estimates require measurements from the target installation and a
workload that represents its activity. A configuration declares intended
resources and retention; it does not prove the running topology, current free
space, or actual retention.

## Measure comparable quantities

The following inputs support separate ledger, broker, and product-store
estimates.

Measurement inputs:

| Input | Measurement method | Qualification |
|---|---|---|
| Ledger events per interval | The measurement uses `ops db sql --execute` through `tack-ops` for one read-only `COUNT(*)` on `audit.events` across all organizations, including the nil organization. It counts each row with `event_time` at or after the start and before the end. | The database login must have ledger read access. The command requires a reason, successful alarm mail delivery, and recorded intent before running the statement. |
| Restored ledger rows | Each organization's row count comes from the restore drill's `backup.restore_drill.ledger_chain.org` results. | Each drill counts its restored backup, rather than the live ledger at drill time. |
| Kafka records per interval | The measurement uses `ops audit consumer-offsets --execute` through `audit-ops` at both boundaries and records each report's `read_at`. For each `topic` and `partition`, it subtracts the starting `high_water_mark` from the ending value and sums each difference once across consumer groups. | The reports must cover the same topic and every partition at both boundaries. The difference counts audit records when each new offset represents one audit record and offsets remain continuous. A stopped consumer does not affect this measure. Offset growth does not count final ledger events. |
| Ledger bytes per final event | The measurement divides audit partition total relation growth, including indexes, by the final events added during the same workload. | Row payload bytes alone omit index and relation costs. A zero growth or row count does not produce a usable per-event measurement. |
| Kafka bytes per record | The measurement divides broker byte growth by broker records added during the same workload. | The workload and compression affect this value. |
| Read-receipt bytes | The measurement includes terminal receipt keys, values, and cleanup indexes, multiplied by the storage replica count. | Per-receipt averages divide those bytes by the measured terminal receipt count. |
| Guest free space | The measurement uses `df -h /` and `du -sh` for each Docker volume on the target guest. | Guest services can share a disk; a retired volume still consumes space. |
| Backup capacity | Operators must measure the object-store guest and retained objects separately. | The application guest's free space does not measure SeaweedFS capacity. |

The host command `audit query --execute` pages through rows for one nonzero
`--org` between the inclusive `--oldest` and exclusive `--latest` RFC3339
bounds. Only the internal reader filter can explicitly select the nil
organization in this query. A subtotal report must identify every omitted organization
when it uses these pages.

The runtime audit scaling test measures the same workload at different app
instance counts and reports storage growth. Its measurements describe that
test's engine topology and workload. They do not measure production disk
headroom.

A background-traffic estimate separates deployment and measurement operations
from the remaining workload. The estimate and the busy interval measure
different activity.

Traffic measurement inputs:

| Quantity | Measurement and qualification |
| --- | --- |
| Remaining workload count | The count includes the remaining records or ledger events after subtracting records or ledger events attributable to deployment and measurement operations. |
| Observation duration | The duration corresponds to the remaining workload count. |
| Background-traffic rate | The rate equals the remaining record or ledger count divided by its corresponding observation duration. |
| Busy interval | A separate interval measures user activity. |

Neither interval alone establishes the installation's future traffic rate.

## Distinguish records from events

A single-record event uses one Kafka record and one final ledger event.
A multipart event uses one record per part plus a manifest record and still
produces one final ledger event. Permanent manifest parts and completed
transport digests consume additional ledger storage.

The following expressions keep the units explicit.

Capacity model:

| Quantity | Expression | Required assumption |
|---|---|---|
| Daily ledger growth per replica | Ledger events per day × total relation bytes per final event | The measured workload represents the estimate. |
| Daily broker growth per replica | Kafka records per day × broker bytes per record | The record rate and compression remain comparable. |
| Days of ledger space | Available bytes ÷ daily ledger growth in bytes | Other disk consumers and temporary engine space need separate allowances. |
| Retained Kafka bytes per replica | Kafka records per day × bytes per record × retention days | Retention succeeds and the disk can contain the retained records. |
| Added consumer-outage backlog | Kafka records per hour × outage hours | The producer continues accepting records during the outage. |
| Added backlog bytes per replica | Added records × bytes per record | Replication and other broker data need separate allowances. |
| GiB | Bytes ÷ 1,073,741,824 | Decimal MB and GiB must not share a label. |

These estimates exclude YugabyteDB point-in-time recovery history, write-ahead
logs, and compaction space unless the input measurements include them. A broker
shares disk with its other services. Kafka retention does not impose a bound on
all consumers of that disk.

## Account for retention and replication

The ledger's weekly audit partitions retain old events. Permanent event parts
and completed transport digests also contribute to continuing growth.
Dead-letter rows remain until replay removes them. Published read receipts
become eligible for cleanup after their publication retention interval;
unpublished receipts do not share that age bound.

Kafka retains records according to the topic's actual settings. The consumer
attempts to configure topic retention on startup, but a failed update logs an
error and permits projection to continue. A compiled retention default does
not prove the running topic's retention.

The following resource relationships affect estimates.

Replication and sizing limits:

| Resource | Capacity relationship |
|---|---|
| YugabyteDB | Each replica consumes storage. The live replication factor and tablet placement determine each guest's share. |
| FoundationDB | `double` stores two copies in separate zones. Guest identities must define the fault domains; separate container hostnames on one guest do not establish separate failure domains. |
| Kafka | Broker byte estimates multiply by the actual topic replication factor. |
| OpenSearch | Primary shards, replicas, and member placement determine copies and memory demand. |
| Guest memory | A container memory ceiling does not reserve physical RAM. Empty guests do not establish headroom after database processes start. |
| QA | A smaller QA topology can verify replication, failover, and export behavior without proving production memory capacity. |

Backup schedules, log rotation, continuous backup snapshots, and object-store
retention add storage demand. A missing object lifecycle rule does not justify
an assumed expiry period. Current sizing must include all active services and
retained volumes on each hypervisor.
