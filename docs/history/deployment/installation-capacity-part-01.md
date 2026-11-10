# Production installation capacity

This quotation preserves historical evidence from the November 7, 2026 source snapshot. Its observations and decisions do not establish current behavior or deployment.

Source: [original historical evidence](https://github.com/agoodkind/tack/blob/7e469f9851c55e0ae36a03fe279287c6a430da05/docs/installation-capacity.md).

[Topic history](README.md)

> # Production installation capacity
>
> This page covers the production installation's measured audit traffic, storage per ledger row and Kafka record, retention, copies, replication, backups, and days of disk left.
>
> ## Audit traffic
>
> The `tack-backup-restore-drill.service` journal on CT 117 reports every organization in the restored ledger through `backup.restore_drill.ledger_chain.org` (`internal/ops/backup_restore_drill_ledger_orgs.go`). Organization totals:
>
> | Organization | Rows on 2026-10-24 drill |
> |---|---|
> | `019dc5ad-0408-7e43-9c4d-d3e6736ac058` (main) | 533,835 |
> | `00000000-0000-0000-0000-0000000005ee` (system) | 51,714 |
> | `00000000-0000-0000-0000-000000000000` (nil organization) | 833 |
>
> `audit query` refuses the nil organization through `QueryFilter.Validate` in `internal/audit/reader.go`. The hourly measurements cover the main and system organizations.
>
> Each daily restore drill restores the export from that day's ledger backup. The backup settings give the export time, and the `tack-backup-restore-drill.service` journal supplies the row counts. Daily ledger totals:
>
> | Drill date | Ledger rows, all organizations | Increase from the previous drill | Nil organization increase |
> |---|---|---|---|
> | 2026-10-21 | 574,653 | | |
> | 2026-10-22 | 577,236 | 2,583 | 2 |
> | 2026-10-23 | 581,265 | 4,029 | 6 |
> | 2026-10-24 | 586,382 | 5,117 | 6 |
>
> The audit producer uses `EncodedEvent.Messages` in `internal/audit/event_parts_wire.go` to encode Kafka records. Every measured multipart event was a read event. Event records and rows:
>
> | Event shape | Parts | Kafka records | Ledger rows |
> |---|---|---|---|
> | Single-record event | | 1 | 1 |
> | Multipart event | One or more | One per part plus one manifest record | 1 |
> | Measured multipart read event | 1 manifest part | 2 | 1 |
>
> `ops audit consumer-offsets` through the `audit-ops` service on CT 117 supplies the summed committed offsets for group `tack-audit-projector` on `audit.events.v1`. `audit query` through the same service supplies ledger rows, with paging over each window. Measurement totals:
>
> | Window (UTC) | Duration | Committed offsets, start to end | Kafka records, measured / rows plus manifest parts | Ledger rows | Read events with one manifest part | Partitions / lag at each read |
> |---|---|---|---|---|---|---|
> | 2026-10-24 13:44:44 to 14:46:32 | 1.03 hours | 247,663 to 248,042 | 379 / 376 | 346 | | 256 / 0 |
> | 2026-10-24 04:40 to 12:02:24 | 7.37 hours | 205,141 to 247,277 | 42,136 / 42,135 | 35,109 (34,132 main; 977 system) | 7,026 | 256 / 0 |
>
> The measurement hour included a production deploy and operator runs. Deploy and measurement record counts equal their ledger rows plus read manifest parts; remaining records equal the measured increase minus those records. Measurement hour traffic:
>
> | Part of the hour | Ledger rows | Kafka records |
> |---|---|---|
> | Production deploy of Tack `4cf2b061` from 14:01:45 to 14:10:03, and its checks through 14:13:21 | 137 | 148 |
> | Deploy verify, fixed definition backfill, and node integrity backfill runs by the deploy operator from 14:23 to 14:41 | 15 | 20 |
> | The `audit query` and `consumer-offsets` runs of this measurement | 42 | 42 |
> | Remaining traffic, outside the deploy window (50.2 minutes) | 152 | 169 |
>
> The remaining measurement hour traffic and the earlier interval provide separate ledger and Kafka rates from the production reads above. Audit rates:
>
> | Traffic | Ledger rows per hour | Ledger rows per day | Kafka records per hour | Kafka records per day | Records per row |
> |---|---|---|---|---|---|
> | Remaining measurement hour traffic | 182 | 4,360 | 202 | 4,848 | 1.11 |
> | 04:40 to 12:02:24 interval | 4,762 | 114,279 | 5,715 | 137,152 | 1.20 |
>
> `audit query` supplies the action counts below. API token actor `14385627-0313-50f8-bf7e-0c966e355dd9` wrote all main organization rows in the earlier interval through MCP. Ledger rows by action:
>
> | Traffic | Action | MCP tool | Rows |
> |---|---|---|---|
> | Remaining measurement hour | `auth.token_used` | | 42 |
> | Remaining measurement hour | `mcp.protocol_read` | | 42 |
> | Remaining measurement hour | `ops.backup_yb_archive_node` | | 36 |
> | Remaining measurement hour | `ops.backup_staleness_check` | | 32 |
> | Earlier interval, main organization | `auth.token_used` | Every tool call | 17,139 |
> | Earlier interval, main organization | `relationship.list` | `tack_list_relationships` | 5,871 |
> | Earlier interval, main organization | `props.read` | `tack_get_properties` | 5,574 |
> | Earlier interval, main organization | `node.list` | `tack_list_comments` (2,550), `tack_list_activities` (2,539), and others | 5,167 |
> | Earlier interval, main organization | Other actions | | 381 |
> | Earlier interval, system organization | `ops.backup_staleness_check` | | 328 |
> | Earlier interval, system organization | `ops.backup_yb_archive_node` | | 278 |
> | Earlier interval, system organization | `mcp.protocol_read` | | 170 |
> | Earlier interval, system organization | `ops.provision` (deploy command) | | 56 |
> | Earlier interval, system organization | `ops.ledger_node_prepare` (deploy command) | | 42 |
> | Earlier interval, system organization | `ops.ledger_node_wait` (deploy command) | | 39 |
>
> `audit query` also supplies the main organization's hourly counts within the earlier interval. Main organization rows by hour:
>
> | Hour (UTC) | 05:00 | 06:00 | 07:00 | 08:00 | Every other hour |
> |---|---|---|---|---|---|
> | Ledger rows | 7,184 | 10,953 | 12,025 | 3,834 | 83 or fewer |
>
> ## Per-event storage
>
> The test harness measured ledger, Kafka, and read-receipt storage.
>
> Storage measurements:
>
> | Measure | Value | Measurement basis | Source |
> |---|---|---|---|
> | Test | `TestRuntimeAuditScaling` | Job `runtime audit coverage`; commit `5e63b097` | CI run 40083280758, job 120841017594 |
> | Test environment | Single-node YugabyteDB and 1 Kafka broker | Test harness | CI run 40083280758, job 120841017594 |
> | Ledger row bytes per event | 1,613 bytes | 638,780 row bytes for 396 ledger events; 1 instance | CI run 40083280758, job 120841017594 |
> | YugabyteDB total relation bytes per final event | 10,625 bytes | Heap and indexes; 4 instances; the runs with 1 and 2 instances did not measure this value | CI run 40083280758, job 120841017594 |
> | Kafka bytes per record | 1,136 bytes | 526,027 broker bytes for 463 records; 1 instance | CI run 40083280758, job 120841017594 |
> | Replica bytes per pending read receipt | 388 bytes | 64 receipts | CI run 40083280758, job 120841017594 |
>
> ## Production disk
>
> Production disk reads used `df -h /` and `du -sh` for each Docker volume. The tables use G for GiB.
>
> Guest disk measurements:
>
> | Guest | Role | Disk size | Used | Available | Largest volumes | Source |
> |---|---|---|---|---|---|---|
> | CT 117 | App, audit-consumer, Kafka, backup owner | 295G | 64G | 220G | `tack_clickhouse-data`: 28G (retired service); `tack_yugabyte-data`: 3.3G (retired node); `tack_tack-logs`: 1.8G; `tack_fdb-data`: 1.3G (retired node); `tack_kafka-data`: 254M | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
> | CT 120 | YugabyteDB yb1, FoundationDB | 59G | 24G | 33G | `tack_yugabyte-data`: 16G; `tack_fdb-data`: 148M | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
> | CT 121 | YugabyteDB yb2, FoundationDB | 59G | 24G | 33G | `tack_yugabyte-data`: 17G; `tack_fdb-data`: 136M | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
> | CT 122 | YugabyteDB yb3, FoundationDB | 59G | 25G | 32G | `tack_yugabyte-data`: 18G; `tack_fdb-data`: 131M | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
> | CT 123 | Second app guest; services do not run | 40G | 1.5G | 36G | The guest does not have Docker volumes | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
> | CT 125 | OpenSearch | 40G | 5.9G | 32G | `tack_opensearch-data`: 1.8G | Production read, 2026-10-24 about 12:05 UTC; roles: Configs `ansible/inventory/group_vars/all/service_mapping.yml:72-116` and `ansible/playbooks/deploy-tack.yml` |
>
> SeaweedFS stores the backup objects separately.
>
> Backup object store:
>
> | Store | Guest | Bucket | Disk measurement | Source |
> |---|---|---|---|---|
> | SeaweedFS | CT 118 | `tack-backups` | The production reads did not measure this guest's disk | Configs `ansible/inventory/group_vars/tack_prod_all.yml:55-60` |
>
> ## Days of disk left
>
> Each YugabyteDB node stores a replica of every ledger tablet. YugabyteDB growth uses ledger rows per day multiplied by the total relation bytes per final event in the storage table. Kafka growth uses Kafka records per day multiplied by the bytes per record in that table. Kafka deletes records older than its retention period in the retention settings.
>
> The calculations use production disk measurements, audit rates, and storage measurements. The measurement hour rate uses only its remaining traffic. Capacity calculations:
>
> | Store and guest | Available | Growth per day, measurement hour | Days left, measurement hour | Growth per day, 04:40 to 12:02 | Days left, 04:40 to 12:02 | Growth basis and source |
> |---|---|---|---|---|---|---|
> | YugabyteDB, CT 120 | 33G | 0.043G | 765 | 1.13G | 29 | Ledger rows per day × 10,625 bytes; production disk read, 2026-10-24 about 12:05 UTC; audit-rate reads above; CI run 40083280758, job 120841017594 |
> | YugabyteDB, CT 121 | 33G | 0.043G | 765 | 1.13G | 29 | Ledger rows per day × 10,625 bytes; production disk read, 2026-10-24 about 12:05 UTC; audit-rate reads above; CI run 40083280758, job 120841017594 |
> | YugabyteDB, CT 122 | 32G | 0.043G | 742 | 1.13G | 28 | Ledger rows per day × 10,625 bytes; production disk read, 2026-10-24 about 12:05 UTC; audit-rate reads above; CI run 40083280758, job 120841017594 |
> | Kafka, CT 117 | 220G shared | 0.0051G | Kafka alone does not fill the available disk; one year of retention is 1.9G | 0.145G | Kafka alone does not fill the available disk; one year of retention is 53G | Kafka records per day × 1,136 bytes; production disk read, 2026-10-24 about 12:05 UTC; audit-rate reads above; CI run 40083280758, job 120841017594; `internal/config/audit_consumer.go:28` |
> | Pending read receipts, FoundationDB | data guests | bounded by one hour of reads | does not grow beyond that bound | bounded by one hour of reads | does not grow beyond that bound | Tack `internal/audit/read_lifecycle.go` |
>
> The calculations use the YugabyteDB total relation measurement and the Kafka record measurement in the storage table. They exclude YugabyteDB point-in-time recovery history, compaction space, and write-ahead logs. The ledger row rates exclude the nil organization.
>
> ## Outage backlog
>
> Kafka retains undelivered records for the topic retention period below.
>
> The audit rates above and Kafka bytes per record from CI run 40083280758, job 120841017594, determine the added storage. Backlog calculation:
>
> | Store and guest | Audit rate | Consumer outage | Added records | Added storage | Kafka retention | Source |
> |---|---|---|---|---|---|---|
> | Kafka, CT 117 | 202 records per hour | 1 day | 4,848 | About 5.5 MB | 365 days | Remaining measurement hour traffic above; CI run 40083280758, job 120841017594; `internal/config/audit_consumer.go:28` |
> | Kafka, CT 117 | 5,715 records per hour | 1 day | 137,152 | About 156 MB | 365 days | Production audit reads, 2026-10-24 04:40 and 12:02:24 UTC; CI run 40083280758, job 120841017594; `internal/config/audit_consumer.go:28` |
