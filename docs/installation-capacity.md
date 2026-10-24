# Production installation capacity

This page covers the production installation's measured audit traffic, per-event storage, retention, copies, replication, backups, and days of disk left.

## Audit traffic

Production reads summed committed offsets for consumer group `tack-audit-projector` on topic `audit.events.v1` through `ops audit consumer-offsets` in the `audit-ops` service.

Production measurements:

| Measure | Time (UTC) | Value | Source |
|---|---|---|---|
| Topic partitions in the sum | 2026-10-24 12:02:24 | 256 | Production CT 117 read at this time |
| Committed events | 2026-10-24 00:14 | 203,629 | Earlier production CT 117 read at this time |
| Committed events | 2026-10-24 04:40 | 205,141 | Earlier production CT 117 read at this time |
| Committed events | 2026-10-24 12:02:24 | 247,277 | Production CT 117 read at this time |
| Total consumer lag | 2026-10-24 12:02:24 | 0 | Production CT 117 read at this time |
| Operator outbox rows | 2026-10-24 12:02:24 | 0 | Production CT 117 read at this time |
| FoundationDB outbox entries | 2026-10-24 12:02:24 | 0 | Production CT 117 read at this time |

The capacity calculations use both measured traffic rates.

Audit rates:

| Interval (UTC) | Events per hour | Events per day | Source |
|---|---|---|---|
| 2026-10-24 00:14 to 04:40 | 341 | 8,185 | Production CT 117 committed-offset reads at the interval endpoints |
| 2026-10-24 04:40 to 12:02 | 5,715 | 137,153 | Production CT 117 committed-offset reads at 04:40 and 12:02:24 |

## Per-event storage

The test harness measured ledger, Kafka, and read-receipt storage.

Storage measurements:

| Measure | Value | Measurement basis | Source |
|---|---|---|---|
| Test | `TestRuntimeAuditScaling` | Job `runtime audit coverage`; commit `5e63b097` | CI run 40083280758, job 120841017594 |
| Test environment | Single-node YugabyteDB and 1 Kafka broker | Test harness | CI run 40083280758, job 120841017594 |
| Ledger row bytes per event | 1,613 bytes | 638,780 row bytes for 396 ledger events; 1 instance | CI run 40083280758, job 120841017594 |
| YugabyteDB total relation bytes per final event | 10,625 bytes | Heap and indexes; 4 instances; the runs with 1 and 2 instances did not measure this value | CI run 40083280758, job 120841017594 |
| Kafka bytes per record | 1,136 bytes | 526,027 broker bytes for 463 records; 1 instance | CI run 40083280758, job 120841017594 |
| Replica bytes per pending read receipt | 388 bytes | 64 receipts | CI run 40083280758, job 120841017594 |
| ClickHouse bytes per event | The test did not measure this value | Capacity calculations do not compute ClickHouse growth | CI run 40083280758, job 120841017594 |

## Production disk

Production disk reads used `df -h /` and `du -sh` for each Docker volume. The tables use G for GiB.

Guest disk measurements:

| Guest | Role | Disk size | Used | Available | Largest volumes | Source |
|---|---|---|---|---|---|---|
| CT 117 | App, audit-consumer, Kafka, ClickHouse, backup owner | 295G | 64G | 220G | `tack_clickhouse-data`: 28G; `tack_yugabyte-data`: 3.3G (retired node); `tack_tack-logs`: 1.8G; `tack_fdb-data`: 1.3G (retired node); `tack_kafka-data`: 254M | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
| CT 120 | YugabyteDB yb1, FoundationDB | 59G | 24G | 33G | `tack_yugabyte-data`: 16G; `tack_fdb-data`: 148M | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
| CT 121 | YugabyteDB yb2, FoundationDB | 59G | 24G | 33G | `tack_yugabyte-data`: 17G; `tack_fdb-data`: 136M | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
| CT 122 | YugabyteDB yb3, FoundationDB | 59G | 25G | 32G | `tack_yugabyte-data`: 18G; `tack_fdb-data`: 131M | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
| CT 123 | Second app guest; services do not run | 40G | 1.5G | 36G | The guest does not have Docker volumes | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
| CT 125 | OpenSearch | 40G | 5.9G | 32G | `tack_opensearch-data`: 1.8G | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |

SeaweedFS stores the backup objects separately.

Backup object store:

| Store | Guest | Bucket | Disk measurement | Source |
|---|---|---|---|---|
| SeaweedFS | CT 118 | `tack-backups` | The production reads did not measure this guest's disk | Configs `ansible/inventory/group_vars/tack_prod_all.yml:55-60` |

## Retention

Repository settings define retention for each data store.

Retention settings:

| Data | Retention | Source |
|---|---|---|
| `audit.events` partitions | pg_partman never drops partitions: `retention = NULL`, `retention_keep_table = true`; weekly partitions; premake 2 | Tack `main`, `migrations/004_audit_partman.sql:130,141-146` and `migrations/014_audit_partition_premake.sql:17-19` |
| `audit.event_parts`, `audit.completed_transports` | Rows do not expire | Tack `main`, `migrations/020_audit_event_parts.sql:100-120` |
| `audit.events_dlq` | Rows do not expire; replay removes a row | Tack `main`, `migrations/011_audit_events_dlq_replay.sql:7-8` |
| Kafka topic `audit.events.v1` | 8,760 hours (365 days); Kafka does not impose a byte limit | Tack `main`, `AUDIT_CONSUMER_TOPIC_RETENTION` default in `internal/config/audit_consumer.go:28`; application in `internal/audit/consumer_startup.go:76-159` |
| ClickHouse `audit.events_olap` | The table does not define a time-to-live limit | Tack `main`, `internal/audit/consumer.go:958-981` |
| Published read receipts | 1 hour after publication | Tack `main`, `ReadReceiptRetention` in `internal/audit/read_lifecycle.go` |
| YugabyteDB point-in-time recovery history | 10,080 minutes (7 days); interval 60 minutes | Tack `main`, `internal/config/config.go:224-225` |
| Application log files | Rotation at 100 MB; every rotated file remains; `tack_log_max_backups: 0`, `tack_log_max_age_days: 0` | Configs `ansible/inventory/group_vars/tack_all.yml:305-307` |
| Backup objects in SeaweedFS | The source review did not find a lifecycle rule | Configs SeaweedFS templates and group vars |

## Copies and replication

Production stores use the following replication settings.

Store copies:

| Store | Copies and replication | Source |
|---|---|---|
| YugabyteDB ledger | 3 replicas, one per data node from yb1 to yb3. Configuration does not set an explicit replication factor. yugabyted raises the cluster to 3 replicas when the third node joins. | Configs `ansible/inventory/group_vars/tack_prod_all.yml:90-93`; Tack `internal/test/integration/ledger_bootstrap_cluster_test.go:117-141` |
| FoundationDB | `double` redundancy across 3 data nodes | Configs `ansible/inventory/group_vars/tack_prod_all.yml:110` |
| Kafka | 1 copy; 1 broker; replication factor 1; 256 partitions | Tack `docker-compose.yml:397-411` and `internal/audit/consumer_startup.go:22,82` |
| ClickHouse | 1 copy on CT 117 | Tack `docker-compose.yml` |
| OpenSearch | 1 shard; 0 replicas; 1 member | Configs `ansible/inventory/group_vars/tack_prod_all.yml:146-148` |

## Backups

Production configuration enables backups and defines their schedules and checks.

Backup settings:

| Backup or check | Setting | Source |
|---|---|---|
| Backup enablement | `tack_backup_enabled: true`; `tack_backup_fdb_continuous: true` | Configs `ansible/inventory/group_vars/tack_prod_all.yml:54,68` |
| Ledger export (`yb-snapshot-export`) to SeaweedFS | Daily at 03:17 UTC | Configs `ansible/inventory/group_vars/tack_all.yml:461-462` and `common/timers/tack-ledger-export.timer.j2` |
| Per-node ledger archive (`yb-archive-node`) | Every 15 minutes on each data guest | Configs `ansible/inventory/group_vars/tack_all.yml:463` |
| FoundationDB continuous backup | Snapshot interval 3,600 seconds | Tack `internal/config/config.go:191` |
| Restore drill | Daily at 05:40 UTC on CT 117 | Configs `ansible/inventory/group_vars/tack_all.yml:486-487` |
| Staleness check | Every 10 minutes; thresholds: export 36 hours, rehearsal 8 days, replication 30 minutes, FoundationDB 2 hours | Configs `ansible/inventory/group_vars/tack_all.yml:475-476`; Tack `internal/config/config.go:280-283` |

## Days of disk left

Each YugabyteDB node stores a replica of every ledger tablet. Daily growth equals events per day multiplied by the measured bytes per event or record.

Capacity calculations:

| Store and guest | Available | Growth per day at 341 events/hour | Days left at 341 events/hour | Growth per day at 5,715 events/hour | Days left at 5,715 events/hour | Source |
|---|---|---|---|---|---|---|
| YugabyteDB, CT 120 | 33G | 0.081G | 407 | 1.36G | 24 | Production disk read, 2026-10-24 about 12:05 UTC; audit-rate reads above; CI run 40083280758, job 120841017594 |
| YugabyteDB, CT 121 | 33G | 0.081G | 407 | 1.36G | 24 | Production disk read, 2026-10-24 about 12:05 UTC; audit-rate reads above; CI run 40083280758, job 120841017594 |
| YugabyteDB, CT 122 | 32G | 0.081G | 395 | 1.36G | 24 | Production disk read, 2026-10-24 about 12:05 UTC; audit-rate reads above; CI run 40083280758, job 120841017594 |
| Kafka, CT 117 | 220G shared | 0.0087G | Kafka alone does not fill the available disk; retention storage is 3.2G | 0.145G | Kafka alone does not fill the available disk; retention storage is 53G | Production disk read, 2026-10-24 about 12:05 UTC; audit-rate reads above; CI run 40083280758, job 120841017594; `internal/config/audit_consumer.go:28` |
| ClickHouse, CT 117 | 220G shared | The test did not measure growth | The calculation does not compute this value | The test did not measure growth | The calculation does not compute this value | Production disk read, 2026-10-24 about 12:05 UTC; CI run 40083280758, job 120841017594 |
| Pending read receipts, FoundationDB | Data guests | One hour of reads bounds storage | Storage does not grow beyond that bound | One hour of reads bounds storage | Storage does not grow beyond that bound | Tack `internal/audit/read_lifecycle.go` |

The calculations use the YugabyteDB total relation measurement and the Kafka record measurement in the storage table. They exclude YugabyteDB point-in-time recovery history, compaction space, and write-ahead logs.

## Outage backlog

A consumer outage adds undelivered records within Kafka's retention period.

Backlog calculation:

| Store and guest | Audit rate | Consumer outage | Added records | Added storage | Source |
|---|---|---|---|---|---|
| Kafka, CT 117 | 5,715 events per hour | 1 day | 137,153 | About 156 MB | Production audit reads, 2026-10-24 04:40 and 12:02:24 UTC; CI run 40083280758, job 120841017594 |