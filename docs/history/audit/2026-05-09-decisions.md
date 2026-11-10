# Earlier audit architecture decisions

The audit design began on May 9, 2026. Its November 7 source snapshot separates the first write-ahead-log repair from the later broker architecture. A write-ahead log records pending work durably before completion. The design retained that repair until the broker could provide equivalent behavior.

## Component selection

The earlier design recorded its component choices and rejected alternatives.

Recorded decisions:

| Component | Selection | Recorded rationale or rejected alternative |
| --- | --- | --- |
| Event broker | Apache Kafka in KRaft mode | The design required open-source licensing, independent durability review, vendor-independent governance, and established operational knowledge. It accepted JVM tuning and garbage-collection costs. |
| Redpanda | Rejected | Its Business Source License conflicted with the open-source-only constraint. An earlier draft had selected it for its smaller operational footprint. |
| NATS JetStream | Rejected | The design cited the December 8, 2025 Jepsen analysis of NATS 2.12.1 and its unresolved data-loss findings. It left reconsideration dependent on fixes and a new clean audit. A change would require producer and consumer work because JetStream does not use Kafka's protocol. |
| Apache Pulsar | Rejected | The design cited the additional broker, BookKeeper, and metadata services, plus a smaller Go-client and operating-knowledge community. It acknowledged stronger broker-level multi-tenancy. |
| Object storage | SeaweedFS, with Garage as an alternative | The design preferred SeaweedFS for its operating history and deployment community. It retained Garage for its single-binary Rust implementation and geographic replication design. Both exposed S3. |
| Earlier object-storage proposal | MinIO for one host and AWS S3 for multiple hosts | The design abandoned that split. Managed cloud services conflicted with its operating constraint, and it preferred the later storage alternatives over MinIO's licensing. |
| Go Kafka client | franz-go | The design rejected the older alternative client API and its less complete transactional-producer support. |

These statements preserve the design's selection record. They do not assess current product licenses, current third-party findings, or deployment status.

## Earlier incidents and open questions

The design cited the May 9 seed-parallel-organization retrospective as evidence that earlier backups had silently produced empty results. It also reported a global address index and an absent reverse index as unresolved constraints on audit queries and forensics.

The design left these questions open at that revision:

- It did not settle address-index scope or reverse-index support.
- It did not define a response to sustained throughput beyond the fixed 256 audit shards.
- It considered YugabyteDB advisory locks, FoundationDB compare-and-set, and a Kafka lease for notarizer leadership without selecting one.
- It did not implement external attestation through Sigstore Rekor.
- It identified event-schema evolution and producer-consumer deployment order as unresolved compatibility work.
- It treated strict isolation between shards and bulk personal-information redaction at larger scale as unfinished designs.

The [original audit design](https://github.com/agoodkind/tack/blob/7e469f9851c55e0ae36a03fe279287c6a430da05/docs/plans/audit-horizontal-design.md) provides the historical context. Its unresolved questions do not establish the status of the current implementation.
