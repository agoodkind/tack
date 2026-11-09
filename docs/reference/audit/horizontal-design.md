# Audit scale requirements

Tack must support many tenants and horizontal scale before shipping an audit design. Single-host and multi-host installations must use the same audit components. Both installation types must use identical code for:

- The producer.
- The consumer.
- The notarizer.
- The personal-data redactor.

The following installation choices may differ:

- Replica counts.
- Placement.
- Configuration.
- Operating procedures.

The same verifier must validate ledger chains at every replica count. A change in replica count must preserve:

- Hash chains.
- Periodic Merkle notarization.
- Protected-value separation.
- Existing ledger history.

A replica-count change must not introduce a different permanent ledger.

Audit components must support self-hosted operation under open-source licenses. Business Source License components, proprietary components, and required managed cloud services do not satisfy this constraint. Each component choice must satisfy these constraints at both scales. A broker or archive that can run only on one host, or a cache that cannot divide its data among instances, cannot satisfy them.

## Storage and delivery

FoundationDB owns product data. YugabyteDB owns authentication records and the compliance ledger. Apache Kafka provides delivery between durable outboxes and the audit consumer. Configs owns the SeaweedFS object-store LXC, the `weed` service under systemd, and its S3 endpoint. Object storage provides a backup destination rather than quorum replication.

The [Kafka delivery explanation](../../explanation/audit/audit-kafka-cutover.md) defines acknowledgment, permanent projection, retries, and multipart completion. Producer, consumer, and verifier behavior must preserve those distinctions at every replica count.

Each organization/shard pair has an independent hash chain. Transactions serialize advancement of one chain, while independent chains permit parallel work. Actor and event hashing distributes events from a busy actor across shards. Event-type routing must not introduce handwritten product rules.

Shard count and Kafka partition count are separate controls. Existing ledger rows retain their recorded shards after a forward-only shard-count change. Neither count guarantees tenant isolation or a measured throughput. Consumers sharing a process, partition, database, or host can still compete for resources.

A shard-count change requires a supported power of two and coordinated producer and consumer revisions. The change author must submit the change through the normal pull request process. In `TestShardOfPinnedAndBucketed`, the expected `ShardCount` must equal the proposed count. The expected shard for the fixed actor and event IDs must equal the value those IDs produce under the new mask.

The operator must announce the interruption to MCP clients if producers must stop for the change. Every old-count event must complete delivery before the new count activates. The drain must account for:

- Outboxes.
- Kafka partitions.
- Dead letters.
- Incomplete assemblies.

A zero Kafka lag gauge alone cannot prove that boundary. Rows produced under the new count must have shard numbers below that count. Acceptance requires a verified export covering both old and new shard spaces without rewriting existing rows or resetting heads.

`ops audit consumer-offsets` reports Kafka lag and operator outboxes but does not report incomplete assemblies. The transient assembly tables permit the consumer writer to read them; their forced row-level policies do not permit the normal migration owner to inspect them through `ops db sql`. A diagnostic count under that role cannot prove emptiness. The operator must refuse a shard-count activation until audited evidence accounts for every old-count event and assembly.

## Chain integrity and verification

Each chain head records its last sequence and hash. Each append computes a hash from the previous hash and canonical event data and advances `audit.chain_heads` transactionally. Recovery must preserve chain heads and historical hash versions. A new replica must continue those chains rather than restart them.

When the consumer has a signing key path, it starts the notarizer. The notarizer computes a Merkle root, a single hash that summarizes the list of shard numbers, last sequences, and hashes for one organization at a checkpoint. It signs that root with Ed25519 and stores the manifest and signature in `audit.notarizations`. A later head can advance beyond the checkpoint. Verification must compare the historical chain position in the signed manifest with the corresponding ledger position.

`audit signers --execute` requires a local verification key and a configured set of allowed signing-key identifiers. `--signers` overrides `AUDIT_VALID_SIGNERS`, and `--pub` overrides `AUDIT_SIGNING_KEY_PATH`. With `--since <RFC3339-time>`, it scans notarizations at or after that time across organizations. Without `--since`, it scans all notarizations. When the signer set is empty, the `audit signers` command logs an error and stops before opening the reader.

The notarizer and export verifier use the same identifier derived from the Ed25519 public key. The report identifies the local signer. For each rejected signing-key identifier, the report includes:

- The number of matching rows.
- Their earliest and latest notarization times.
- The distinct hosts claimed by those rows.

The report calculates each time span from the row times independently of scan order. The signed bytes do not include the host or key identifier. The signature cannot establish which host signed the root.

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

Broker retention must exceed the supported delivery outage and recovery backlog. An unread record can expire at the broker before projection; a configured retention request does not prove that the broker accepted it. Operators must observe each delivery condition separately:

- Effective retention.
- Consumer lag.
- Dead letters.
- Incomplete assemblies.
- Permanent projection.

## Capacity evidence

The [complete recording contract](2026-10-21-audit-completeness-design.md#retention-and-scale) specifies the workload comparison and required measurements. An installation capacity estimate also needs an identified workload and observation interval.

## Safe changes

The operator must use the reviewed Configs deployment path for infrastructure and release configuration. The selected published revision must pass the required image workflow checks. The operator must verify image tags and digests for every changed service. Image verification does not prove provisioning, migrations, public behavior, ledger integrity, or audit completeness.

QA acceptance must validate each change before production:

- Migrations.
- Seeds.
- Backfills.
- Restores.
- Changed audit behavior.

QA must remain disposable.

Production tests require explicit operator authorization. The historical production manifest repair requires separate approval of its refreshed manifest after QA. The operator must preserve the following data during recovery or rollback:

- Authentication records.
- Permanent audit rows.
- Chain heads.
- Protected-value references.
- Queued evidence.

[Historical repair approval](https://tack.home.goodkind.io/browse/TACK-583)
