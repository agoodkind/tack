# Audit recording lifecycle and formats

These lifecycle and format requirements supplement the [complete audit recording contract](2026-10-21-audit-completeness-design.md).

## Read ownership and recovery

Each durable read admission identifies an owner token and a generation number. That number identifies the current period of ownership: recovery changes it to prevent a stale owner from releasing a result. When finalization or recovery ends the read, a terminal receipt stores its outcome and digest. The receipt lets a retry determine whether the same completion already committed.

Both storage adapters implement:

- Admission.
- Renewal.
- Finalization.
- Expired-owner recovery.
- Publication acknowledgment.
- Published-receipt cleanup.

The relay can publish a read completion after finalization writes it to the outbox. Pending admissions are not outbox entries.

Each transition requires one transaction.

Lifecycle transitions:

| Transition | Required behavior |
| --- | --- |
| Begin | The storage adapter must create the event template, random owner token, generation, deadline, and expiry index. It must accept an existing identity only for the same admission retry. |
| Renew | The storage adapter must compare owner and generation and replace the lease deadline and expiry index together. |
| Finalize | The storage adapter must compare owner and generation. It must save the complete event and parts in the outbox and its digest in a terminal receipt. It must remove pending state and its expiry index. |
| Recover | The storage adapter must reread the expired candidate and compare generation and indexed deadline. It must save an incomplete event and receipt, then remove pending state. It must not rerun the read. |
| Acknowledge | After Kafka acknowledges the exact event, the storage adapter must delete the outbox entry and mark its matching receipt published together. |
| Clean up | The storage adapter must delete published receipts after their retry window. It must not delete active evidence solely because it is old. |

FoundationDB stores pending, expiry, receipt, and cleanup records as tuple keys and uses its injected clock for recovery eligibility. Finalization must write the versionstamped outbox record in the same transaction as the terminal receipt; a standalone append that commits separately cannot satisfy this rule. YugabyteDB uses transient lifecycle state beside the operator outbox, database time, conditional updates, and indexes for expired work and published receipts. Narrow lifecycle functions permit application execution without direct ledger modification rights.

Transaction comparisons decide ownership. Time establishes recovery eligibility. Clock differences can cause early recovery, but a stale owner must not finalize after another owner changes the generation. A handler that loses finalization must discard its result and return a recording error.

The implementation uses bounded lifecycle settings.

Lifecycle settings:

| Setting | Value |
| --- | --- |
| Recovery buckets | 64 |
| Candidates per scan | 32 |
| Lease | 30 seconds |
| Renewal interval | 10 seconds |
| Admission retry deadline | 30 seconds |
| Published receipt retention | One hour after publication |
| Receipt cleanup batch | 256 |
| Initial generation | 1 |

Recovery selects the bucket from the first SHA-256 byte of the event UUID modulo the bucket count. When an application instance starts audit read recovery, each store worker chooses a starting bucket and scans all buckets without a global lock. Renewals preserve generation; recovery increments it transactionally. Owners renew only while their requests remain active.

Admission retries retain their original identity and fail after their deadline. The executor generates admission IDs; clients must not reuse an old audit ID as a new admission. Finalization must preserve the admitted actor, event identity, operation context, and start time. An uncertain-commit retry must supply identical final data and transport digest. A matching terminal receipt confirms that commit. A different digest or incomplete receipt rejects the retry. Finalization must not recreate missing pending state.

Final recording after cancellation uses a detached context with a 30-second deadline. A completed read means that the server prepared the result; it does not prove client receipt. Finished events remain queued until delivery succeeds. Storage exhaustion must refuse new work rather than permit unaudited success.

## Read payloads and manifests

The read contract requires shared adapters to collect resolved identities in returned order. The executor must record normalized selection criteria, result count, and completion status, compute SHA-256 over the exact serialized result payload, and store the digest and format identifier. The transport must emit those saved bytes rather than serialize the result again. Transport envelopes remain outside the declared payload format.

A digest permits comparison with a candidate payload. It cannot reconstruct historical content or establish that timestamps uniquely identify data versions.

Each read manifest part identifies its event. The following requirements apply to the ordered list of returned identities.

Read manifest requirements:

| Subject | Requirement |
| --- | --- |
| Size | Each part contains up to 256 ordered identities in at most 64 KiB of encoded data. |
| Event evidence | The canonical event records the total identity count and a digest of the ordered manifest. The separate result fingerprint covers the serialized result payload. |
| Permanent part | The consumer must store each part's identities and the digest of its encoded bytes in `audit.event_parts`. Parts do not store an individual object fingerprint or a copy of a returned object. |
| Completeness | The storage and transport components must preserve all parts. The consumer must verify the complete manifest before committing the event. Exports and backups must include the permanent parts. |
| Organization access | Only the audit reader and export interfaces may expose permanent parts. `Reader.ManifestParts` filters by organization ID and event ID and returns parts in index order. The exporter supplies both IDs from the event row. |

The `audit_reader` database role can select permanent parts for every organization. The reader query enforces the organization boundary. The row-level policies for transient assembly tables grant access only to `audit_writer`.

## Multipart transport

When encoding a transport envelope for any operation class, `EncodeEvent` splits the serialized event envelope into transport parts with at most 64 KiB of payload each. The limit applies before the encoder adds transport message fields. The change transaction must commit the manifest and every part with the changed data, including old and new values larger than one FoundationDB value. The relay publishes parts before the manifest and retains them until acknowledgment. Incomplete assemblies must remain durable; the consumer must not commit a canonical event with absent parts.

Each message identifies its kind (part or manifest) and format version. Every publication and replay of a message for the same event must use the event's original organization and shard as its Kafka key. SHA-256 digests use lowercase hexadecimal.

Transport fields identify the event, its parts, and the bytes used for digest comparisons.

Transport fields:

| Field | Meaning |
| --- | --- |
| Event ID and part count | Identify the logical event and total number of parts. |
| Part index and part digest | Identify a part's position and compare the SHA-256 digest of its exact bytes with the expected digest. |
| Transport digest | Compare the SHA-256 digest of the complete serialized envelope, including the event and ordered read targets, with the expected digest. |

The encoder must compare the ordered targets with the declared manifest digest and part count. Mutation and external events use an empty read-target list. An event without read targets may remain one record when it fits in one part. Multipart delivery must preserve its logical event identity.

The consumer alone may read transient assembly tables. A transient part identifies its event and position within that event. Each part's bytes are immutable. An identical retry succeeds, and conflicting bytes require invalid-event handling. Each received part or manifest must commit with its SQL consumer offset and mark the assembly ready when every required part and manifest exists. Advancing that offset for an incomplete assembly does not claim a permanent event.

A bounded completion worker verifies the ordered digest and reconstructs the event. One transaction claims the event ID, separates protected data, appends the canonical row, stores permanent read-identity parts, and removes transient assembly state. The canonical row stores the complete delta once. A crash must repeat completion safely through the permanent event-ID claim.

A replayed part must check permanent event identity and completed transport digest. When the canonical event already exists, a matching identity and digest permit offset advancement without a new assembly. Missing parts require a delivery alert and durable retry without blocking unrelated complete events. Audit evidence must not expire solely because an assembly remains incomplete.

## Ledger formats and integrity

Complete events serialize typed targets in `context.target` and actual changes in `delta`. Targets support:

- Node identity.
- Relationship identity.
- Resource address.
- External resource identity.

Searchable columns must derive from that serialized event.

The format version forms part of hashed context. Historical rows must preserve omission of new fields, historical outcome strings, and original hash semantics. Format 2 requires class-specific evidence and explicit terminal outcomes. An omitted outcome must not become success. Only external intent may use pending; pending read state must not enter the ledger. Invalid events must remain available for repair through existing invalid-event handling.

The permanent ledger must remain `audit.events`, and only the audit consumer may write it. Recovery must preserve `audit.chain_heads` and continue every organization/shard chain. Hash and signed export verification must detect alterations to targets, deltas, outcomes, and manifests for both historical and complete formats.
