# Audit scale requirements

Tack must support many tenants and horizontal scale before shipping an audit design. Single-host and multi-host installations must use the same audit components and identical producer, consumer, notarizer, and personal-data redactor code. Replica counts, placement, configuration, and operating procedures may differ. The same verifier must validate ledger chains at every replica count. A change in replica count must preserve hash chains, periodic Merkle notarization, protected-value separation, and existing ledger history without introducing a different permanent ledger.

Audit components must support self-hosted operation under open-source licenses. Business Source License components, proprietary components, and required managed cloud services do not satisfy this constraint. Each component choice must satisfy these constraints at both scales. A broker or archive that can run only on one host, or a cache that cannot divide its data among instances, cannot satisfy them.

## Storage and delivery

FoundationDB owns product data. YugabyteDB owns authentication records and the compliance ledger. Apache Kafka provides delivery between durable outboxes and the audit consumer. Configs owns the SeaweedFS object-store LXC, the `weed` service under systemd, and its S3 endpoint. Object storage provides a backup destination rather than quorum replication.

The [Kafka delivery explanation](../../explanation/audit/audit-kafka-cutover.md) defines acknowledgment, permanent projection, retries, and multipart completion. Producer, consumer, and verifier behavior must preserve those distinctions at every replica count.

Each organization/shard pair has an independent hash chain. Transactions serialize advancement of one chain, while independent chains permit parallel work. Actor and event hashing distributes events from a busy actor across shards. Event-type routing must not introduce handwritten product rules.

Shard count and Kafka partition count are separate controls. Existing ledger rows retain their recorded shards after a forward-only shard-count change. Neither count guarantees tenant isolation or a measured throughput. Consumers sharing a process, partition, database, or host can still compete for resources.

A shard-count change requires a supported power of two and coordinated producer and consumer revisions. Submit the change through the normal pull request process. In `TestShardOfPinnedAndBucketed`, set the expected `ShardCount` to the proposed count and the expected shard for the fixed actor and event IDs to the value those IDs produce under the new mask.

Announce the interruption to MCP clients if producers must stop for the change. Every old-count event must complete delivery before the new count activates. The drain must account for outboxes, Kafka partitions, dead letters, and incomplete assemblies. A zero Kafka lag gauge alone cannot prove that boundary. Rows produced under the new count must have shard numbers below that count. Acceptance requires a verified export covering both old and new shard spaces without rewriting existing rows or resetting heads.

`ops audit consumer-offsets` reports Kafka lag and operator outboxes but does not report incomplete assemblies. The transient assembly tables permit the consumer writer to read them; their forced row-level policies do not permit the normal migration owner to inspect them through `ops db sql`. A diagnostic count under that role cannot prove emptiness. Refuse a shard-count activation until audited evidence accounts for every old-count event and assembly.

## Chain integrity and verification

Each chain head records its last sequence and hash. Each append computes a hash from the previous hash and canonical event data and advances `audit.chain_heads` transactionally. Recovery must preserve chain heads and historical hash versions. A new replica must continue those chains rather than restart them.

When the consumer has a signing key path, it starts the notarizer. The notarizer computes a Merkle root, a single hash that summarizes the list of shard numbers, last sequences, and hashes for one organization at a checkpoint. It signs that root with Ed25519 and stores the manifest and signature in `audit.notarizations`. A later head can advance beyond the checkpoint. Verification must compare the historical chain position in the signed manifest with the corresponding ledger position.

`audit signers --execute` requires a local verification key and a configured set of allowed signing-key identifiers. `--signers` overrides `AUDIT_VALID_SIGNERS`, and `--pub` overrides `AUDIT_SIGNING_KEY_PATH`. With `--since <RFC3339-time>`, it scans notarizations at or after that time across organizations. Without `--since`, it scans all notarizations. An empty signer set logs an error and stops the inspection.

The notarizer and export verifier use the same identifier derived from the Ed25519 public key. The report identifies the local signer. For each rejected signing-key identifier, it reports the number of matching rows, their earliest and latest notarization times, and the distinct hosts claimed by those rows. The report calculates each time span from the row times independently of scan order. The signed bytes do not include the host or key identifier. The signature cannot establish which host signed the root.

An allowed key without local verification material fails the check unless the operator supplies `--allow-unverified`. The option cannot admit a local key outside the configured set or excuse a failed signature under the local key. When the scan succeeds, the command prints the report before returning a failing verdict for rejected or unacknowledged results.

Signing replicas must coordinate their work. One replica and multiple replicas must use the same reviewed key-rotation procedure. The signing keys also require a reviewed distribution procedure. The current notarizer does not implement leader election. Multiple configured replicas do not prove coordinated signing.

Each notarizer loads one key and signs each organization's root once per pass. It does not implement the proposed rotation overlap in which both keys sign for one full signing interval.

The offline export verifier reads the events file identified in the signed manifest. It scans one row at a time and allocates fixed-size chain records for the rows it actually reads. It does not allocate from the unverified manifest row count. Filters by time, actor, or entity can omit intervening rows. The verifier counts these sequence gaps without treating them as integrity failures.

The verifier rejects the bundle when any of these checks fails:

- The manifest must fit within the 1 MiB input limit. Neither the manifest nor a row may contain unknown fields.
- The signed row count must equal the number of rows the verifier scans. Every row must belong to the manifest's organization.
- Row hashes and the events file digest must match. Each sequence number must occur only once per shard. Rows with consecutive sequence numbers in one shard must have matching previous-hash links. The row must use a recognized hash version. A legacy version cannot claim a stored timestamp with sub-microsecond precision.
- The supplied public key must have the correct Ed25519 length. The verifier must confirm the manifest signature with that key. With a configured signer set, the verified key must belong to that set and match the signer identified in the manifest.

## Durability and recovery

Quorum replication across fault domains must preserve committed data after a node or zone fails. Point-in-time recovery and tested backups must cover corruption and accidental deletion that replication also copies. A single broker or database replica cannot establish host-loss tolerance. A restore drill must restore the available backups into a disposable environment before they can serve as evidence of recovery.

Kafka must disable unclean leader election. An out-of-sync replica must not become leader and replace acknowledged audit evidence. Controller quorum, broker replication, and minimum in-sync replicas require separate configuration; a controller majority does not prove that topic data has enough copies.

Broker retention must exceed the supported delivery outage and recovery backlog. An unread record can expire at the broker before projection; a configured retention request does not prove that the broker accepted it. Operators must observe effective retention, consumer lag, dead letters, incomplete assemblies, and permanent projection separately.

## Capacity evidence

The [complete recording contract](2026-10-21-audit-completeness-design.md#retention-and-scale) specifies the workload comparison and required measurements. An installation capacity estimate also needs an identified workload and observation interval.

## Safe changes

Use the reviewed Configs deployment path for infrastructure and release configuration. Select a published revision that passes the required image workflow checks. Verify image tags and digests for every changed service. Image verification does not prove provisioning, migrations, public behavior, ledger integrity, or audit completeness.

Validate migrations, seeds, backfills, restores, and changed audit behavior on disposable QA before production. Production tests require explicit operator authorization. The historical production manifest repair requires separate approval of its refreshed manifest after QA. Preserve authentication records, permanent audit rows, chain heads, protected-value references, and queued evidence during recovery or rollback. [Historical repair approval](https://tack.home.goodkind.io/browse/TACK-583)
