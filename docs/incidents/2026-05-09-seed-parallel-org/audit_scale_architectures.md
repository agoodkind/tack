# Audit Scale Architectures: How Real Systems Run at Multi-Million EPS

Prepared 2026-05-09 as the input for the Tack horizontal audit design at
`/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/audit_horizontal_design.md`.
This document is concerned with the architectural endgame at multi-million
sustained events per second (EPS), not with what Tack's current traffic looks
like. The companion document at `audit_scale_research.md` already established
that Tack's actual scale is roughly 0.37 EPS sustained today; that finding is
not revisited here. The question here is: when somebody really does run at one
to ten million sustained EPS, what does the architecture look like, and which
parts of that architecture are public, which parts are inferred, and which
parts are "no public source found."

Throughout, three throughput quantities are distinguished:

- **Marketing throughput.** The headline a vendor or engineering blog leads
  with. Often a peak demo, often a synthetic benchmark, sometimes a single
  isolated record-shape that the system cannot hold.
- **Achievable throughput.** What an external observer can reproduce on
  similar hardware following the published recipe. Frequently lower than
  marketing by a factor of 2 to 10.
- **Sustained throughput.** What a real customer or first-party team holds
  steady for weeks without ops drama. Frequently lower than achievable
  throughput by another factor of 2 to 5.

The third number is the only one that should drive an architecture decision.

---

## 1. Executive summary

- The architectural pattern at multi-million EPS is consistent across every
  system the public record covers: a Kafka-class persistent log on the write
  path, a columnar OLAP store (ClickHouse, Druid, or proprietary equivalent)
  on the read path, with a coarse partition-by-tenant or partition-by-hash
  topology and tiered cold storage to object storage for retention.
  Producer-side batching with idempotent semantics is universal. Per-record
  synchronous transactional commits are absent at this tier.
- Hash-chained, signed, append-only audit ledgers in the compliance sense
  (GDPR redaction, tamper evidence, court-admissible) appear to top out at
  the low thousands of writes per second in published architectures. The
  highest-throughput system that meets all three properties (hash chain plus
  signing plus tamper evidence) appears to be Sigstore Rekor at roughly 1,500
  to 3,000 writes per second sustained per shard. No published architecture
  delivers court-grade compliance audit at over one million sustained EPS.
- The systems that DO run at multi-million EPS (LinkedIn, Cloudflare,
  Datadog, Splunk, Confluent customers in the "trillion messages per day"
  tier) explicitly relax some compliance property to get there. The most
  common relaxation is "we hash chain at the partition level, sign the chain
  heads periodically, and accept that gap windows of one to fifteen minutes
  are uncovered by signature." Sigstore's Rekor is a counterexample because
  every entry is included in a signed Merkle inclusion proof, but Rekor pays
  for that with throughput, not the other way around.
- Storage tier separation is universal at scale. Hot-tier indexed query
  storage (ClickHouse, Snowflake, BigQuery, Druid, Splunk SmartStore) is
  separate from cold-tier object archive (S3, GCS, Iceberg, Delta Lake).
  No system the research turned up runs row-store transactional databases
  (Postgres, MySQL, Yugabyte) past about 100k EPS sustained. The transition
  from row-store to columnar is around 10k to 100k EPS depending on shape.
- Producer-side batching is a hard requirement above about 50k EPS.
  Per-event synchronous TCP plus disk fsync round-trips do not scale past
  that; every system at the multi-million tier uses producer-side batching
  with batch sizes in the 1k to 100k records range and per-batch fsync at
  the broker.
- Backpressure in published architectures lives at exactly two layers:
  producer-side, where the broker rejects appends or the producer drops
  to a side queue, and at the projection consumer, where lag is allowed to
  grow inside the broker's retention window. Backpressure across tenants
  (one tenant's burst slows another's audit) appears to be considered a
  bug, not a feature, and is engineered against with per-tenant partitions
  and per-tenant rate limits.
- The operational shape of multi-million-EPS audit is roughly five to
  twenty engineers full time on the streaming layer alone, plus a separate
  data-platform team for the OLAP layer, plus a separate compliance team
  that handles signing, attestation, and legal evidentiary chain. This is
  not a one-operator deployment shape and cannot be made one without
  managed services (Confluent Cloud, Snowflake, BigQuery).
- The most surprising finding from the research: the highest-throughput
  cryptographically-attested append-only log in published architecture
  is not a compliance audit log. It is Certificate Transparency
  (Let's Encrypt's Argon, Cloudflare's Nimbus, Google's Argon) which sustains
  roughly 30 to 100 inclusions per second per log shard with multi-shard
  fan-out. CT logs hash chain every entry, sign tree heads every minute,
  expose Merkle inclusion proofs to every observer, and are externally
  audited. They do this at three orders of magnitude below "multi-million
  EPS." The conclusion: court-grade compliance audit at multi-million EPS
  is not a published architecture.

---

## 2. Real systems at the multi-million tier

The pattern below is: per system, a one-paragraph architectural sketch, the
strongest published throughput number, and the marketing-versus-sustained
distinction.

### 2.1 LinkedIn Kafka (the canonical reference)

- **Architecture.** Apache Kafka clusters running as the "central nervous
  system" for the entire company. Approximately 100+ clusters, 4,000+
  brokers, 7+ million partitions across the fleet. Producers write to
  topic partitions; consumers read in groups; offsets are committed back
  to internal Kafka topics. Per-topic replication factor of typically 3.
  Schema registry (Confluent-compatible) for type evolution. Mirror Maker 2
  for cross-DC replication.
- **Marketing throughput.** "7 trillion messages per day" per the
  [LinkedIn Engineering blog Apache Kafka post](https://www.linkedin.com/blog/engineering/open-source/apache-kafka-trillion-messages).
  7 trillion per day is roughly 81 million messages per second sustained
  averaged over 24 hours.
- **Achievable throughput, single broker.** 2,024,032 records per second
  with 100-byte records, three concurrent producer processes on separate
  hosts, three-way async replication, on Intel Xeon 2.5 GHz, six cores,
  32 GB RAM, 6x 7200 RPM SATA, 1 GbE. Per the
  [LinkedIn 2-million-writes benchmark](https://engineering.linkedin.com/kafka/benchmarking-apache-kafka-2-million-writes-second-three-cheap-machines).
- **Sustained throughput.** Not directly published per cluster, but the
  fleet-wide 81 million per second divided by 100 clusters is roughly
  810k per second per cluster, divided by 40 brokers per cluster (4000 / 100)
  is roughly 20k per second per broker. That is two orders of magnitude
  below the achievable benchmark, which is the typical marketing-versus-
  sustained ratio.
- **Operational shape.** LinkedIn has a dedicated Kafka SRE team measured
  in dozens of engineers per the public talks (KafkaSummit). Plus a
  separate data infrastructure team for downstream consumers.
- **Notes for compliance.** LinkedIn does not publicly position Kafka as
  the compliance audit ledger. Compliance audit at LinkedIn appears to be
  a separate downstream consumer that projects from Kafka into a separate
  durable store; the public posts do not detail the chain integrity story.

### 2.2 Cloudflare logging pipeline

- **Architecture.** Cloudflare runs three relevant pipelines in public:
  the HTTP edge log pipeline (HTTP request logs), the analytics pipeline
  (aggregated metrics), and the inter-service Kafka bus. The HTTP edge
  pipeline ingests into Kafka topics partitioned by zone, fans out to
  ClickHouse for analytics and to S3-compatible object storage for
  long-term archival. Per [the Cloudflare logging pipeline overview](https://blog.cloudflare.com/an-overview-of-cloudflares-logging-pipeline/).
- **Marketing throughput.** "1 trillion Kafka messages per day" per
  [the Cloudflare 1-trillion post](https://blog.cloudflare.com/using-apache-kafka-to-process-1-trillion-messages/),
  which is roughly 11.5 million messages per second sustained across all
  pipelines combined. "6 million HTTP requests per second" served by
  ClickHouse for analytics per [the Cloudflare HTTP analytics post](https://blog.cloudflare.com/http-analytics-for-6m-requests-per-second-using-clickhouse/).
- **Achievable throughput.** Cloudflare publishes aggregate fleet numbers,
  not per-host benchmarks. The 6M-RPS ClickHouse number is a sustained
  production read+write rate, not a synthetic peak.
- **Sustained throughput.** The 1 trillion per day is the bus, not one
  topic. Per-topic numbers are not in the public posts but the architecture
  is clearly per-topic capped well below the bus number, with fan-out across
  many topics.
- **Operational shape.** Cloudflare's data team is dozens of engineers per
  the public posts. The systems-engineering blog cites a "data infrastructure"
  team distinct from the SRE team distinct from the analytics team.
- **Notes for compliance.** Cloudflare's own audit log for customer dashboard
  actions is a separate product, not the HTTP edge log. The public docs at
  [audit logs (legacy)](https://developers.cloudflare.com/fundamentals/setup/account/account-security/review-audit-logs/)
  describe a per-account log with REST query, not per-second throughput
  numbers. This is consistent with the rest of the industry: at Cloudflare's
  scale they do not run dashboard-actions audit at edge-log scale.

### 2.3 Uber M3 (metrics, not strictly audit)

- **Architecture.** M3DB, a custom time-series store, fronted by M3
  Coordinator. Producers write metrics through an aggregator tier; the
  aggregator deduplicates and downsamples; the storage tier persists in
  M3DB with multi-level retention. Per
  [the Uber M3 introduction](https://eng.uber.com/m3/).
- **Marketing throughput.** "9 billion unique time series" with "tens of
  millions of writes per second" per the original Uber Engineering post.
- **Sustained throughput.** Multi-million writes per second sustained
  across the fleet. Per-host throughput is in the hundreds of thousands of
  metrics per second range.
- **Operational shape.** Uber's observability team is dozens of engineers
  per public talks.
- **Notes for compliance.** M3 is metrics, not compliance audit. There
  is no hash chain, no signing, no PII isolation. This system is included
  here to establish that even Uber's at-scale time-series infrastructure
  does not aspire to court-admissible audit semantics.

### 2.4 Stripe ledger

- **Architecture.** No public detailed architectural disclosure of the
  Stripe internal ledger. Stripe has published the
  [Stripe ledger blog post](https://stripe.com/blog/online-migrations) on
  a different topic (online migrations) and the
  [Building a 99.999% reliable payments API](https://stripe.com/blog/api-availability)
  post but neither describes the audit ledger throughput. The
  [TigerBeetle blog](https://tigerbeetle.com/blog/) frequently references
  Stripe-style ledgers as the design target but does not have access to
  Stripe's actual internals.
- **Marketing throughput.** No public number found. Search query "Stripe
  ledger throughput events per second" returned no first-party Stripe
  documentation.
- **Sustained throughput.** No public source found.
- **Notes.** Financial transaction ledgers at Stripe scale are a
  fundamentally different design problem from compliance audit logs. The
  former requires double-entry semantics, idempotency, and balance
  consistency; the latter requires append-only durability and
  tamper-evidence. Stripe's published material is on the former, not the
  latter.

### 2.5 Sigstore Rekor (the closest published compliance-grade analog)

- **Architecture.** Rekor is an append-only transparency log for software
  supply chain artifacts (signatures, attestations, build provenance). Each
  entry is hashed into a Merkle tree; tree heads are signed and published;
  inclusion proofs are publicly verifiable. The backing storage is
  Trillian, a Google-developed verifiable log. Per
  [the Sigstore docs](https://docs.sigstore.dev/logging/overview/).
- **Marketing throughput.** Rekor's published roadmap at
  [sigstore.dev](https://www.sigstore.dev/) does not headline a per-second
  rate. Trillian itself is documented at
  [the Trillian project](https://github.com/google/trillian) as supporting
  "billions of entries per log."
- **Sustained throughput.** Per the Sigstore community discussions and
  [Rekor scale analysis blog posts](https://blog.sigstore.dev/), Rekor
  Public Good Instance peaks around 2,000 to 3,000 entries per second
  sustained. The shard architecture is documented at
  [the Sigstore architecture docs](https://docs.sigstore.dev/logging/sharding/);
  shards are added when the active shard hits its capacity ceiling, and
  inclusion proofs from old shards remain valid because the shard root
  is itself signed.
- **Achievable throughput.** Trillian benchmarks at higher rates in
  isolation but Rekor's wrapping (signing, public visibility, multi-tenant)
  brings effective sustained throughput to the low thousands per second
  per shard. Adding shards is the scale-out path.
- **Operational shape.** Sigstore is operated as a public good by a
  consortium (Linux Foundation, Google, Red Hat, Chainguard, others). Per
  the public roadmap, this is on the order of a small handful of dedicated
  engineers across the consortium, augmented by community contribution.
- **Notes for compliance.** Rekor is the published architecture closest to
  Tack's compliance contract. Hash chain, signing, public verifiability,
  PII isolation by design (Rekor signatures point at hashes of artifacts,
  not at PII payloads). Its sustained throughput is in the low thousands
  per second per shard. Sustained multi-million-per-second compliance audit
  with Rekor-grade properties is not in the public record.

### 2.6 Datadog logs ingestion

- **Architecture.** Multi-tenant SaaS. Customer agents stream logs over
  HTTPS to Datadog edge ingest; ingest writes to internal Kafka; consumers
  project to indexing tier (proprietary, ClickHouse-style columnar) and to
  cold archive. "Logs Without Limits" decouples ingest from indexing so
  customers can ingest more than they index. Per
  [the Datadog logs guide](https://docs.datadoghq.com/logs/guide/getting-started-lwl/).
- **Marketing throughput.** Datadog does not publish a per-customer EPS
  ceiling but the [Datadog rate limits page](https://docs.datadoghq.com/api/latest/rate-limits/)
  caps the logs intake API at high tens of thousands of requests per minute
  per organization, depending on tier.
- **Sustained throughput.** Aggregate Datadog ingest is in the tens of
  millions of events per second range across the entire customer fleet
  per public Datadog earnings call mentions; per-customer is far below.
- **Operational shape.** Datadog has thousands of employees with hundreds
  on infrastructure. Not a one-operator shape.
- **Notes for compliance.** Datadog's audit log product is a separate SKU
  from log ingestion. Per
  [the Datadog audit trail docs](https://docs.datadoghq.com/account_management/audit_trail/),
  audit trail covers Datadog account actions, not customer audit. Datadog
  does not position itself as a compliance audit destination at the
  multi-million-EPS tier.

### 2.7 Splunk

- **Architecture.** Indexer cluster with HTTP Event Collector (HEC) ingest,
  search heads for query, indexers for storage, and "SmartStore" for
  S3-backed cold storage. Per
  [the Splunk indexer cluster docs](https://docs.splunk.com/Documentation/Splunk/latest/Indexer/Aboutindexesandindexers).
- **Marketing throughput.** Splunk benchmarks vary by source. Per the
  [Splunk community thread](https://community.splunk.com/t5/Getting-Data-In/How-many-events-per-second-a-heavy-forwarder-can-ingest-with-the/m-p/433116),
  one indexer with 24 to 48 cores and 64 to 128 GB RAM sustains 55,000 to
  58,000 EPS via HEC.
- **Sustained throughput.** Real customer deployments at the high end run
  hundreds of indexers; aggregate fleet throughput at the largest customers
  is reported in low millions of EPS per
  [Splunk's reference architectures](https://www.splunk.com/en_us/pdfs/tech-brief/splunk-validated-architectures.pdf).
- **Operational shape.** Customers running Splunk at multi-million EPS
  typically have a dedicated platform team of five plus engineers per
  the validated architecture guides.
- **Notes for compliance.** Splunk is the dominant SIEM and the dominant
  enterprise compliance audit destination. Splunk does not provide built-in
  cryptographic chain-of-custody by default; chain-of-custody is achieved
  by integrity hashing on ingest plus operational controls on the indexers.
  This is "audit at scale, with operational tamper-evidence" rather than
  "audit at scale, with cryptographic tamper-evidence."

### 2.8 ClickHouse-based audit systems

- **Architecture.** ClickHouse is the open-source columnar OLAP store at
  the heart of many at-scale audit systems. The pattern: producer writes
  to Kafka or directly to ClickHouse via the Kafka table engine or an
  HTTP-buffered ingest service; ClickHouse stores columnar with MergeTree
  partitions; queries run analytical SQL across billions of rows. Per
  [the ClickHouse architecture overview](https://clickhouse.com/docs/en/development/architecture).
- **Marketing throughput.** ClickHouse publishes 1 million inserts per
  second per server in the
  [ClickHouse performance docs](https://clickhouse.com/docs/en/operations/performance).
  Cloudflare's HTTP analytics blog post reports 11M+ rows/second sustained
  across the cluster.
- **Sustained throughput.** Real-world ClickHouse deployments at scale
  (Cloudflare, Yandex, Uber) sustain hundreds of thousands of inserts per
  second per shard with cluster aggregates in the millions. Per
  [the Yandex ClickHouse case study](https://clickhouse.com/customer-stories/yandex).
- **Operational shape.** ClickHouse is operationally lighter than Splunk
  per row but heavier than Postgres. Customers running ClickHouse at the
  multi-million-row-per-second tier typically have a dedicated DBA or
  data-platform engineer.
- **Notes for compliance.** ClickHouse alone is not an audit ledger. The
  pattern at scale is to put ClickHouse downstream of a Kafka log that
  carries the compliance contract (hash chain, signing) and use ClickHouse
  for query. ClickHouse is the projection tier, not the integrity tier.

### 2.9 AWS CloudTrail

- **Architecture.** AWS-internal service that captures every AWS API call
  per account. Events are batched into log files written to S3 every ~5
  minutes per
  [the CloudTrail events docs](https://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-events.html).
  Optional CloudTrail Lake provides queryable archive. CloudTrail Insights
  provides anomaly detection.
- **Marketing throughput.** AWS does not publish a CloudTrail per-second
  throughput number. The [CloudTrail quotas page](https://docs.aws.amazon.com/awscloudtrail/latest/userguide/WhatIsCloudTrail-Limits.html)
  lists per-account limits (5 trails per region, 100k events per second
  per region for management events) but not aggregate.
- **Sustained throughput.** Per the AWS quotas, the per-region per-account
  ceiling is 100k EPS for management events. AWS is plausibly running
  CloudTrail at multi-million EPS aggregate across all accounts and regions
  but does not publish that number.
- **Operational shape.** Internal AWS service, dedicated team, cost
  invisible to customers.
- **Notes for compliance.** CloudTrail is the dominant compliance audit
  log for AWS workloads. CloudTrail does not publish a hash chain or
  cryptographic notarization scheme. The compliance contract is "AWS
  guarantees the integrity of CloudTrail by operational controls," which
  is a different kind of contract from Tack's hash-chained signed Merkle
  notarization.

### 2.10 GitHub audit log

- **Architecture.** GitHub's audit log API plus streaming export. Per the
  [GitHub audit log streaming docs](https://docs.github.com/en/enterprise-cloud@latest/admin/monitoring-activity-in-your-enterprise/reviewing-audit-logs-for-your-enterprise/streaming-the-audit-log-for-your-enterprise),
  customers stream events over HTTPS or via Azure Event Hubs.
- **Marketing throughput.** Per-customer streaming ceiling is documented at
  200 events per second baseline plus 100 to 200 EPS for API events, total
  300 to 400 EPS sustained. Each payload must be processed in 500 ms.
- **Sustained throughput.** The per-customer ceiling is the documented
  number. Aggregate GitHub-wide is not published.
- **Operational shape.** GitHub-internal service.
- **Notes for compliance.** GitHub's compliance contract (SOC 2, ISO 27001)
  is met by operational controls plus an internally-protected audit store.
  No public hash chain or cryptographic notarization.

### 2.11 Honeycomb

- **Architecture.** Stateless ingest workers ("shepherd") write to Kafka;
  stateful columnar storage ("retriever") consumes and serves queries. Per
  [the Honeycomb scaling Kafka post](https://www.honeycomb.io/blog/scaling-kafka-observability-pipelines).
- **Marketing throughput.** "Trillions of events per year" per the
  [Honeycomb platform page](https://www.honeycomb.io/platform).
- **Sustained throughput.** Trillions per year is roughly 30k to 100k EPS
  sustained.
- **Operational shape.** Mid-size SaaS company; observability platform
  team is documented in public talks at roughly a dozen engineers.
- **Notes for compliance.** Honeycomb is observability, not audit. No
  cryptographic chain-of-custody.

---

## 3. Architectural patterns at scale

This section is the toolkit. Each pattern: what it does, when it applies,
what it costs.

### 3.1 Producer-side batching

- **What it does.** The producer accumulates records in memory (typical
  batch size 1k to 100k) and flushes either when the batch fills or when a
  linger timer fires (typical 5 ms to 100 ms). The broker accepts the
  whole batch in one network round trip.
- **When it applies.** Always above 50k EPS per producer. Below that,
  per-record produce works. Above that, the network round trip and disk
  fsync per record dominate.
- **What it costs.** Up to one linger period of latency added to every
  audit event. Memory usage scales with batch size. Crash loses the
  unflushed batch unless the producer also writes to a local WAL first.
- **Real-world example.** Kafka's `linger.ms` and `batch.size` per
  [the Kafka producer docs](https://kafka.apache.org/documentation/#producerconfigs).
  Default 0 ms linger; production high-throughput deployments tune to
  10 to 50 ms.

### 3.2 Idempotent producer (exactly-once semantics)

- **What it does.** The producer assigns a sequence number to each record;
  the broker dedupes on (producer ID, sequence) so a retried produce after
  network blip does not double-write. Combined with transactional commit,
  this gives exactly-once semantics across producer retries.
- **When it applies.** Compliance audit always wants this on. The
  alternative is "audit may have duplicates after a network blip" and the
  hash chain has to deduplicate at projection.
- **What it costs.** Slight throughput cost (Kafka measures it at single
  digit percent in the
  [exactly-once design doc](https://kafka.apache.org/documentation/#semantics)).
  Producer state must be carried across restarts.

### 3.3 Regional pre-aggregators

- **What it does.** Producers write to a regional gateway service that
  batches and forwards. The gateway absorbs producer-side bursts, dedupes,
  enriches, and writes to the central broker. Common in multi-region
  ingestion (Datadog, Cloudflare, AWS).
- **When it applies.** Above one million EPS where producers are
  geographically distributed and the central broker latency from far
  producers exceeds the producer's ingest budget.
- **What it costs.** One more service tier to operate. One more failure
  domain. Tack's single-host single-region reality does not need this
  today, but a future multi-region Tack would.

### 3.4 Broker tier sharding

- **Partition by tenant.** Each tenant gets one or more dedicated
  partitions. Pros: per-tenant isolation, per-tenant rate limits work.
  Cons: tenant count drives partition count; over a few thousand
  partitions per cluster the broker controller is strained. Mitigation:
  partition tenants into N groups and partition each group internally.
  Real-world: LinkedIn's 7M partitions across 100 clusters.
- **Partition by event type.** All `node.create` to one set of partitions,
  all `auth.*` to another. Pros: per-type backpressure isolation, per-type
  retention policy. Cons: one chatty type can monopolize its partitions
  while another type is idle. Less common at scale.
- **Partition by hash of actor.** Cloudflare-style. Hash actor ID modulo
  partition count. Pros: load-balances even with skewed tenants. Cons:
  partition count is fixed at table creation (Kafka, Redpanda) and
  rebalancing requires a topic clone. Mitigation: pick a generous
  partition count up front (256 or 1024 is common) and live with under
  utilization at low N.
- **Partition by hash of (actor, event_id).** Tack's current approach
  (`internal/audit/canonical.go:87` shardOf). Combines actor distribution
  with event-id distribution so a single high-traffic actor still spreads.
  This is the right choice for Tack and matches the published Kafka
  pattern.

### 3.5 Storage tier separation: hot vs cold

- **Hot.** Recent N days of events, indexed for query, kept on local
  NVMe or attached block storage. Typical N: 7 to 90 days. Storage tier:
  ClickHouse, Druid, Splunk indexers, BigQuery (when used as hot), or
  Postgres / Yugabyte for moderate scale.
- **Cold.** Older than N days. Stored in object storage (S3, GCS, Azure
  Blob) typically as Parquet or as the hot store's native format. Queryable
  via Iceberg / Delta Lake / Snowflake external tables. Significantly
  cheaper per byte; significantly slower to query.
- **Transition mechanism.** A scheduled job (typically nightly) reads the
  oldest hot partition, writes it to cold, and either drops the hot
  partition or marks it tombstoned. Splunk SmartStore handles this
  automatically; ClickHouse via storage policies; Postgres via partition
  detach.
- **Cost. **Hot storage at multi-million EPS is expensive (NVMe at petabyte
  scale). Cold archive is roughly 30x cheaper per byte. Without
  hot-cold separation a multi-million-EPS audit system bankrupts on
  storage cost in the first quarter.

### 3.6 Streaming projection

- **What it does.** Consumers subscribe to the broker, project records to
  the hot OLAP store, and update materialized views and counters. Per-domain
  consumers each project a subset of fields they care about; one event in
  Kafka can fan out to N consumers.
- **When it applies.** Whenever the OLAP store cannot accept the producer's
  format directly. Always, in practice, because the hash chain has to be
  computed somewhere and that somewhere is the projector.
- **Cost.** One more service tier. Lag must be observable; lag must not
  silently break compliance.

### 3.7 Backpressure topology

- **Where it lives at scale.** Producer-side. The broker rejects (or
  client-library blocks) when the broker cannot keep up. The producer
  either drops to a local WAL, returns an error to the caller, or
  throttles upstream. Kafka's
  [producer configuration](https://kafka.apache.org/documentation/#producerconfigs)
  documents `max.block.ms` and `delivery.timeout.ms` for this.
- **Where it cannot live.** Across tenants. If tenant A's burst slows
  tenant B's audit, multi-tenancy is broken at the audit layer. The
  industry pattern is: per-tenant partition + per-tenant broker quota
  (Kafka client quotas) so tenant A's burst hits its own ceiling first.
- **Where it must not live.** State-change verbs. If a state-change verb's
  audit cannot be persisted, the operation must abort. The compliance
  contract requires it. Tack already encodes this at
  `internal/audit/wal.go:239` (state-change bypasses the WAL).

### 3.8 Multi-region replication

- **Pattern at scale.** Active-active across regions, with per-region
  primaries and Mirror Maker 2 (Kafka) or equivalent (Confluent Cluster
  Linking, Redpanda Cluster Linking) replicating across regions. Per
  [the MM2 docs](https://docs.confluent.io/platform/current/multi-dc-deployments/multi-region.html).
- **Compliance subtlety.** The hash chain and notarization must work
  across regions. Two patterns: (1) one canonical region for the chain,
  others read-replicas of audit; (2) per-region chains with cross-region
  Merkle aggregation in a notarizer that has visibility into all regions.
  Pattern 2 is more available; pattern 1 is simpler.
- **Cost.** Network egress cost is real at multi-million EPS. Multi-region
  audit is typically the most expensive line item in the architecture.

---

## 4. Compliance integrity at scale

This section is the heart of the question: how do real systems maintain
hash chain plus signing plus PII separation when throughput exceeds 100k
EPS?

### 4.1 Per-shard hash chains (universal pattern)

Every system that hash-chains at scale does so per shard, not globally.
A global chain forces every appender to read the head and write a new
head, which serializes all writes. Per-shard chains parallelize the writes
across N shards and accept that "the chain" is actually N independent
chains.

- **Tack today.** Per `(org_id, shard)` with shard in 0 to 255 per
  `internal/audit/canonical.go:87`. This is the standard pattern.
- **Sigstore Rekor.** Per shard, each shard a separate Trillian log. New
  shards are created when the active shard hits a configured ceiling. Per
  [the Rekor sharding docs](https://docs.sigstore.dev/logging/sharding/).
- **Certificate Transparency.** Per shard, each CT log a separate
  Trillian log. Multiple CT logs run by different operators provide
  redundancy. Per
  [the CT documentation](https://certificate.transparency.dev/).
- **CockroachDB ledger references.** Cockroach Labs has discussed
  per-range hash chains in their internal audit at
  [the Cockroach audit docs](https://www.cockroachlabs.com/docs/stable/sql-audit-logging.html).

### 4.2 Periodic cross-shard Merkle aggregation

- **Pattern.** A notarizer reads all shard heads at a fixed cadence (once
  per minute is typical), computes a Merkle root over the heads, signs the
  root, publishes it. Anyone holding the root can verify any chain head
  was included at notarization time, and any row included in any shard
  before that time is transitively covered.
- **Tack today.** Implemented at `internal/audit/notarizer.go:132`. The
  cadence is configurable (default 60s).
- **Sigstore Rekor.** Tree heads are signed every minute and published to
  the public log per the Sigstore design docs. The same pattern.
- **Throughput cost.** The notarizer reads N shard heads. At N = 256
  this is 256 row reads per cadence. Trivial cost; the notarizer is not
  on the throughput path.

### 4.3 External attestation

- **Pattern.** The notarizer's signed roots are themselves logged into
  an external transparency log (Sigstore, a public CT log, an internal
  HSM-backed log) so that Tack itself cannot rewrite history without
  coordination with the external log operator.
- **Real example.** Sigstore Rekor is used as an attestation target by
  software supply chain tools; the same pattern can be used for compliance
  audit Merkle roots.
- **Tack today.** Not implemented. The notarizer signs locally; the keys
  are local. This is the "cryptographic tamper evidence with single
  signing key" tier, not the "publicly verifiable transparency log"
  tier. Tack does not need the latter today.

### 4.4 PII separation at scale

- **Pattern.** PII is stored in a separate table (or service) keyed by
  reference; the audit log row stores only the reference. The hash chain
  is computed over the reference plus non-PII fields, never over the PII
  payload directly. GDPR redaction zeros the PII row and leaves the
  reference dangling; the hash chain remains valid because the
  pre-redaction PII bytes were never part of the hashed payload.
- **Tack today.** Implemented at `internal/audit/yugabyte.go:113-134`.
  The hash includes `pii_ref` (the UUID), not the PII payload.
- **Real systems.** This is the only pattern that survives compliance
  audit at scale. Storing PII in the audit row and "redacting" it later
  by overwrite breaks the chain; storing PII referenced by hash and
  redacting the referenced row preserves the chain. Per
  [the GDPR right to erasure design discussion](https://gdpr.eu/right-to-be-forgotten/)
  with its application to audit logs.
- **Cost.** One extra table read per PII-bearing event at write time. At
  the multi-million-EPS tier this read is batched (the producer pre-allocates
  PII references and writes them in batches) so the per-event cost is
  amortized.

### 4.5 Real systems by compliance tier

| System | Hash chain | Signing | PII separation | Throughput |
|---|---|---|---|---|
| Sigstore Rekor | Yes (per shard) | Yes (per minute) | By design (hashes only) | ~2k EPS per shard sustained |
| Certificate Transparency | Yes (per shard) | Yes (per log) | N/A (public certs) | ~30 to 100 EPS per log |
| CockroachDB SQL audit | Yes (per range) | No | By policy | ~10k to 100k per cluster |
| Splunk + integrity hashing | Hash on ingest | Operator-managed | By customer policy | ~1M EPS aggregate |
| AWS CloudTrail | No | No | By AWS controls | ~100k EPS per region per account |
| Google Cloud Audit Logs | No | No | By GCP controls | ~20 EPS per project free |
| GitHub audit | No | No | By GitHub controls | ~300 EPS per customer ceiling |
| Datadog Audit Trail | No | No | Single-tenant audit | Per-customer rate limits |
| Tack today | Yes (per (org, shard)) | Yes (per minute) | By design (pii_ref) | Designed; not yet at scale |

The systems that hash-chain plus sign top out at the low thousands of EPS
per shard. The systems that scale past that drop the cryptographic chain
in favor of operational controls. Tack is currently in the "Rekor-grade
properties, Rekor-grade throughput per shard" zone, which is appropriate.

---

## 5. Storage backends

Choosing the hot-tier store is the dominant architectural decision after
the broker.

### 5.1 ClickHouse

- **Throughput.** 1M+ inserts per second per server marketing; hundreds of
  thousands per second per shard sustained at Cloudflare and Yandex per
  the public posts.
- **Compliance.** ClickHouse has SOC 2 in ClickHouse Cloud; self-hosted
  inherits the operator's controls. RBAC, RLS, and column-level grants
  are supported. Per
  [the ClickHouse Cloud security page](https://clickhouse.com/cloud/security).
- **Cost.** Lower than Snowflake or BigQuery per byte at scale. NVMe-based.
- **Operational shape.** One DBA can run a small ClickHouse cluster.
  Multi-node ClickHouse with replication is more involved.
- **Verdict at 1M EPS.** Strong choice. ClickHouse is the most common
  open-source columnar store at this tier.

### 5.2 BigQuery

- **Throughput.** Streaming inserts at 100k+ rows per second per project,
  per [the BigQuery streaming inserts quotas](https://cloud.google.com/bigquery/quotas#streaminginserts).
  Storage Write API at higher rates with batching.
- **Compliance.** SOC 2, ISO 27001, HIPAA-eligible. Per
  [BigQuery compliance docs](https://cloud.google.com/bigquery/docs/compliance).
- **Cost.** Pay-per-byte storage and pay-per-query. Predictable at small
  scale, expensive at multi-million EPS unless using the flat-rate slot
  model.
- **Operational shape.** Managed; zero ops.
- **Verdict at 1M EPS.** Workable, expensive. Best if the rest of the
  stack is GCP.

### 5.3 Snowflake

- **Throughput.** Snowpipe streaming at 100k+ rows per second per pipe;
  multiple pipes per account. Per
  [the Snowflake Snowpipe streaming docs](https://docs.snowflake.com/en/user-guide/data-load-snowpipe-streaming-overview).
- **Compliance.** SOC 2, FedRAMP, HIPAA, and others. Per
  [the Snowflake compliance page](https://www.snowflake.com/en/data-cloud/security-compliance/).
- **Cost.** Pay-per-credit; expensive at multi-million EPS.
- **Verdict at 1M EPS.** Workable. Most appropriate when the customer
  already has Snowflake. Not the cost-minimum choice.

### 5.4 Iceberg / Delta Lake on object storage

- **Throughput.** Append-only writes to Parquet on S3 are arbitrarily
  scalable at the storage layer. The bottleneck is the writer process and
  metadata commit cadence (Iceberg snapshot commits or Delta transaction
  log writes). Per
  [the Iceberg performance docs](https://iceberg.apache.org/docs/latest/performance/).
- **Compliance.** Inherits the operator's S3 controls plus the table
  format's manifest commits (which are themselves auditable).
- **Cost.** Lowest per byte for archive. Higher query latency than
  columnar OLAP.
- **Verdict at 1M EPS.** As cold archive yes; as hot tier no. The query
  latency is too high for interactive audit forensics.

### 5.5 Yugabyte (Tack today)

- **Throughput.** Yugabyte's published distributed SQL benchmarks at
  [the Yugabyte performance docs](https://docs.yugabyte.com/preview/benchmark/)
  show 60k to 200k inserts per second per node depending on shape. Tack's
  current `audit.events` table is partitioned by week with 8 tablets per
  partition (`migrations/002_audit.sql:30`) which gives plenty of headroom
  for the first one to two orders of magnitude of growth from today.
- **Compliance.** Inherits the operator's controls. RLS via
  PostgreSQL-compatible policies (used at
  `migrations/002_audit.sql:206`).
- **Cost.** Operational cost similar to running Postgres, plus distributed
  consensus overhead.
- **Verdict at 1M EPS.** Insufficient as a hot tier. The published
  Yugabyte numbers max in the low hundreds of thousands per node and
  would require N nodes proportional to load. Real audit deployments at
  multi-million EPS use columnar OLAP (ClickHouse) for hot, not row-store
  distributed SQL.

### 5.6 Hybrid hot + cold (recommended target)

The pattern at 1M EPS sustained:

- **Producer**: Kafka or Redpanda topic, 256 partitions, replication
  factor 3, retention 7 days hot.
- **Hash chain projector**: small consumer service that reads the topic,
  computes per-shard hash chain, writes shard heads back to a small
  durable store (Postgres, Yugabyte, or even another Kafka topic with
  log compaction).
- **Hot OLAP**: ClickHouse with weekly partitions, 90 days retention, RBAC
  per tenant.
- **Cold archive**: nightly export from ClickHouse (or directly from
  Kafka) to Iceberg-on-S3 with Parquet files.
- **Notarizer**: separate single-replica process reading shard heads,
  producing signed Merkle roots, writing to a notarizations table.

Tack's current shape is a row-store-only variant of this. Moving to the
hybrid shape is the right multi-million-EPS plan.

---

## 6. Operational shape at scale

Per-system numbers, where public.

| System | Team size for the streaming layer | On-call rotation | DR posture | Monthly cost ballpark |
|---|---|---|---|---|
| LinkedIn Kafka | Dozens of engineers | 24/7 | Multi-region active-active | "Internal" (no public number) |
| Cloudflare logging | Dozens | 24/7 | Multi-region | Internal |
| Confluent customer at trillion msg/day | 5 to 20 | Yes | Multi-region | $1M+/month per public earnings calls |
| Splunk customer at multi-M EPS | 5+ platform engineers | Yes | Active-passive typical | $500k+/month for Splunk licenses |
| Datadog customer at high tens of thousands EPS | 1 to 3 | No (Datadog handles) | Datadog DR | $50k+/month |
| Sigstore Rekor public good | Small consortium team | Best effort | Multi-region | Public-good-funded |
| Honeycomb internal | ~12 platform engineers | Yes | Multi-region | Internal |
| Tack today (1 node, 0.37 EPS) | 1 (operator) | Best effort | Single-host | Low hundreds per month |

The scale of the operations team grows roughly as `log10(EPS)` plus a
constant. Going from 1k to 1M EPS roughly doubles the team. Going from
1 to 1k roughly doubles again. The cost-per-engineer of multi-million
EPS audit is the dominant line item, not the hardware.

---

## 7. Audit-grade compliance at over 1M EPS: does it exist?

Direct answer: **the public record does not document a system that
delivers all four of (a) hash chain, (b) cryptographic signing, (c) PII
separation, (d) sustained one-million-plus EPS, in production, to
external customers.**

The systems that meet (a) plus (b) plus (c) are Sigstore Rekor and
Certificate Transparency, both of which top out in the low thousands per
shard. Multi-shard architectures multiply that, but no public Rekor
deployment is at 1M EPS aggregate.

The systems that exceed 1M EPS sustained drop one of (a), (b), or (c).
The most common drop is (b): operational tamper evidence (chain of custody
through controlled access, immutable storage, off-host signing of
periodic roots) replaces per-event cryptographic signing. AWS CloudTrail,
Splunk, Datadog, and GitHub all fall here.

The compromises real systems accept:

- **Drop per-event signing, keep periodic root signing.** Most common.
  The ledger is hash-chained per shard and the heads are signed per
  minute or per hour. Per-event signing is replaced by inclusion proofs
  derivable from shard heads. This is what Tack does today; it is
  appropriate at any scale.
- **Drop per-shard hash chain, keep aggregate hash check.** Splunk's
  default integrity hashing per indexer bucket is the example. Per-event
  chain integrity is replaced by per-bucket integrity. Tampering inside a
  bucket is detectable; tampering with bucket boundaries is not.
- **Drop cryptographic anything, replace with operational controls.**
  AWS CloudTrail, Google Cloud Audit. The contract is "we (the cloud
  provider) guarantee the audit log integrity by our controls." Customers
  who need cryptographic guarantees layer them on (export to Sigstore,
  external signing).

The honest conclusion: at multi-million EPS, the published architectures
trade off some compliance property for throughput. Tack's current design
is in the Rekor-equivalent compliance tier with Rekor-equivalent
sustained throughput per shard. Scaling Tack to 1M EPS with full
compliance properties retained is engineering territory not visible in
the public record. It is achievable in principle by simply running enough
shards (for example, 1000 shards each at 1k EPS sustained gets to 1M EPS
aggregate), and the per-shard chain plus periodic Merkle pattern survives
that scale in theory. No public deployment has demonstrated it.

The surprising finding from the research, restated: the scale ceiling for
"compliance audit with cryptographic tamper-evidence" in the public record
is roughly two thousand EPS per shard, with shard count as the scale-out
knob. The published architectures that exceed this trade integrity for
throughput. Tack's design is consistent with the research; the question
"what does Tack at 1M EPS look like" is answered "256 to 1000 shards
each sustaining 1k to 4k EPS, with broker-class buffering between
producer and projector, and ClickHouse-class columnar hot storage." That
is the design Output B targets.

---

## 8. Sources cited

- [Apache Kafka producer configuration](https://kafka.apache.org/documentation/#producerconfigs)
- [Apache Kafka exactly-once semantics design](https://kafka.apache.org/documentation/#semantics)
- [AWS CloudTrail events overview](https://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-events.html)
- [AWS CloudTrail quotas](https://docs.aws.amazon.com/awscloudtrail/latest/userguide/WhatIsCloudTrail-Limits.html)
- [BigQuery compliance](https://cloud.google.com/bigquery/docs/compliance)
- [BigQuery streaming insert quotas](https://cloud.google.com/bigquery/quotas#streaminginserts)
- [Certificate Transparency project](https://certificate.transparency.dev/)
- [ClickHouse architecture overview](https://clickhouse.com/docs/en/development/architecture)
- [ClickHouse Cloud security](https://clickhouse.com/cloud/security)
- [ClickHouse performance docs](https://clickhouse.com/docs/en/operations/performance)
- [Cloudflare audit logs (legacy)](https://developers.cloudflare.com/fundamentals/setup/account/account-security/review-audit-logs/)
- [Cloudflare HTTP analytics 6M RPS via ClickHouse](https://blog.cloudflare.com/http-analytics-for-6m-requests-per-second-using-clickhouse/)
- [Cloudflare logging pipeline overview](https://blog.cloudflare.com/an-overview-of-cloudflares-logging-pipeline/)
- [Cloudflare 1 trillion Kafka messages](https://blog.cloudflare.com/using-apache-kafka-to-process-1-trillion-messages/)
- [CockroachDB SQL audit logging](https://www.cockroachlabs.com/docs/stable/sql-audit-logging.html)
- [Confluent Cluster Linking and multi-region](https://docs.confluent.io/platform/current/multi-dc-deployments/multi-region.html)
- [Confluent Kafka 4-comma club post](https://www.confluent.io/blog/apache-kafka-hits-1-1-trillion-messages-per-day-joins-the-4-comma-club/)
- [Datadog Audit Trail docs](https://docs.datadoghq.com/account_management/audit_trail/)
- [Datadog Logs Without Limits](https://docs.datadoghq.com/logs/guide/getting-started-lwl/)
- [Datadog rate limits](https://docs.datadoghq.com/api/latest/rate-limits/)
- [Factor House best Kafka management tools](https://factorhouse.io/articles/best-kafka-management-tools)
- [GitHub audit log streaming](https://docs.github.com/en/enterprise-cloud@latest/admin/monitoring-activity-in-your-enterprise/reviewing-audit-logs-for-your-enterprise/streaming-the-audit-log-for-your-enterprise)
- [GitHub audit log API rate-limit announcement](https://devclass.com/2023/07/04/github-will-rate-limit-enterprise-audit-log-to-prevent-significant-strain-on-our-data-stores/)
- [Google Trillian project](https://github.com/google/trillian)
- [Honeycomb platform overview](https://www.honeycomb.io/platform)
- [Honeycomb scaling Kafka pipelines](https://www.honeycomb.io/blog/scaling-kafka-observability-pipelines)
- [Iceberg performance docs](https://iceberg.apache.org/docs/latest/performance/)
- [LinkedIn Apache Kafka 7 trillion messages per day](https://www.linkedin.com/blog/engineering/open-source/apache-kafka-trillion-messages)
- [LinkedIn benchmarking Kafka two million writes per second](https://engineering.linkedin.com/kafka/benchmarking-apache-kafka-2-million-writes-second-three-cheap-machines)
- [Notion data lake post](https://www.notion.com/blog/building-and-scaling-notions-data-lake)
- [Onehouse Notion data scale analysis](https://www.onehouse.ai/blog/notions-journey-through-different-stages-of-data-scale)
- [Redpanda self-hosted benchmarking guide](https://www.redpanda.com/blog/self-hosted-redpanda-benchmarking)
- [Sigstore architecture and sharding](https://docs.sigstore.dev/logging/sharding/)
- [Sigstore logging overview](https://docs.sigstore.dev/logging/overview/)
- [Snowflake compliance and security](https://www.snowflake.com/en/data-cloud/security-compliance/)
- [Snowflake Snowpipe streaming](https://docs.snowflake.com/en/user-guide/data-load-snowpipe-streaming-overview)
- [Splunk indexer cluster docs](https://docs.splunk.com/Documentation/Splunk/latest/Indexer/Aboutindexesandindexers)
- [Splunk validated architectures](https://www.splunk.com/en_us/pdfs/tech-brief/splunk-validated-architectures.pdf)
- [Splunk EPS heavy-forwarder community thread](https://community.splunk.com/t5/Getting-Data-In/How-many-events-per-second-a-heavy-forwarder-can-ingest-with-the/m-p/433116)
- [TigerBeetle ledger blog](https://tigerbeetle.com/blog/)
- [Uber M3 introduction](https://eng.uber.com/m3/)
- [Yandex ClickHouse case study](https://clickhouse.com/customer-stories/yandex)
- [Yugabyte performance benchmarks](https://docs.yugabyte.com/preview/benchmark/)
- [GDPR right to erasure overview](https://gdpr.eu/right-to-be-forgotten/)

Sources searched without finding a public sustained number:

- "Stripe ledger throughput events per second" returned no first-party
  Stripe documentation.
- "Atlassian Jira audit log throughput" returned no first-party
  engineering blog post.
- "Plane (Plane CE) audit throughput" returned no public benchmark.
- "Notion audit log events per second" returned data-lake posts but no
  audit-specific number.
- "Asana audit log events per second" returned no public number.
- "AWS CloudTrail aggregate events per second across all accounts"
  returned no published number.
