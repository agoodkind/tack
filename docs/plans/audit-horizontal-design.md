# Tack Audit Subsystem: Horizontal-From-Day-One Design

Prepared 2026-05-09 as the post-Phase-1 architectural target. This document
describes a single audit architecture that runs identically on N=1 (today,
CT 117, one operator) and on N=many (future, multi-host, multi-region) with
no architectural rewrite between the two. The N=1 deployment is what ships
first; subsequent N=many deployments are config and ops, not code rewrites,
except where this document explicitly identifies open questions.

The companion document at
`/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/audit_scale_architectures.md`
is the input for component selection. Its findings are referenced inline.

---

## 1. Design constraints

The constraints listed here are absolute. Every component choice in section 3
must be testable against this list.

### 1.1 Single node deployable today

The first deployment of this design runs on CT 117 inside the existing
docker-compose stack. No new host. No external SaaS dependency. No SLA-bound
managed service. The operator brings up the architecture by editing
`docker-compose.yml` and `cmd/server/main.go`, runs `make deploy`, and
production audit traffic flows through the new path.

### 1.2 No architectural rewrite required to add nodes 2 through N

The same components present at N=1 are the same components present at
N=many. The cardinality of each component changes, the placement (host,
network, region) changes, and configuration changes. The component
identities (broker product, hot store product, cold archive product,
notarizer process, PII service) do not change.

This rules out single-host-only choices that have no horizontal-scale
analog: SQLite as the broker, on-host file system as the cold archive,
in-memory key-value caches that cannot be partitioned. It permits choices
that scale by configuration (Apache Kafka with one broker scales to many
brokers by reconfiguration; ClickHouse with one shard scales to many shards
by reconfiguration).

### 1.3 Every component runs identically on N=1 and N=many

The producer code, consumer code, notarizer code, and PII redactor code
are byte-identical at N=1 and N=many. Configuration differs. Operational
runbooks differ. Source code does not.

This is the strongest constraint and the one most likely to slip. Every
section 3 choice is justified against it.

### 1.4 Compliance integrity at every N

Hash chain plus periodic Merkle notarization plus PII separation must work
at N=1 and at N=many. The chain bytes produced at N=1 must be verifiable
by the same verifier that handles N=many bytes. A migration from N=1 to
N=many cannot break or rewrite chain history.

### 1.5 Phase 1 stands

The Phase 1 WAL fix at
`/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/audit_two_phase_plan.md`
section 2 is the path that ships first. This design is what ships after
Phase 1 stabilizes, not what replaces Phase 1 mid-flight. Phase 1
preserves the Yugabyte path; this design replaces the WAL with a broker
without touching the Yugabyte path until the broker has parity.

### 1.6 State-change verbs never depend on the broker

Per `internal/audit/wal.go:239` and per the compliance contract, state-change
verbs must persist their audit row synchronously with the operation. A
broker outage cannot break state-change writes. State-change verbs go
straight to the durable hot store at N=1 and N=many. Read-class verbs go
through the broker.

This constraint divides the audit architecture into a sync path (state
change) and an async path (read class) that share storage but not
buffering.

---

## 2. Target architecture diagram

The same components at N=1 and N=many. Cardinality differs. Identity
does not.

### 2.1 N=1 (CT 117 today)

```
+-------------------------------------------------------------+
| CT 117 (single host)                                        |
|                                                             |
|  +------------+    Recorder.Record(ctx, ev)                 |
|  | tack-app   |---------------+                             |
|  +------------+               |                             |
|       |                       v                             |
|       |              +------------------+                   |
|       |              | RecorderRouter   |                   |
|       |              +------------------+                   |
|       |                |              |                     |
|       |     state-change           read-class               |
|       |        (sync)               (async)                 |
|       |          |                    |                     |
|       |          v                    v                     |
|       |    +-----------+      +----------------+            |
|       |    | YBRecorder|      | KafkaRecorder  |            |
|       |    | (sync     |      | (produce       |            |
|       |    |  hash     |      |  to topic)     |            |
|       |    |  chain)   |      +----------------+            |
|       |    +-----------+              |                     |
|       |          |                    v                     |
|       |          |          +-------------------+           |
|       |          |          | Apache Kafka      |           |
|       |          |          | broker (1 node,   |           |
|       |          |          | KRaft mode)       |           |
|       |          |          | topic:            |           |
|       |          |          |   audit.events.v1 |           |
|       |          |          | partitions: 256   |           |
|       |          |          | replication: 1    |           |
|       |          |          +-------------------+           |
|       |          |                    |                     |
|       |          |                    v                     |
|       |          |          +-------------------+           |
|       |          |          | audit-projector   |           |
|       |          |          | binary (1 inst.)  |           |
|       |          |          | - hash chain      |           |
|       |          |          | - PII split       |           |
|       |          |          | - dedup by ev_id  |           |
|       |          |          +-------------------+           |
|       |          |                    |                     |
|       |          v                    v                     |
|       |    +-----------------------------+                  |
|       |    | YugabyteDB (single node)    |                  |
|       |    | - audit.chain_heads (sync)  |                  |
|       |    | - audit.events (hot)        |                  |
|       |    | - audit.notarizations       |                  |
|       |    | - audit.pii                 |                  |
|       |    | - audit.consumer_offsets    |                  |
|       |    +-----------------------------+                  |
|       |                                                     |
|       |                +-----------------+                  |
|       +--------------->| audit-notarizer |                  |
|                        | binary (1 inst.)|                  |
|                        | - signs Merkle  |                  |
|                        |   roots (60s)   |                  |
|                        +-----------------+                  |
|                                                             |
|       +-----------------------------+                       |
|       | ClickHouse (1 node, optional in N=1)                |
|       | - hot OLAP, 90 day retention                        |
|       | - fed by audit-projector parallel sink              |
|       +-----------------------------+                       |
|                                                             |
|       +-----------------------------+                       |
|       | SeaweedFS (S3 API)          |                       |
|       | (Iceberg cold archive)      |                       |
|       +-----------------------------+                       |
|                                                             |
+-------------------------------------------------------------+
```

ClickHouse and the cold archive are physically present at N=1 even though
ClickHouse is overkill for 0.37 EPS. Their presence at N=1 is the price
of horizontal-from-day-one. The N=many architecture cannot introduce them
later without code changes; they must be in the path from day one. See
section 3.2 for justification.

### 2.2 N=many (future multi-host)

```
+--------+         +--------+         +--------+
| Host A |         | Host B |         | Host C |
| tack-  |         | tack-  |         | tack-  |
| app    |         | app    |         | app    |
+--------+         +--------+         +--------+
    \                  |                  /
     \                 |                 /
      \                v                /
       \   +--------------------+      /
        +->| Apache Kafka       |<----+
           | cluster (KRaft)    |
           | brokers: 3 to N    |
           | topic:             |
           |   audit.events.v1  |
           | partitions: 256    |
           | replication: 3     |
           +--------------------+
              |              |
              v              v
   +-----------------+  +-----------------+
   | audit-projector |  | audit-projector |
   | instance 1      |  | instance 2..M   |
   | (consumer       |  | (consumer       |
   |  group:         |  |  group:         |
   |  audit-proj)    |  |  audit-proj)    |
   +-----------------+  +-----------------+
              |              |
              v              v
       +-----------------------------+
       | YugabyteDB cluster          |
       | (3 to N nodes, multi-region |
       |  via xCluster)              |
       | - audit.chain_heads         |
       | - audit.events              |
       | - audit.notarizations       |
       | - audit.pii                 |
       | - audit.consumer_offsets    |
       +-----------------------------+
              |
              v
       +-----------------------------+
       | ClickHouse cluster          |
       | (3 to N shards x R replicas)|
       | - hot OLAP (90 day)         |
       +-----------------------------+
              |
              v
       +-----------------------------+
       | Iceberg on SeaweedFS        |
       | (or Garage); S3-compatible  |
       | - cold archive (forever)    |
       +-----------------------------+

   +------------------+
   | audit-notarizer  |  (single active instance with leader election;
   | (leader-elected) |   followers warm)
   +------------------+
```

The boxes are the same set as N=1. Cardinality has changed; placement has
changed; replication factors have changed. No box is new. No box has
disappeared.

---

## 3. Component selection with horizontal-day-one rationale

Every choice below answers the same question: does this component scale
out by configuration, with no producer-side or consumer-side code change?

### 3.1 Broker: Apache Kafka (KRaft mode)

Choice: Apache Kafka 4.x in KRaft mode, Apache 2.0 licensed, no ZooKeeper.

Justification against horizontal-day-one:

- **Wire protocol stability.** Apache Kafka defines the wire protocol.
  Producer code (Go: `twmb/franz-go`) and consumer code do not change
  when scaling from one broker to many. Section 2.1 of
  `audit_scale_architectures.md` documents that the wire protocol is the
  contract; the broker product behind it is replaceable.
- **Partition count is set at topic creation.** Topic created with 256
  partitions. At N=1 all 256 partitions live on one broker; at N=many
  they redistribute. Producers do not care; partition assignment by key
  hash is wire-protocol-level. Per
  [Kafka producer config](https://kafka.apache.org/documentation/#producerconfigs).
- **Replication factor changes by broker reconfiguration.** RF=1 at N=1;
  RF=3 at N=many is a `kafka-configs.sh` alter plus a partition reassignment
  pass driven by `kafka-reassign-partitions.sh`. No producer or consumer
  code change.
- **KRaft removes ZooKeeper.** Kafka 4.0 (March 2025) made KRaft the
  only supported metadata mode and removed ZooKeeper entirely. Per
  [Apache Kafka 4.2.0 release](https://kafka.apache.org/blog/2026/02/17/apache-kafka-4.2.0-release-announcement/).
  At N=1 a single Kafka process runs the controller and broker roles
  in one JVM (combined `process.roles=broker,controller`). No separate
  metadata service, no separate controller process. Operational
  footprint is closer to a single-binary broker than the historic
  Kafka-plus-ZooKeeper picture.
- **Consumer-group rebalancing handles N=many natively.** When a second
  projector instance starts, the consumer group rebalancer reassigns
  partitions automatically. The projector binary does not need to know
  whether it is one of one or one of many.
- **Native Kafka transactional commit.** The projector's "consume,
  project to YB, commit Kafka offset" loop uses Kafka transactional
  semantics so a projector crash does not double-commit.
- **License is Apache 2.0.** Satisfies the operator's "100% open source,
  no BSL, no proprietary, no managed cloud services" constraint.

See section 3a for a fuller discussion of why Kafka was preferred over
Redpanda, NATS JetStream, and Apache Pulsar.

N=1 behavior: one Apache Kafka process running combined
`broker,controller` KRaft roles, 256 partitions on one broker, RF=1,
no partition leader election across brokers (single replica is leader
by default). Metadata replication factor (`controller.quorum.voters`)
is 1 at N=1.

N=many behavior: 3+ Kafka brokers, separate or combined controller
quorum (3 or 5 controller voters typical), 256 partitions distributed
across brokers, RF=3, partition leadership rebalanced on broker
addition through `kafka-reassign-partitions.sh` or Cruise Control.

### 3a. Broker selection rationale

This subsection records why Apache Kafka was chosen over the three
alternatives that were on the short list. The operator constraint that
drove the decision is "100% open source, no BSL, no proprietary, no
managed cloud services."

#### 3a.1 Kafka over Redpanda

Redpanda Community Edition is licensed under the Business Source
License (BSL), which is a source-available license, not an open-source
license under the Open Source Initiative definition. BSL imposes
production-use restrictions for a defined period before the source
converts to an open license. The operator's constraint excludes BSL,
so Redpanda Community Edition is not eligible regardless of its
technical merits.

Redpanda's technical advantages (single C++ binary, no JVM, lower
tail latency) were attractive when an earlier draft of this document
selected it. With Kafka 4.x in KRaft mode, the operational gap has
narrowed: Kafka no longer needs ZooKeeper, and a single combined
`broker,controller` process runs as one JVM with one config file.
The remaining gap (JVM tuning, GC pauses) is a real cost but not a
disqualifier at Tack's scale.

#### 3a.2 Kafka over NATS JetStream

NATS JetStream is Apache 2.0 licensed and was a serious candidate.
The disqualifier is the
[Jepsen NATS 2.12.1 analysis](https://jepsen.io/analyses/nats-2.12.1)
published 2025-12-08, which documents multiple unresolved data-loss
scenarios in JetStream. For an audit subsystem that must hold the
durability authority for compliance evidence, an unresolved
silent-data-loss finding by Jepsen is a hard stop. Apache Kafka does
not have a comparable published finding at this severity. The
risk-adjusted choice is Kafka.

If NATS JetStream's findings are resolved in a future release and
re-audited cleanly, this decision is worth revisiting; the wire
protocol contract argument for Kafka does not extend to JetStream
(JetStream uses NATS subjects, not Kafka partitions), so a swap
would require producer and consumer code change.

#### 3a.3 Kafka over Apache Pulsar

Apache Pulsar is Apache 2.0 licensed and offers stronger multi-tenancy
primitives than Kafka (per-tenant namespaces, geo-replication built
into the broker). It was rejected for two reasons:

- **Operational complexity.** Pulsar deploys as broker plus BookKeeper
  plus ZooKeeper (or, in newer versions, an alternative metadata
  store). Three coordinated services versus one Kafka process is a
  larger footprint at N=1.
- **Smaller self-host community.** The Go client ecosystem and the
  body of self-host operational knowledge are smaller than Kafka's.
  A one-operator deployment depends on community runbooks and
  StackOverflow density.

Pulsar's multi-tenancy is genuinely better than Kafka's, but Tack
does not need broker-level multi-tenancy because tenant isolation is
enforced upstream at the producer (per `(org_id, shard)` partition
key) and downstream at the projector (per-tenant rows in Yugabyte).

#### 3a.4 Acknowledgment

Kafka is operationally heavier than the alternatives. JVM tuning is
real work, GC pause monitoring is real work, and a Kafka cluster has
more knobs than a Redpanda cluster or a JetStream cluster. The
trade-off is accepted because Kafka is the only candidate that
satisfies all of:

- Open source under an OSI-approved license (Apache 2.0).
- Audited at scale by independent third parties (multiple Jepsen
  analyses across versions, none currently flagging unresolved
  silent-data-loss).
- Apache Software Foundation governance (vendor-independent project
  control).
- Mature self-host operational knowledge across the industry.

### 3.2 Hot storage: Yugabyte for the chain plus ClickHouse for the OLAP read tier

Choice: dual hot tier. Yugabyte holds the integrity tables
(`audit.chain_heads`, `audit.events`, `audit.notarizations`, `audit.pii`,
`audit.consumer_offsets`). ClickHouse holds the projection of `audit.events`
optimized for analytical query.

This is a deliberate departure from the
`/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/audit_two_phase_plan.md`
section 3 plan, which used Yugabyte alone. The change is motivated by
section 5.5 of `audit_scale_architectures.md`: Yugabyte's published
throughput tops out around 60k to 200k inserts per second per node, which
becomes the bottleneck at the multi-million-EPS endgame. ClickHouse
sustains hundreds of thousands of inserts per second per shard at the
same hardware tier.

Justification against horizontal-day-one:

- **Yugabyte already runs in the stack.** Tack's auth tables and the
  current `audit.events` table live in Yugabyte. Adding the audit tables
  is config (the tables are already there), not new infrastructure.
- **ClickHouse runs as a single-node process at N=1.** Same Compose
  service shape as Yugabyte. ClickHouse-Keeper enables cluster mode by
  configuration when N=many. The `clickhouse-server` binary is the same
  in both cases.
- **The chain integrity tier (Yugabyte) and the query tier (ClickHouse)
  are decoupled.** The projector writes to both. ClickHouse outage does
  not stop chain advancement. Yugabyte outage stops chain advancement
  (which is the correct behavior; the integrity tier is the durability
  authority).
- **MCP audit query tools query both.** Recent rows (under 90 days, or
  under whatever ClickHouse retention is configured) come from
  ClickHouse for speed. Older rows or chain-verification queries come
  from Yugabyte. Two paths from day one.

Why not Yugabyte alone:

- At Tack's current scale Yugabyte is enough. At the 1M EPS endgame
  Yugabyte cannot serve analytical query latency for cross-tenant audit
  forensics in seconds. Either ClickHouse goes in from day one or it
  has to be retrofitted later, which violates constraint 1.2.

Why not ClickHouse alone:

- Yugabyte is the existing transactional store and already holds
  `audit.chain_heads`. The hash chain advancement is a transactional
  read-modify-write; Yugabyte's distributed SQL semantics are designed
  for this. ClickHouse is not a transactional store.

N=1 behavior: one Yugabyte node holding the chain, one ClickHouse node
holding the OLAP projection. ClickHouse is a small process at N=1 (a few
hundred MB of memory, single shard).

N=many behavior: Yugabyte cluster with xCluster replication for
multi-region. ClickHouse cluster with shards and replicas.

### 3.3 Cold archive: Iceberg tables on SeaweedFS (S3-compatible)

Choice: Apache Iceberg table format, Parquet files, SeaweedFS as the
S3-compatible storage backend at every N. Garage is the noted
alternative for operators who prefer the more modern self-host-focused
design. Both are Apache 2.0 (SeaweedFS) and AGPL (Garage) respectively;
both have been verified as actively maintained in 2026.

The choice of SeaweedFS as the primary recommendation is operational
stability: SeaweedFS has a longer release history, broader deployment
footprint, and a larger community of self-host operators. Garage is
selected by operators who prefer its simpler single-binary model, its
modern Rust implementation, or its stricter geo-replication semantics;
both expose the same S3 API and the archiver binary does not change
between them.

This is a deliberate move away from the earlier draft's choice of
MinIO at N=1 plus AWS S3 at N=many. AWS S3 is a managed cloud service
and is excluded by the operator constraint; MinIO's licensing has
become operator-unfriendly enough that SeaweedFS and Garage are
preferable in the open-source-only stack.

Justification against horizontal-day-one:

- **SeaweedFS exposes the S3 API.** A single-LXC SeaweedFS at N=1
  exposes the same S3 API that a SeaweedFS cluster exposes at N=many.
  The archiver binary uses the same S3 SDK in both cases.
- **Iceberg metadata commits scale by configuration.** At N=1 the
  Iceberg catalog can be a small SQLite or a single-file JSON catalog.
  At N=many it can be a self-hosted Nessie catalog. The Iceberg table
  format itself is the same.
- **Parquet files are immutable.** Writing once and never rewriting is
  the cold-archive contract. Hash chain integrity is preserved because
  the file content is hash-addressable.
- **The archiver binary is identical at N=1 and N=many.** It reads from
  Yugabyte (the canonical store), groups rows into Parquet files
  partitioned by week and by org, writes to the S3 endpoint, commits
  to the Iceberg catalog. No code branches on N. No code branches on
  whether the backend is SeaweedFS or Garage.

N=1 behavior: SeaweedFS on its own LXC, the weed binary under systemd, single
node, single bucket, a local data directory. Catalog is a single SQLite file.

N=many behavior: SeaweedFS cluster (multiple volume servers, replicated
filer) or Garage cluster (3-node minimum), with a self-hosted Nessie
Iceberg catalog. Nightly archiver job runs as a Kubernetes Cron at
N=many or as a Compose-scheduled task at N=1.

### 3.4 Notarizer: leader-elected single active replica

Choice: the existing `internal/audit/notarizer.go` design with one change:
add a leader-election preamble that uses Yugabyte advisory locks at N=1
or FoundationDB CAS at N=many. The notarizer reads `audit.chain_heads`,
computes Merkle root, signs Ed25519, writes to `audit.notarizations`.

Justification against horizontal-day-one:

- **Single active notarizer is correct at any N.** The notarizer is not
  on the throughput path. It runs once per minute. Running it on multiple
  hosts simultaneously produces redundant `audit.notarizations` rows;
  not incorrect, but wasteful. Leader election ensures one active.
- **Leader election uses already-available primitives.** Yugabyte
  PostgreSQL-compatible advisory locks at N=1; FoundationDB CAS as
  documented in `CLAUDE.md` at N=many; or pluggable to use Kafka
  consumer-group as the lock at any N.
- **Same Ed25519 key at N=1 and N=many.** The signing key is a single
  Ed25519 private key. At N=many the key is held by a leader-elected
  replica; the followers hold a copy and become active on election.
  Operational rotation requires a key handover protocol, which is the
  same protocol at N=1 and N=many. See section 9 for the rotation
  details.

Why not a notarizer per shard:

- Per-shard notarizers would require per-shard signing keys, which
  fans out the key-management surface area. The single-key design with
  a Merkle aggregate is the Sigstore Rekor model and matches Tack's
  existing `internal/audit/notarizer.go`.

N=1 behavior: one notarizer process, no leader election (only one
replica). Holds the signing key on disk at `/etc/tack/audit-signing.pem`.

N=many behavior: notarizer process runs on multiple hosts; leader
election picks one active. Followers warm. Signing key replicated to
all replicas (via secret-management primitive: file mount at N=1, an
open-source secret manager such as OpenBao at N=many; HashiCorp Vault
is excluded under the BUSL-1.1 license).

### 3.5 PII store: existing `audit.pii` table

Choice: the existing `audit.pii` table in Yugabyte. No change.

Justification against horizontal-day-one:

- **Yugabyte multi-region replication scales the PII table.** xCluster
  replication for the audit schema covers `audit.pii` along with
  everything else.
- **The producer writes PII synchronously before the broker produce.**
  Per `internal/audit/yugabyte.go:113-134`. This is preserved at
  N=many.
- **GDPR redaction works at any N.** The `audit_redactor` role updates
  `audit.pii.payload`; the redaction is replicated by Yugabyte. The
  hash chain is unaffected because it never includes the PII payload.

N=1 behavior: writes go to single-node Yugabyte. Same as today.

N=many behavior: writes go to the Yugabyte primary; replication to
followers handles read scale.

### 3.6 Producer client: `twmb/franz-go`

Choice: `github.com/twmb/franz-go` (MIT licensed) for the Kafka producer
client. Most-cited modern Go Kafka client; pure Go (no CGO); supports
the full Kafka protocol including transactions, idempotence, and
exactly-once semantics. Recommended for performance and modern API
ergonomics.

Alternative considered: `github.com/segmentio/kafka-go` (MIT licensed),
also pure Go and widely deployed. Selected against because its API is
older and its transactional-producer support is less complete than
franz-go's. If the operator has an existing `segmentio/kafka-go`
preference for some reason, the wire-protocol contract holds and the
swap is local to `KafkaRecorder`.

Configuration values that are the same at N=1 and N=many:

- `acks = -1` (all in-sync replicas). At N=1 this is "the one replica."
  At N=many this is "all RF replicas."
- `idempotence = true`. Always.
- `linger.ms = 5`. Allows the producer to batch up to 5 ms worth of
  records. Trades 5 ms of latency for produce throughput.
- `batch.size = 65536` bytes. Producer-side batching cap.
- `compression = lz4`. Per-batch compression.

The producer code in `internal/audit/kafka_recorder.go` (new file)
contains zero conditional logic on N. The behavior at N=1 versus N=many
is determined entirely by broker configuration.

### 3.7 Consumer binary: `cmd/audit-projector`

Choice: a single Go binary in a new `cmd/audit-projector` directory.
Reads from Kafka via franz-go consumer-group; writes to Yugabyte and
ClickHouse; commits Kafka offsets transactionally.

Deployment model:

- **N=1.** Compose service `audit-projector` with `replicas: 1`. Container
  shares the same Docker image as `tack-app` (build-time tag
  `tack-server:latest`).
- **N=many.** Compose service or Kubernetes Deployment with `replicas: M`
  where M scales with topic partition count. Kafka consumer-group
  rebalancing handles partition assignment.

The binary is the same at both Ns. The `replicas` count is the only
configuration difference.

---

## 4. Sharding model

The hash chain in `internal/audit/canonical.go:87` is per
`(org_id, shard)` with shard in 0 to 255 (8-bit CRC32 modulo). This
section confirms that choice and clarifies its scaling behavior.

### 4.1 Shard count is fixed at 256 at every N

The shard count is a property of the chain, not of the deployment. A
chain at shard 5 has prev_hash referring to the previous row at shard 5;
changing shard count after the fact would break the chain unless the
existing rows were rehashed (which requires the original payloads).

Therefore: shard count is fixed at 256 forever. At N=1 most shards have
zero rows and the few active shards carry the entire load. At N=many
load distributes evenly across all 256 shards.

This is the same pattern Sigstore Rekor uses per
`audit_scale_architectures.md` section 4.1: per-shard chains, with shards
added by creating new logs rather than resharding existing logs.

### 4.2 Adding partitions later

The Apache Kafka topic also has 256 partitions, fixed at topic creation.
The mapping from shard to partition is identity: shard `s` maps to
partition `s`. The producer key is `(org_id, shard)` which yields a
deterministic partition assignment.

If a future scale demand exceeds what 256 partitions can carry (more than
roughly 1M EPS aggregate at the broker, per
`audit_scale_architectures.md` section 2.1's per-broker numbers), the
options are:

- **Option A.** Create a sibling topic `audit.events.v2` with more
  partitions; route new traffic there; drain the old topic. Hash chain
  migration is a non-trivial follow-up because v2 traffic on the same
  shard must continue from the v1 chain head. This is recorded as an
  open question in section 15.
- **Option B.** Keep 256 partitions at the broker; add brokers
  horizontally so each partition gets more disk and CPU per broker.
  Sustains the broker tier well into the multi-million-EPS range without
  changing partition count. This is the assumed scale-out path.

Option B is the chosen scale-out strategy for at least the first 10x of
growth above 1M EPS. Option A is the contingency.

### 4.3 Why not partition by event type or by tenant directly

Per `audit_scale_architectures.md` section 3.4: tenant-only partitioning
forces partition count to scale with tenant count. Hash-of-(actor,
event_id) partitioning balances load even when tenant distribution is
skewed. Tack already uses this pattern.

Partition by event type (`node.create` to one set, `auth.*` to another)
was rejected because it does not solve hot-tenant skew and adds a
type-aware routing decision that the producer would need to make. The
current actor-event hash already mixes types implicitly.

---

## 5. Topic and table partitioning

Concrete configuration values for both N=1 and N=many.

### 5.1 Apache Kafka topic configuration

| Property | N=1 | N=many |
|---|---|---|
| Broker count | 1 (combined `broker,controller` role) | 3+ brokers, 3 or 5 controller voters |
| KRaft metadata replication | 1 | 3 (controller quorum) |
| Topic partition count | 256 | 256 |
| Topic replication factor | 1 | 3 |
| Retention bytes | 50 GiB | 1 TiB |
| Retention milliseconds | 7 days | 30 days |
| Compression | producer (lz4) | producer (lz4) |
| Cleanup policy | delete | delete |
| Min in-sync replicas | 1 | 2 |
| `unclean.leader.election.enable` | false | false |

The retention values are intentionally larger at N=many because the
broker tier is the durability backstop during projector outages.
At N=1, a 7-day retention plus the Yugabyte-backed projection is
sufficient. At N=many, 30 days lets a regional projector outage be
resolved without losing in-flight events.

KRaft-specific notes for N=many:

- **Controller quorum sizing.** Three controller voters is the minimum
  for tolerating a single controller failure; five voters tolerate
  two. Tack defaults to three at the first horizontal step (N=many
  with 3 brokers in combined role) and to five at multi-region.
- **Combined versus separate controller and broker roles.** At N=1
  the single Kafka process runs `process.roles=broker,controller`. At
  N=many with 3 brokers, the combined role still works but separating
  controllers from brokers (dedicated controller-only nodes) is the
  recommended pattern at production scale because it isolates
  metadata-quorum contention from data-plane load.
- **`min.insync.replicas` plus `acks=all` at N=many** is the
  durability contract. At least two of three replicas must acknowledge
  before the producer's send returns success.
- **`unclean.leader.election.enable=false` is mandatory.** Audit data
  is durability-critical; an out-of-sync replica must never be
  elected leader because it can publish events that diverge from the
  hash chain.

### 5.2 Yugabyte table partitioning

`audit.events` is already partitioned by week per
`migrations/002_audit.sql:57`. Tablet count per partition is 8 in dev
and grows in production. No change at N=many beyond increasing tablet
count.

`audit.chain_heads` is hash-partitioned on `(org_id, shard)` per
`migrations/002_audit.sql:124`. This works at any N. At N=many the
hash distribution spreads heads across tablets; chain advancement is
serialized only within a single (org, shard).

`audit.consumer_offsets` (new table introduced for Phase 2) holds
`(topic, partition, offset, updated_at)` keyed on `(topic, partition)`.
At 256 partitions this table has 256 rows and is read-modify-write per
projector commit. Negligible at any N.

### 5.3 ClickHouse table partitioning

`audit.events_olap` (new ClickHouse table) is partitioned by
`toYYYYMMDD(event_time)` per day, with primary key
`(org_id, shard, event_time, event_id)`. Replication via
ReplicatedMergeTree at N=many; non-replicated MergeTree at N=1. The
table definition is the same at both Ns; the engine name differs.

---

## 6. Writer path

Step-by-step behavior of `Recorder.Record(ctx, ev)` at both Ns. Code
locations cited.

### 6.1 N=1 writer path (state-change verb)

1. MCP tool wrapper or RPC handler calls
   `audit.SuppressingRecorder.Record(ctx, ev)` (see
   `internal/audit/context.go:97`).
2. `SuppressingRecorder` checks `IsSuppressed(ctx)`. If suppressed,
   return nil. Same at any N.
3. Inner recorder is `RecorderRouter` (new type). Router checks
   `IsStateChange(Verb(ev.Verb))` per `internal/audit/verbs.go:84`.
4. State-change goes to `YBRecorder.Record` per
   `internal/audit/yugabyte.go:66`. Synchronous. Same at any N.
5. `YBRecorder` writes `audit.pii` row, computes shard, reads chain
   head from `audit.chain_heads`, computes hash, INSERTs `audit.events`
   row, UPDATEs `audit.chain_heads`, COMMITs.
6. Returns to caller. State-change is durable in Yugabyte.

No broker involvement. State-change verbs are not affected by broker
outage.

### 6.2 N=1 writer path (read-class verb)

1. Same steps 1 to 3 as above.
2. Router routes to `KafkaRecorder.Record` (new type).
3. `KafkaRecorder` synchronously produces the event JSON to Apache
   Kafka topic `audit.events.v1`, key = `(org_id, shard)` packed bytes,
   value = the full Event JSON.
4. `acks=-1` plus `min.insync.replicas=1` at N=1: producer waits for
   the single replica to acknowledge.
5. On success, Record returns nil. On failure, returns the broker
   error.

The `audit-projector` consumer reads from the topic asynchronously:

7. `audit-projector` consumes a batch.
8. For each record: writes the PII row (if hasPII), computes shard
   (same `shardOf` function as Phase 1), reads chain head, computes
   hash, INSERTs `audit.events`, UPDATEs `audit.chain_heads`,
   UPSERTs `audit.consumer_offsets`. All in one Yugabyte transaction.
9. Same record is also written to ClickHouse `audit.events_olap` in a
   separate (idempotent) write.
10. After Yugabyte commit succeeds, projector commits the Kafka offset
    via consumer-group commit.

### 6.3 N=many writer path (state-change verb)

Same steps 1 to 6 as N=1. The difference is in step 5:

5. `YBRecorder` connects through a Yugabyte cluster endpoint (smart
   client driver). Writes go to the tablet leader for the
   `(org_id, shard)` chain head, with synchronous replication to
   followers. Latency is higher than N=1 (cross-replica consensus)
   but write semantics are identical.

No producer code change.

### 6.4 N=many writer path (read-class verb)

Same steps 1 to 6 as N=1. The difference is in step 4:

4. `acks=-1` plus `min.insync.replicas=2` at N=many: producer waits
   for at least 2 of 3 replicas to acknowledge.

The projector now runs as multiple replicas (steps 7 to 10):

7. Each `audit-projector` instance is assigned a subset of the 256
   topic partitions by Kafka consumer-group rebalancing. Each
   instance is responsible for its assigned partitions only.
8. Per assigned partition, the instance runs the same per-record
   logic as N=1.
9. ClickHouse writes go to the appropriate shard via the Distributed
   table or via direct shard routing.
10. Yugabyte writes go to the appropriate tablet by smart-client
    routing.

### 6.5 Code-level changes by component

| Component | N=1 to N=many code change? | Configuration change? |
|---|---|---|
| `KafkaRecorder` | No | `KAFKA_BROKERS` env (comma-separated host list) |
| `YBRecorder` | No | `AUDIT_WRITER_DSN` (cluster endpoint vs single host) |
| `RecorderRouter` | No | None |
| `audit-projector` main loop | No | `replicas` count in deployment |
| `audit-notarizer` | One-line addition for leader election | Same |
| Yugabyte schema | No | None |
| ClickHouse schema | Engine name (MergeTree vs ReplicatedMergeTree) | Distributed table at N=many |
| Cold archive | No | S3 endpoint (single SeaweedFS at N=1; SeaweedFS or Garage cluster behind a load balancer at N=many) |

The notarizer one-line change is the only code that materially differs
between N=1 and N=many. It is a single conditional (`if replicas > 1
do leader election else proceed`) that lives behind a configuration
flag. See section 9.

---

## 7. Reader path

MCP audit tools today query Yugabyte directly via
`internal/audit/yugabyte.go` reader paths (not exhaustively traced in
this document but visible at MCP `tack_audit_query` and `tack_audit_get`
per `internal/audit/verbs.go:107`).

### 7.1 N=1 reader path

The MCP audit tools query Yugabyte for chain-verification queries (any
query touching `prev_hash`, `row_hash`, `audit.chain_heads`, or
`audit.notarizations`) and ClickHouse for analytical queries (count by
verb over a time range, top-N actors, faceted search across `delta`
fields).

A simple routing rule lives in `internal/mcp/audit_tools.go` (or
wherever the audit tool handlers live). Default to ClickHouse for
recent windows; fall through to Yugabyte for older windows or for
chain-touching queries.

### 7.2 N=many reader path

Same routing rule. The ClickHouse query goes to the `audit.events_olap`
Distributed table (which fans out to all shards). The Yugabyte query
goes to the cluster endpoint.

The reader does not federate across multiple separate stores. There is
one ClickHouse cluster (one logical store, distributed under the hood)
and one Yugabyte cluster (same).

### 7.3 What changes for the reader

No code change between N=1 and N=many. Configuration values for
`CLICKHOUSE_DSN` and `AUDIT_READER_DSN` differ.

---

## 8. Hash chain integrity at scale

Per `audit_scale_architectures.md` section 4: the universal pattern is
per-shard chains plus periodic Merkle aggregation across shards plus
optional external attestation. Tack already implements the first two.

### 8.1 Per-shard chains

The `audit.chain_heads` row for `(org_id, shard)` holds the latest
`(last_seq, last_hash)`. Each new event reads the head, computes
`hashRow(last_hash, payload)` per `internal/audit/canonical.go:97`,
inserts the event, and advances the head. This is a serializable
read-modify-write within a single Yugabyte transaction.

At N=1: the read-modify-write is local. Latency is sub-millisecond.

At N=many: the read-modify-write involves cross-replica consensus for
the tablet hosting the `(org_id, shard)` head row. Latency increases
to single-digit milliseconds. Concurrent writes to the same
`(org_id, shard)` are serialized; Yugabyte guarantees this. Concurrent
writes to different shards are parallel.

Throughput per shard at N=many: limited by Yugabyte's serial write
throughput per row, on the order of a few thousand TPS per shard. With
256 shards, aggregate is in the range of a few hundred thousand
events per second. To exceed this requires either (a) increasing shard
count (which has the chain-immutability problem, see section 4.2), or
(b) moving the chain advancement out of Yugabyte to a faster
read-modify-write primitive (FoundationDB transactional ops, or a
Kafka-based per-partition state machine). Option (b) is recorded as
an open question in section 15.

### 8.2 Cross-shard Merkle root, periodic notarization

The notarizer reads all shard heads at a fixed cadence (default 60s),
computes a Merkle root over `(org_id, shard, last_seq, last_hash)`
tuples, signs the root with Ed25519, writes to `audit.notarizations`.

This is implemented today at `internal/audit/notarizer.go:132`. No
change at N=many.

A verifier reconstructs the chain at any N as follows:

1. Pull the latest `audit.notarizations` row.
2. Verify the Ed25519 signature against the published `signing_key`.
3. Walk the `shard_heads` JSON manifest. For each `(org_id, shard,
   last_seq, last_hash)` claimed in the manifest, query
   `audit.chain_heads` and confirm match.
4. For each shard, walk `audit.events` from seq=1 to last_seq, verify
   `prev_hash` matches the previous row's `row_hash`, verify
   `row_hash` matches `hashRow(prev_hash, canonical(payload))`.
5. If all matches, the chain is intact up to the notarization
   timestamp.

The verifier is the same code at any N.

### 8.3 External attestation (optional, not implemented)

The notarizer's signed root could itself be logged into Sigstore Rekor
or into a public CT-style log. This is recorded as an open question in
section 15 because Tack does not need it today. The architectural
hook is the `audit.notarizations` row plus a future
`audit.notarizations_external_proof` row holding a Rekor inclusion
proof.

---

## 9. Notarizer at scale

### 9.1 N=1 behavior

One `audit-notarizer` process runs. No leader election. Holds the
Ed25519 private key on disk. Wakes every 60s (configurable via
`AUDIT_NOTARIZER_PERIOD`). Reads `audit.chain_heads`, computes Merkle
root, signs, writes `audit.notarizations`.

Implemented today at `internal/audit/notarizer.go:90`. No change.

### 9.2 N=many behavior

Multiple notarizer replicas run, one elected as active. Active replica
performs the same logic as N=1. Followers warm.

Leader election uses Yugabyte advisory locks (or a Kafka consumer-group
on a single-partition `audit.notarizer.leader` topic, which is an
alternative considered). The lock is reacquired every cadence; if the
leader fails to reacquire (host crash, network partition), a follower
takes over within one cadence period.

Fencing: every notarization carries the leader's Ed25519 key ID
(already in `signing_key` column). After a leader change, the new
leader uses the same signing key (replicated via secret-management).

### 9.3 Same Ed25519 key versus per-shard keys

Choice: same Ed25519 key. The Merkle root covers all shards; signing
the root with one key gives one verifiable signature per cadence. Per-
shard keys would require signature-aggregation (BLS or threshold
signatures) which is more cryptographically complex with no operational
benefit at this scale.

Key rotation:

1. Generate new key.
2. Notarizer signs both old and new root for one full cadence period
   (overlap window).
3. After overlap, notarizer drops the old key.
4. Verifiers must accept either key for signatures during the overlap
   window. Verifier code reads `signing_key` from the notarization row.

The rotation process is the same at N=1 and N=many.

---

## 10. Backpressure at scale

### 10.1 Where pressure originates

At N=1: producer fsync to Apache Kafka is the slowest step on the
read-class path. State-change writes are gated by Yugabyte commit
latency.

At N=many: producer write to Apache Kafka involves cross-replica acks;
this is the slowest step on the read-class path. State-change writes
are gated by Yugabyte cross-replica consensus.

### 10.2 Where pressure propagates

Producer-side pressure: when the broker cannot keep up, the producer
client (`twmb/franz-go`) buffers internally up to its memory cap, then
either blocks or rejects (`max.block.ms` controls). The producer's
`Record` returns an error to the caller. State-change verbs treat this
as a fatal error (the operation aborts). Read-class verbs treat this
as an error to the caller; the caller logs and continues, not blocking
the upstream HTTP request.

Projector-side pressure: when the projector cannot keep up, broker lag
grows. Lag is bounded by the topic retention. Beyond retention, oldest
events are deleted before the projector reads them. This is the worst
failure mode and is paged via the `audit_kafka_lag_seconds` metric.

### 10.3 Cross-shard backpressure isolation at N=many

Per `audit_scale_architectures.md` section 3.7: cross-tenant
backpressure is a bug at scale. Tack's architecture isolates as
follows:

- **Per-partition isolation at the broker.** Partition A's lag does
  not slow partition B's produce or consume.
- **Per-partition projector workers.** Each projector instance handles
  a subset of partitions; one slow tenant on partition 5 does not
  block partition 6 if they are on different projector instances.
- **Yugabyte tablet-level isolation.** Per-`(org_id, shard)` writes
  go to different tablets at N=many; the tablets are independent
  Raft groups.
- **ClickHouse shard isolation at N=many.** Same.

Cross-shard pressure can only happen if a single projector instance
handles many shards and falls behind on all of them. This is mitigated
by scaling projector instance count proportionally to shard load.
Recommended ratio: at most 64 partitions per projector instance.

### 10.4 Backpressure on shard A affecting shard B at N=many

Direct answer: only if they share a projector instance and the
projector itself is the bottleneck. The architecture does not prevent
this; the operational guidance is "scale projector instances so each
handles a tractable subset of partitions."

If cross-shard isolation is required as a hard guarantee, the next
step is dedicated projector instances per shard (256 instances), which
is operationally heavier but the architecture supports it (Kafka
consumer-group with one consumer per partition, statically assigned).

---

## 11. Failure modes by N

### 11.1 N=1 failure modes

| Failure | Recovery procedure |
|---|---|
| `tack-app` crash | Restart container; producer reconnects to broker |
| Apache Kafka crash | Restart broker (combined `broker,controller` KRaft process); producer reconnects; in-flight events lost from producer buffer if not yet ack'd |
| `audit-projector` crash | Restart container; resume from last committed offset |
| Yugabyte crash | Restart; chain advancement blocks until DB up; producer state-change writes fail in the meantime |
| ClickHouse crash | Restart; OLAP reads fall back to Yugabyte |
| SeaweedFS crash | Restart; archiver writes fail until back |
| CT 117 host loss | Catastrophic. Restore from backup per `audit_scale_research.md` section 8 |

The CT 117 host-loss case is the dominant N=1 risk and is the reason
the `audit_two_phase_plan.md` retro section 1A flagged backups as
broken.

### 11.2 N=many failure modes

| Failure | Recovery procedure |
|---|---|
| Single tack-app host loss | Other hosts continue; broker rebalances |
| Single broker loss | RF=3 keeps topic available; rebalance on broker rejoin |
| Single projector loss | Consumer group rebalances; surviving projectors take over partitions |
| Yugabyte single-node loss | Cluster continues with majority; tablet leader election picks new leaders |
| Network partition | Brokers and DB nodes split; minority partitions go read-only; producers in minority fail-loud |
| Whole-AZ loss (multi-region only) | xCluster failover to surviving region; DNS update |
| Split-brain (broker, DB) | Both products handle this via their consensus layers (Raft for Yugabyte, KRaft for Apache Kafka) |
| Consumer-group rebalance storm | Mitigated by `session.timeout.ms` and `max.poll.interval.ms` tuning |

Notarizer leader-election failure modes:

| Failure | Recovery |
|---|---|
| Leader crashes | Follower takes over within one cadence (60s by default) |
| Lock service unavailable | All notarizers refuse to act until lock service back; notarizations pause but no integrity loss |
| Multiple leaders briefly during election | Both write to `audit.notarizations`; result is an extra (redundant) notarization row, not incorrect data |

### 11.3 Migration-specific failure modes

During the Phase 1 to N=1 migration (section 12), dual-write parity
windows create a third class of failure: parity drift. If the WAL path
and the Kafka path diverge during dual-write, the operator must
investigate. Mitigation: dual-write parity gate at every wave.

---

## 12. Migration path: today to N=1 of this design

The first deploy of this architecture is to a single CT 117 host. This
is the path from today (Phase 1 WAL fix shipped) to N=1 of the new
design.

### 12.1 Wave structure (5 waves)

**Wave 0: Phase 1 stabilizes.**

Phase 1 from `audit_two_phase_plan.md` ships first. Production runs on
the WAL fix for at least 7 days clean. No `audit_wal_backpressure_*`
counters trip. This is the precondition for Wave 1.

**Wave 1: Add Apache Kafka and ClickHouse, shadow-write only.**

1. Add Apache Kafka Compose service. Single broker in combined KRaft
   `broker,controller` role, 256 partitions, RF=1, controller quorum
   size 1. Image: `apache/kafka:4.2.0` (or current stable 4.x).
   Required env: `KAFKA_PROCESS_ROLES=broker,controller`,
   `KAFKA_NODE_ID=1`, `KAFKA_CONTROLLER_QUORUM_VOTERS=1@kafka:9093`,
   `KAFKA_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093`,
   `KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT`,
   `KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER`,
   `KAFKA_LOG_DIRS=/var/lib/kafka/data`. Mount a volume at
   `/var/lib/kafka/data`. The cluster ID is generated once via
   `kafka-storage.sh random-uuid` and pinned in
   `KAFKA_CLUSTER_ID`. No ZooKeeper service.
2. Add ClickHouse Compose service. Single node.
3. Provision the SeaweedFS object store as a dedicated LXC through the configs
   repo (the weed binary under systemd). Single node, S3 API enabled.
4. Apply migrations:
   - `005_audit_consumer_offsets.sql` (new table for offset tracking)
   - `006_audit_events_event_id_uniq.sql` (UNIQUE on `event_id` for
     idempotent reproject)
   - ClickHouse table creation (run via `clickhouse-client`, separate
     from goose migrations)
5. Build `tack-server` with `RecorderRouter` plus a new
   `ShadowKafkaRecorder` that writes to BOTH the existing WAL and the
   new Kafka topic for read-class verbs.
6. Build `audit-projector` binary; deploy as Compose service writing
   to `audit.events_v2` (sibling table, `CREATE TABLE LIKE`) so
   projection is observed in isolation.
7. Build `audit-notarizer` as a separate binary (was an in-process
   goroutine; pull out into its own service for N=many parity).
8. Soak 7 days.

Parity gate: `(event_id, row_hash)` matches between `audit.events`
(WAL path) and `audit.events_v2` (Kafka path) for every event in the
window. Drift greater than zero after 5 minutes means abort.

**Wave 2: Cutover producer to Kafka path only.**

1. Switch `RecorderRouter` to route read-class verbs to
   `KafkaRecorder` only (no shadow). State-change verbs still go to
   `YBRecorder` directly.
2. Switch projector from `audit.events_v2` to `audit.events` (the
   canonical table; UNIQUE on event_id from migration 006 prevents
   duplicates).
3. Stop writing to the WAL.
4. Soak 7 days.

Parity gate: number of read-class events in `audit.events` continues
to climb proportionally to MCP traffic. No silent drops.

**Wave 3: Wire up ClickHouse projection.**

1. Projector starts writing to ClickHouse `audit.events_olap` in
   parallel with Yugabyte.
2. MCP audit query tools start routing recent-window queries to
   ClickHouse with Yugabyte fallback.
3. Soak 7 days.

Parity gate: ClickHouse row count tracks Yugabyte row count within a
small lag window.

**Wave 4: Wire up cold archive.**

1. Add archiver job (Compose-scheduled cron at N=1) that nightly reads
   the oldest week of `audit.events` and writes Iceberg tables to
   SeaweedFS.
2. The archiver is a separate binary `cmd/audit-archiver`.
3. Verify archived data: a verifier tool reads the latest Iceberg
   commit, samples random rows, confirms they match Yugabyte.
4. Soak 7 days.

**Wave 5: Cleanup.**

1. Drop `audit.events_v2` (sibling table from Wave 1).
2. Delete `internal/audit/wal.go` and `wal_test.go`.
3. Remove `AUDIT_WAL_DIR` env var and volume mount.
4. Remove `WALRecorder` from `cmd/server/main.go`.

### 12.2 Rollback gates per wave

| Wave | Rollback action | Recoverability |
|---|---|---|
| 1 | Stop projector; ignore `audit.events_v2`; producer continues WAL-only | Full; WAL path unchanged |
| 2 | Re-enable shadow-write; re-route reader; restart projector to v2 | Full; up to 7 days of dual-write history retained |
| 3 | Stop ClickHouse projection; reader falls back to Yugabyte only | Full; ClickHouse state ignored |
| 4 | Stop archiver; existing Iceberg files orphaned (still queryable directly) | Full |
| 5 | None (irreversible cleanup) | Not applicable; only performed after 7 clean days |

### 12.3 Total wave count

Five waves from Phase 1 stabilization to N=1 of the new architecture.
Each wave is one to two weeks of soak. Total migration window: roughly
six to ten weeks.

---

## 13. Migration path: N=1 to N=many

### 13.1 Configuration changes (no code)

| Component | Configuration change |
|---|---|
| Apache Kafka | Add brokers (KRaft combined or dedicated controller mode); update `controller.quorum.voters` for the new quorum; reconfigure topic with `kafka-configs.sh` to RF=3; run `kafka-reassign-partitions.sh` for partition rebalance |
| Yugabyte | Add nodes; configure xCluster for multi-region |
| ClickHouse | Add nodes; convert tables to `Replicated*` engine; create Distributed table |
| SeaweedFS | Add volume servers; replicate filer; clients keep the same S3 endpoint behind a load balancer |
| `tack-app` | `KAFKA_BROKERS` env var moves from single-host to comma-separated bootstrap-broker list; the franz-go client uses bootstrap brokers to discover the rest of the cluster |
| `audit-projector` | `replicas` count increases |
| `audit-notarizer` | Enable leader-election flag; `replicas` count >= 2 |

### 13.2 Code changes

The only required code change between N=1 and N=many is the notarizer
leader-election preamble, which is a single conditional already
present (per section 9). No producer code change. No consumer code
change. No schema migration.

### 13.3 What prevents "no code change required to scale out"

Two open issues prevent strict zero-code scale-out:

1. **Notarizer leader election.** Section 9 requires a one-line
   conditional. This is technically code. It can be made
   configuration-only by always running the leader-election preamble
   (with no-op behavior at replicas=1). Resolving this to zero code
   change is a small fixup.
2. **ClickHouse table engine name.** `MergeTree` at N=1 versus
   `ReplicatedMergeTree` at N=many is a schema difference, not code.
   The schema migration runs once at the N=1-to-N=many transition.
   This is configuration if "schema migration" is configuration; it
   is code if "schema migration" is code. Treat as configuration with
   a documented migration step.

With those two caveats, the answer is: no code change required, only
configuration and one schema migration.

### 13.4 Migration steps from N=1 to N=2

1. Provision second host. Bring up Apache Kafka broker 2 in KRaft
   combined `broker,controller` mode; add it to
   `controller.quorum.voters` on broker 1 first, restart in a rolling
   manner.
2. Reconfigure topic to RF=3 (requires 3 brokers; bring up broker 3
   first or run RF=2 transitionally). Use `kafka-configs.sh` in alter
   mode to set `min.insync.replicas=2` on the topic, then
   `kafka-reassign-partitions.sh` with a generated reassignment plan.
3. Update `KAFKA_BROKERS` on `tack-app` to the comma-separated
   bootstrap list. franz-go discovers the full cluster from the
   bootstrap list, so adding brokers later is a config-only change.
4. Bring up second `tack-app` instance on host 2.
5. Bring up second `audit-projector` instance on host 2; consumer
   group rebalances.
6. Bring up Yugabyte node 2; cluster expands.
7. Bring up ClickHouse node 2; convert to ReplicatedMergeTree.
8. Bring up SeaweedFS volume server 2 (or, for the Garage alternative,
   bring up the third Garage node to reach the 3-node minimum).

This is operations work; no code deploy required.

---

## 14. Operational requirements at each N

### 14.1 N=1 (today plus this design)

- **Team size.** One operator (the Tack operator), part time.
- **On-call.** Best effort. CT 117 reboot is the recovery primitive.
- **Monitoring.** Existing slog plus new Prometheus metrics for Kafka
  lag, projector commit rate, ClickHouse query latency.
- **Capacity planning.** None required at 0.37 EPS. The architecture
  has 5+ orders of magnitude of headroom.
- **Cost.** Within an order of magnitude of current Tack hosting cost.
  Apache Kafka and ClickHouse processes share the existing host; the
  SeaweedFS object store runs on its own LXC. Kafka's JVM footprint is the dominant memory cost on
  the host; budget roughly 2 GiB heap for a small-scale single-broker
  KRaft process.

### 14.2 N=2 (first horizontal step)

- **Team size.** Adds roughly 0.5 FTE of operational work for the
  streaming and cluster management layer per
  `audit_scale_architectures.md` section 6 (Splunk and Kafka
  customer FTE numbers extrapolated to small scale).
- **On-call.** Now requires a real rotation. A single host can no
  longer be the recovery primitive.
- **Monitoring.** Adds inter-host network metrics, broker quorum
  health, Yugabyte cluster health, ClickHouse replication lag.
- **Capacity planning.** Now matters. Partition reassignment under
  topic alter-config is a planned operation.
- **Cost.** Roughly 2x to 3x N=1 cost (two hosts plus inter-host
  bandwidth).

### 14.3 N=many (multi-region, multi-million EPS)

- **Team size.** 5 to 20 FTE on the streaming and storage tiers per
  `audit_scale_architectures.md` section 6.
- **On-call.** 24/7.
- **Monitoring.** Comprehensive: per-tenant SLIs, per-region SLIs,
  cross-region replication health.
- **Capacity planning.** Continuous.
- **Cost.** Driven by hardware and headcount; per
  `audit_scale_architectures.md` section 6 in the high six to seven
  figures monthly at the 1M EPS endgame.

### 14.4 What N=1 to N=2 specifically adds

- A second host.
- A second Apache Kafka broker (KRaft combined mode at first; dedicated
  controller-only nodes at the next horizontal step).
- A second `tack-app` instance.
- A second `audit-projector` instance.
- Inter-host networking with sufficient bandwidth.
- Backup procedures that account for two hosts.
- An on-call rotation.
- Monitoring of inter-host health.

---

## 15. Open questions and risks

### 15.1 Open questions

- **Address index global vs scoped.** Per the retro at section 1B in
  `incident_2026-05-09_seed_parallel_org/retro_log.md`, the address
  index is currently global. This affects audit query semantics
  (cross-tenant audit reads must respect the address index's
  global/scoped distinction). Resolution required before N=many.
- **Reverse address index.** Same retro section: no reverse index
  exists today. Affects audit forensics that need to find all
  references to a node.
- **Per-shard chain immutability vs growing partition count.** Per
  section 4.2: shard count is fixed at 256 forever, which caps
  per-shard throughput. If a tenant ever needs more than 1k to 4k
  EPS sustained on a single shard, the architecture needs an answer.
- **Notarizer leader-election primitive.** Yugabyte advisory locks
  versus FoundationDB CAS versus a Kafka single-partition lease.
  Decision punted; all three are workable.
- **External attestation (Sigstore Rekor).** Not implemented; whether
  Tack needs it depends on customer compliance demands.
- **Schema evolution for `Event`.** Producer and consumer share the
  Go struct today. If consumer ships first with a new field,
  encoding/json default behavior is fine. If producer ships first
  removing a field, the consumer reads zero-value. A `schema_version`
  field on `Event` is a small fixup.
- **Cross-shard isolation at N=many.** Section 10.4: not strictly
  guaranteed by architecture; relies on operational sizing of
  projector instances. Hard isolation requires per-shard projector
  instances.
- **PII bulk redaction at N=many.** Single-row `audit.pii` redaction
  works at any N. Bulk redaction (GDPR right-to-erasure for an entire
  user's history) is a batch job; the batch primitive at N=many is
  not specified.

### 15.2 Risks not solved by this design

- **Consumer offset corruption.** If `audit.consumer_offsets` is
  somehow advanced past unprocessed events, the projector skips
  them. Mitigated by transactional offset commit (`audit.events`
  INSERT and offset UPDATE in one Yugabyte transaction). Hard
  failure: corruption of the offsets table by a manual operator
  action. No protection.
- **Broker death without backup.** N=1 single-broker loss with the
  topic disk gone is bounded loss in flight. RF=1 is a known
  compromise. Mitigated only at N=many.
- **Schema drift between producer and consumer.** Same as
  Phase 2 risk in `audit_two_phase_plan.md` section 5b. Standard
  JSON-schema-evolution risk.
- **ClickHouse and Yugabyte divergence.** ClickHouse is the
  read-optimized copy; Yugabyte is canonical. If they diverge
  (projector writes to one but not the other), the OLAP layer can
  silently lie. Mitigated by parity check; the parity check runs
  daily in production at N=many.
- **Backup correctness.** Per `incident_2026-05-09_seed_parallel_org/retro_log.md`
  section 1A: prior backups were silently empty. The same class of
  defect can recur with the broker, ClickHouse, or SeaweedFS. Each
  backup must
  have a restore test.

---

## 16. Critical files

### 16.1 Files created

- `/Users/agoodkind/Sites/tack/internal/audit/router.go`
  RecorderRouter dispatch on IsStateChange.
- `/Users/agoodkind/Sites/tack/internal/audit/kafka_recorder.go`
  KafkaRecorder using franz-go.
- `/Users/agoodkind/Sites/tack/internal/audit/kafka_recorder_test.go`
  Unit tests with embedded broker (or fake).
- `/Users/agoodkind/Sites/tack/internal/audit/shadow_recorder.go`
  ShadowKafkaRecorder for Wave 1 dual-write.
- `/Users/agoodkind/Sites/tack/internal/audit/projector.go`
  Library that the `audit-projector` binary uses; also testable in
  isolation.
- `/Users/agoodkind/Sites/tack/internal/audit/projector_test.go`
- `/Users/agoodkind/Sites/tack/internal/audit/clickhouse_writer.go`
  ClickHouse write path used by projector.
- `/Users/agoodkind/Sites/tack/internal/audit/clickhouse_reader.go`
  ClickHouse read path used by MCP audit tools.
- `/Users/agoodkind/Sites/tack/cmd/audit-projector/main.go`
- `/Users/agoodkind/Sites/tack/cmd/audit-notarizer/main.go`
  Pulled out from in-process goroutine.
- `/Users/agoodkind/Sites/tack/cmd/audit-archiver/main.go`
- `/Users/agoodkind/Sites/tack/internal/audit/leader_election.go`
  Yugabyte advisory lock leader election for notarizer.
- `/Users/agoodkind/Sites/tack/migrations/005_audit_consumer_offsets.sql`
- `/Users/agoodkind/Sites/tack/migrations/006_audit_events_event_id_uniq.sql`
- `/Users/agoodkind/Sites/tack/migrations/007_audit_events_v2_sibling.sql`
  Wave 1 only.
- `/Users/agoodkind/Sites/tack/migrations/008_audit_events_v2_drop.sql`
  Wave 5 cleanup.
- `/Users/agoodkind/Sites/tack/clickhouse_schema/001_audit_events_olap.sql`
- `/Users/agoodkind/Sites/tack/scripts/audit-parity-check.sh`
- `/Users/agoodkind/Sites/tack/scripts/audit-backup-with-restore-test.sh`

### 16.2 Files modified

- `/Users/agoodkind/Sites/tack/cmd/server/main.go`
  Wire RecorderRouter; remove WAL in Wave 5.
- `/Users/agoodkind/Sites/tack/internal/config/config.go`
  Add KAFKA_BROKERS (bootstrap list), KAFKA_TOPIC, KAFKA_CLUSTER_ID,
  CLICKHOUSE_DSN, S3_ENDPOINT (SeaweedFS or Garage), S3_ACCESS_KEY,
  S3_SECRET_KEY, AUDIT_NOTARIZER_LEADER_ELECTION env vars.
- `/Users/agoodkind/Sites/tack/internal/audit/notarizer.go`
  Add optional leader-election preamble.
- `/Users/agoodkind/Sites/tack/internal/audit/yugabyte.go`
  No change (preserved for state-change writes).
- `/Users/agoodkind/Sites/tack/docker-compose.yml`
  Add `kafka` (Apache Kafka 4.x in KRaft combined mode),
  `audit-projector`, `audit-notarizer`, `audit-archiver`, and `clickhouse`
  services. The SeaweedFS object store is a dedicated LXC provisioned by the
  configs repo, not a Compose service.
- `/Users/agoodkind/Sites/tack/internal/telemetry/metrics.go`
  Add `audit_kafka_lag_seconds`, `audit_projector_commit_rate`,
  `audit_clickhouse_write_latency_ms`, `audit_archiver_lag_hours`.
- `/Users/agoodkind/Sites/tack/scripts/backup.sh`
  Add Apache Kafka log-dir backups, ClickHouse backups, and SeaweedFS
  volume backups, each with restore-test gates.
- `/Users/agoodkind/Sites/tack/CLAUDE.md`
  Document the architecture, the Apache Kafka topic, the projector,
  the ClickHouse OLAP tier, and the SeaweedFS-backed cold archive.

### 16.3 Files deleted (Wave 5)

- `/Users/agoodkind/Sites/tack/internal/audit/wal.go`
- `/Users/agoodkind/Sites/tack/internal/audit/wal_test.go`
- `/var/lib/tack/audit-wal/` on CT 117 (operator action).
