# Scale the Tack audit subsystem across hosts

This 2026-05-09 design defines the target after Phase 1. N=1 means the initial
CT 117 deployment with one operator. N=many means multiple hosts and regions.
Use the same producer, consumer, notarizer, redactor, broker, ledger, and archive
components at both sizes. Change placement, replica counts, and configuration,
except for the explicit notarizer election question below.

## Deployment and integrity constraints

The initial deployment uses the existing Compose stack without a new host,
external SaaS, or SLA-bound managed service. Change `docker-compose.yml` and
`cmd/server/main.go` through the deployment contract in `AGENTS.md`.

Components must scale through configuration. Do not select a SQLite broker,
on-host archive filesystem, or an in-memory cache that cannot partition.
The same verifier must validate chain bytes from either deployment size.
Expansion must preserve history, shard chains, Merkle notarization, and separate
personal data (PII).

Complete the Phase 1 WAL correction before replacing it with a broker. Preserve
YugabyteDB recording until the broker provides equivalent behavior. The original
state-change requirement persists audit rows synchronously without depending
on broker availability. The original read-class design uses asynchronous broker
consumption. The later [cutover plan](audit-kafka-cutover.md) supersedes the
original dual-write rollout and makes audit-consumer the sole ledger writer.

## Component relationships

At N=1, `tack-app` invokes `Recorder.Record(ctx, ev)`. The original
`RecorderRouter` selects synchronous `YBRecorder` for state changes and
`KafkaRecorder` for reads. Kafka stores `audit.events.v1` in 256 partitions with
replication factor 1. One `audit-consumer` performs hash chaining, PII separation,
and event-ID deduplication before writing YugabyteDB. One notarizer signs Merkle
roots every 60s. SeaweedFS provides the S3 archive on its own LXC.

At N=many, multiple app hosts publish to 3 or more Kafka brokers with 256
partitions and replication factor 3. Consumer group `audit-proj` assigns
partitions to M consumer instances. YugabyteDB uses 3 or more nodes and xCluster
for multiple regions. Iceberg archives use SeaweedFS or Garage through S3.
Multiple notarizer replicas elect one active signer. Expansion changes component
counts and placement rather than replacing components.

The ledger schema includes `audit.chain_heads`, `audit.events`,
`audit.notarizations`, `audit.pii`, and `audit.consumer_offsets` at either size.

## Select the broker

Use Apache Kafka 4.x in KRaft mode under Apache 2.0. Kafka 4.0 removed
ZooKeeper in March 2025. The [Kafka 4.2.0 release](https://kafka.apache.org/blog/2026/02/17/apache-kafka-4.2.0-release-announcement/)
continues that metadata model. At N=1, one JVM runs
`process.roles=broker,controller`; it does not require a separate metadata
service or controller process.

Kafka's stable wire protocol permits unchanged Go producers and consumers as
the broker cluster expands. Create 256 partitions initially. Key-based partition
assignment continues after redistribution; see [producer configuration](https://kafka.apache.org/documentation/#producerconfigs).
Increase replication from RF=1 to RF=3 with `kafka-configs.sh` and
`kafka-reassign-partitions.sh`. Kafka consumer-group rebalancing assigns
partitions when new projector instances start. The original projector design
uses transactional commit semantics for consumption and offset recording.

At N=1, the sole replica leads each partition and
`controller.quorum.voters` has replication factor 1. At N=many, use 3 or more
brokers and a controller quorum of 3 or 5 voters. Rebalance partition leadership
with `kafka-reassign-partitions.sh` or Cruise Control.

The operator requires fully open-source components without BSL, proprietary
software, or managed cloud services. This excludes Redpanda Community Edition's
Business Source License, which restricts production use before conversion to an
open license. Redpanda's single C++ binary, absence of a JVM, and lower tail
latency do not override the licensing requirement. Kafka still requires JVM
tuning and garbage-collection monitoring, although KRaft removes ZooKeeper.

NATS JetStream uses Apache 2.0, but the
[Jepsen NATS 2.12.1 analysis](https://jepsen.io/analyses/nats-2.12.1), published
2025-12-08, reports unresolved data loss. This design rejects unresolved silent
loss for compliance evidence. It did not identify a comparable Kafka finding
at that severity. Reconsider NATS after a corrected release and independent
validation. Switching would require producer and consumer changes because NATS
subjects do not implement Kafka partitions.

Apache Pulsar also uses Apache 2.0 and supports tenant namespaces and built-in
geo-replication. The design rejects it because brokers, BookKeeper, and
ZooKeeper or another metadata service add coordinated processes at N=1. Its
Go ecosystem and self-hosted operational references are smaller than Kafka's.
Tack enforces tenancy in producer keys and ledger rows rather than relying on
broker tenancy.

Kafka satisfies the licensing requirement, independent large-scale review,
Apache Software Foundation governance, and established operational support.
Its larger operational workload remains an explicit cost.

## Store the ledger and archive

YugabyteDB stores the canonical ledger, chain heads, notarizations, personal
data, and consumer offsets. Each ledger transaction writes its event and chain
head together. Operator commands query that database. N=1 uses one node;
N=many uses a cluster with xCluster replication for multiple regions.

Use Apache Iceberg tables with immutable Parquet files on SeaweedFS S3 storage.
SeaweedFS uses Apache 2.0; Garage is an AGPL alternative. Both projects had
active maintenance in 2026. SeaweedFS provides a longer release history and
broader deployment experience. Garage offers a single Rust binary and stricter
geo-replication semantics. The same archive binary uses either S3 API.
Do not substitute managed AWS S3. The design also rejects the earlier MinIO
recommendation under the operator's licensing requirements.

At N=1, run `weed` under systemd in its own LXC with one node, bucket, local
data directory, and SQLite Iceberg catalog. At N=many, use multiple SeaweedFS
volume servers and a replicated filer, or at least 3 Garage nodes, with a
self-hosted Nessie catalog. The catalog may use a single-file JSON alternative
at N=1 without changing Iceberg table format.

The archiver reads YugabyteDB, groups rows by week and organization, writes
Parquet to the S3 endpoint, and commits catalog metadata. Do not rewrite archive
files. Hash-addressed immutable contents preserve chain verification. The
archive binary must not branch on replica count or storage product. Schedule
nightly work with Compose at N=1 or Kubernetes Cron at N=many.

## Elect and operate the notarizer

Extend `internal/audit/notarizer.go` with an election step. The original
candidates are YugabyteDB advisory locks at N=1, FoundationDB compare-and-swap
at N=many, or a Kafka consumer-group lease. The primitive remains unresolved.

One active process reads `audit.chain_heads`, computes a Merkle root, signs it
with Ed25519, and writes `audit.notarizations` once per minute. Multiple active
processes produce redundant rather than invalid notarizations, but election
avoids that duplication. The signer does not limit event throughput.

Use one Ed25519 private key for the aggregate rather than per-shard keys.
Per-shard signing would require more key management or BLS/threshold signature
aggregation without a benefit at this scale. This follows the Sigstore Rekor
aggregate model and the existing notarizer.

At N=1, store the key at `/etc/tack/audit-signing.pem` and run one process
without election. At N=many, distribute the same key to every replica and elect
one active signer. Mount the key as a file initially; an open-source secret
manager such as OpenBao can manage replicated copies. Do not use HashiCorp
Vault under BUSL-1.1.

Reacquire the election lock each cadence. After a host or network failure, a
follower must become active within one cadence. The considered Kafka lease uses
single-partition topic `audit.notarizer.leader`. Record the leader's key ID in
`signing_key`; the replacement leader uses the replicated key.

Rotate keys identically at either size:

1. Generate the new key.
2. Sign with both old and new keys for one complete cadence.
3. Stop using the old key after the overlap.
4. Accept either key for signatures from the overlap window. Read
   `signing_key` from the notarization row.

`AUDIT_NOTARIZER_PERIOD` controls the default 60s cadence. The existing loop is
at `internal/audit/notarizer.go:90`, and root construction is at line 132.

## Separate and redact personal data

Keep `audit.pii` in YugabyteDB. The producer publishes personal data in the
event payload. After broker publication, the consumer writes that data to
`audit.pii` in the transaction that appends the ledger event. Replication
includes that table. The `audit_redactor` role updates
`audit.pii.payload`; the hash chain excludes that payload and remains valid.
At N=1, writes use one node. At N=many, writes use the primary and replicas
support reads. Bulk erasure of a user's complete history still needs a defined
batch operation.

## Configure producers and consumers

Use MIT-licensed `github.com/twmb/franz-go`, a pure Go Kafka client with
transactions and idempotence. The alternative MIT-licensed
`github.com/segmentio/kafka-go` also avoids CGO but provides less complete
transactional producer support. A client change would remain confined to
`KafkaRecorder` because both use Kafka's protocol.

Keep these producer settings at both deployment sizes:

| Setting | Value | Behavior |
|---|---|---|
| `acks` | `-1` | Wait for all in-sync replicas: one at N=1 and all RF replicas at N=many. |
| `idempotence` | `true` | Enable producer deduplication. |
| `linger.ms` | `5` | Batch up to 5 ms of records at the cost of up to 5 ms latency. |
| `batch.size` | `65536` bytes | Bound each producer batch. |
| `compression` | `lz4` | Compress each batch. |

`internal/audit/kafka_recorder.go` must not branch on N. Broker configuration
controls replication behavior. `cmd/audit-consumer` runs the same Go binary at
either size, consumes with franz-go, writes YugabyteDB, and commits Kafka offsets
after the ledger transaction. N=1 uses Compose `replicas: 1` and the app image
`tack-server:latest`. N=many uses Compose or Kubernetes with `replicas: M` and
consumer-group partition assignment.

## Preserve shard identity

`internal/audit/canonical.go:87` defines 256 `(org_id, shard)` chains using
an 8-bit CRC32 modulo. Each shard's `prev_hash` refers to its own prior row.
Changing that mapping would require original payloads to rehash historical rows.
Keep the 256-chain mapping permanently. Initially, a few active shards perform
all writes while others remain empty. More traffic uses the existing shards.

Create the Kafka topic with 256 partitions. The original mapping assigns shard
`s` to partition `s` and uses packed `(org_id, shard)` producer keys. Actor and
event-ID hashing balances activity within large tenants. Partitioning by tenant
would tie partition count to tenant count. Partitioning by event type would add
producer routing logic without resolving a disproportionately active tenant.

The component-selection estimates place broker capacity above roughly 1M events
per second (EPS). For at least the first 10x growth above that rate, retain 256
partitions and add brokers for more disk and CPU per partition. A contingency
creates `audit.events.v2` with more partitions, redirects new events, and consumes
the old topic to completion. That contingency still needs a chain-continuation
migration from each v1 head; it is not an approved resharding procedure.

Use these topic settings:

| Property | N=1 | N=many |
|---|---|---|
| Broker count | 1 combined `broker,controller` | 3+ with 3 or 5 controller voters |
| KRaft metadata replication | 1 | 3 |
| Partition count | 256 | 256 |
| Topic replication factor | 1 | 3 |
| Retention bytes | 50 GiB | 1 TiB |
| Retention milliseconds | 7 days | 30 days |
| Compression | producer lz4 | producer lz4 |
| Cleanup policy | delete | delete |
| Min in-sync replicas | 1 | 2 |
| `unclean.leader.election.enable` | false | false |

Retention permits recovery from projector outages: seven days initially and
thirty days for regional failures. Three controller voters tolerate one failure;
five tolerate two. Use three for the first 3-broker expansion and five for
multiple regions. Combined roles work with three brokers, but dedicated
controllers isolate metadata work from broker load at production scale.
Require `min.insync.replicas=2` and `acks=all` before a replicated send succeeds.
Never elect an out-of-sync replica.

Weekly `audit.events` partitions use 8 tablets in development and more in
production, as in `migrations/002_audit.sql:57`. Expansion changes tablet counts
rather than partition semantics. Hash-partitioned `audit.chain_heads` uses
`(org_id, shard)` at line 124. Each head advances serially; independent heads
can advance concurrently. `audit.consumer_offsets` stores
`(topic, partition, offset, updated_at)` keyed by `(topic, partition)`, with
256 rows and one read-modify-write per projector commit.

## Record events at either size

The cutover plan supersedes the state-change and read-class sequences below.
After cutover, both classes publish to Kafka, and audit-consumer alone writes
ledger rows and chain heads.

The original state-change sequence is:

1. An MCP wrapper or RPC handler invokes
   `audit.SuppressingRecorder.Record(ctx, ev)` at `internal/audit/context.go:97`.
2. The recorder returns nil when `IsSuppressed(ctx)` is true.
3. `RecorderRouter` checks `IsStateChange(Verb(ev.Verb))` from
   `internal/audit/verbs.go:84`.
4. It invokes `YBRecorder.Record` at `internal/audit/yugabyte.go:66` synchronously.
5. The recorder writes PII, computes the shard, reads the chain head, computes
   the hash, inserts `audit.events`, updates `audit.chain_heads`, and commits.
6. It returns after YugabyteDB durably commits without requiring Kafka.

The original read-class sequence shares steps 1 through 3, then invokes
`KafkaRecorder.Record`. It synchronously publishes full Event JSON to
`audit.events.v1` with packed `(org_id, shard)` keys. At N=1, `acks=-1` and
`min.insync.replicas=1` wait for the sole replica. At N=many, the minimum is
2 of 3 replicas. Success returns nil; failure returns the broker error.

The asynchronous consumer processes batches. For each record it writes PII
when `hasPII`, calls the existing `shardOf`, reads the head, computes the hash,
inserts the event, updates the head, and upserts its offset in one YugabyteDB
transaction. Only after commit does it commit the consumer-group Kafka offset.
At N=many, Kafka assigns each instance a subset of partitions. The same logic
processes each assigned partition, and the smart driver selects its tablet.

State-change writes at N=many use the smart cluster endpoint and tablet leader
with synchronous follower replication. Cross-replica consensus increases latency
without changing semantics. `KAFKA_BROKERS` changes from one host to a
comma-separated bootstrap list; `AUDIT_WRITER_DSN` changes to a cluster endpoint.
`RecorderRouter` and the schema do not change. Consumer configuration changes
only replica count. Archive configuration changes the S3 endpoint.

The notarizer election conditional, `if replicas > 1 do leader election else
proceed`, is the one proposed code difference. Election at every count would
make expansion configuration-only; resolve that choice before claiming
identical source at every size.

## Query and verify the ledger

Operator commands query YugabyteDB using `AUDIT_READER_DSN`, selecting a
single-node endpoint initially and a cluster endpoint after expansion.

For each `(org_id, shard)`, `audit.chain_heads` stores `(last_seq, last_hash)`.
A ledger transaction computes `hashRow(last_hash, payload)` at
`internal/audit/canonical.go:97`, inserts the event, and updates the head.
YugabyteDB serializes concurrent writes to that head while permitting different
shards to write concurrently.

The original estimates predict sub-millisecond local head updates and
single-digit-millisecond replicated updates. A replicated shard supports a few
thousand transactions per second, giving a few hundred thousand EPS over 256
shards. Exceeding this requires either unresolved resharding or another
transaction primitive, such as FoundationDB or a Kafka per-partition state
machine. These estimates do not replace deployment measurements.

Every 60s by default, the notarizer signs a Merkle root over
`(org_id, shard, last_seq, last_hash)` tuples. Verify identically at either size:

1. Read the latest `audit.notarizations` row.
2. Verify its Ed25519 signature against the published `signing_key`.
3. Compare every tuple in `shard_heads` JSON with `audit.chain_heads`.
4. Read each shard from seq=1 through last_seq. Compare `prev_hash` with the
   prior `row_hash` and recompute `hashRow(prev_hash, canonical(payload))`.
5. Require every comparison to match through the notarization timestamp.

External attestation remains optional and unimplemented. A future Sigstore Rekor
or public CT-style log could publish signed roots and store inclusion proofs in
`audit.notarizations_external_proof` beside the existing notarization.

## Bound backlog and isolate workloads

Initially, Kafka fsync limits read-class publication while YugabyteDB commit
latency limits state changes. After expansion, replica acknowledgments limit
publication and YugabyteDB consensus limits state changes.

When Kafka cannot accept records fast enough, franz-go buffers to its memory cap,
then blocks or rejects under `max.block.ms`. `Record` returns an error. The
original caller contract aborts state changes on recording failure and logs
read-class errors without blocking the HTTP request.

When projection lags beyond topic retention, Kafka deletes unread events.
Page on `audit_kafka_lag_seconds` before that loss. One partition's lag does
not slow another partition's publication or consumption. Separate projector
instances and independent YugabyteDB Raft tablets isolate their work. A shared
projector can still delay several shards when it cannot process its load.
Limit each projector to 64 partitions and scale instance count with workload.
Hard per-shard isolation requires 256 statically assigned consumer instances,
one per partition, at a larger operational cost.

## Recover from failures

At N=1, use these recovery actions:

| Failure | Recovery |
|---|---|
| App crash | Restart the container and reconnect the producer. |
| Kafka crash | Restart the combined KRaft broker. A crash can discard unacknowledged producer-buffer events. |
| Consumer crash | Restart from the last committed offset. |
| YugabyteDB crash | Restart the database. Chain advancement waits and state-change writes fail until recovery. |
| SeaweedFS crash | Restart storage; archive writes fail until recovery. |
| CT 117 loss | Restore from backups. The original incident review identified broken backups as the largest initial risk. |

At N=many, use these recovery actions:

| Failure | Recovery |
|---|---|
| App host loss | Other app hosts continue and brokers rebalance. |
| Broker loss | RF=3 preserves availability; rebalance after rejoin. |
| Projector loss | Consumer-group rebalance assigns its partitions to survivors. |
| YugabyteDB node loss | A surviving majority elects tablet leaders. |
| Network partition | Minority brokers and database partitions become read-only; minority producers return explicit failures. |
| Availability-zone loss | Fail over xCluster to the surviving region and update DNS. |
| Competing leaders | YugabyteDB Raft and Kafka KRaft enforce consensus. |
| Rebalance storm | Tune `session.timeout.ms` and `max.poll.interval.ms`. |

A notarizer follower replaces a failed leader within one cadence, default 60s.
If the lock service fails, every signer refuses work until it recovers.
Notarization pauses without changing chain integrity. Brief duplicate leaders
write redundant valid notarizations rather than incorrect rows.

The abandoned dual-write proposal required parity checks when WAL and Kafka
records differed. The accepted hard cutover does not use a dual-write window
or an `events_v2` table. Follow the cutover plan for migration
`003_audit_kafka_cutover.sql`, sole consumer ownership, keying, and rollback.

## Expand beyond one host

1. Provision a second host and start broker 2 in combined KRaft mode. Add it to
   broker 1's `controller.quorum.voters` first and restart members in order.
2. Add broker 3 before RF=3, or use RF=2 temporarily. Set
   `min.insync.replicas=2` with `kafka-configs.sh` alter mode and apply a generated
   reassignment plan with `kafka-reassign-partitions.sh`.
3. Update app `KAFKA_BROKERS` to the comma-separated bootstrap list. Franz-go
   discovers other members from that list.
4. Start the second app and consumer on host 2; let the group rebalance.
5. Add YugabyteDB node 2 and expand the cluster. Configure xCluster for regions.
6. Add SeaweedFS volume server 2 and replicate the filer, or add the third
   Garage node to satisfy its 3-node minimum. Preserve the load-balanced S3 API.
7. Enable notarizer election and at least two replicas after resolving the
   election conditional. The remaining expansion does not require code changes.

## Plan operations and capacity

The component-selection research used these operational estimates:

| Deployment | Staffing and on-call | Monitoring and capacity | Cost |
|---|---|---|---|
| N=1 | One part-time operator with best-effort response; reboot CT 117 for recovery. | Existing slog plus Kafka lag and commit-rate metrics. At 0.37 EPS, the estimate predicts more than five orders of capacity margin. Budget roughly 2 GiB JVM heap for one small combined broker. | Within an order of magnitude of current hosting; Kafka uses the existing host and SeaweedFS its own LXC. |
| N=2 | About 0.5 additional FTE for streaming and clusters, with a real on-call rotation rather than relying on host reboot. | Add inter-host network, broker quorum, and database health. Plan topic changes and partition reassignment. Backups must include both hosts. | Roughly 2x to 3x N=1, including bandwidth. |
| N=many | 5 to 20 FTE and 24/7 response. | Track per-tenant and per-region service-level indicators and cross-region health continuously. | Hardware and staffing estimates total high six to seven figures monthly at 1M EPS. |

The N=2 requirement includes a second host, broker, app, and consumer, sufficient
inter-host bandwidth, multi-host backups, on-call coverage, and host health
monitoring. Dedicated controllers can follow the initial combined-role broker.

## Resolve open questions and protect recovery evidence

- Resolve global versus scoped address-index semantics before multi-host audit
  queries. The incident review's section 1B reports a global index.
- Define a reverse address index for forensic lookup of node references.
- Define a solution before one shard requires sustained 1k to 4k EPS beyond
  its serial head-update capacity. A new topic alone does not preserve chains.
- Choose advisory locks, FoundationDB compare-and-swap, or a Kafka lease for
  notarizer election.
- Decide whether customer compliance needs require external root attestation.
- Define Event schema evolution. A consumer-first added JSON field is compatible;
  a producer-first removed field becomes a zero value in the old consumer.
  Consider `schema_version` rather than silently relying on that behavior.
- Define hard shard isolation and bulk PII redaction before claiming support.
- Keep event insertion and offset advancement in one database transaction.
  Manual corruption that advances `audit.consumer_offsets` can still skip events;
  this design does not protect against that operator action.
- Treat single-broker topic-disk loss at RF=1 as a known risk to in-flight events.
  Replication at N=many corrects that failure exposure.
- Test restores for every broker and SeaweedFS backup. Prior backups silently
  contained empty data; the defect can recur in new stores.

The cutover plan replaces the earlier shadow-recorder and sibling-table file
inventory. The consumer binary is `cmd/audit-consumer`. Do not add a shadow
recorder or an `events_v2` table.
