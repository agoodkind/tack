# Complete audit recording

Tack must record every application operation and committed change in its existing audit ledger. Records establish the actor, action, target, time and outcome. Changes preserve actual old and new values. Reads establish the request and returned result identities.

This draft resolves engineering choices for the audit correction tracked by TACK-583. The source review used `d5b38f8a`. The correction has not deployed.

## Scope

Cover operations after the required services exist: registered HTTP and MCP operations, operator commands, database writers, background jobs, provider hooks and deployment operations that invoke Tack. Include refusals, failures, partial work and interrupted work. Creating services from an empty installation is outside this correction. Execute the reviewed runtime operation inventory during acceptance; declarations alone do not prove coverage.

Complete audit recording first. Implement repository-triggered QA and production deployment second, with validation before merge. Complete production access restrictions third. User-facing history remains separate. Preserve existing ledger rows and hash chains. Do not reconstruct missing historical values or create a second permanent history store. Current installation capacity affects deployment sizing rather than product architecture.

## Approved recording contracts

| Operation | Required recording |
|---|---|
| A database write changes records. | Commit actual changes and pending audit events in the same transaction. Missing audit context prevents the write. |
| A deletion removes nodes or relationships or moves children. | Record complete deleted values and actual old/new moved values in each bounded transaction. Identify the original actor and enclosing operation. |
| A read returns data. | Save durable pending state before reading. Finalize one immutable event before returning the result. Deliver it to the ledger asynchronously. |
| An external operation changes another system. | Save intent before the effect. Record observed steps and results. Recovery checks actual state before repeating an effect. |
| A command changes SQL records. | Use `WriteOutboxTx` in the change transaction. Record a failed attempt separately after rollback. |

Product reads use FoundationDB delivery state. Operator reads use YugabyteDB delivery state. One permanent read event replaces the earlier two-event proposal. Two durable local writes remain: admission and finalization. Successful finalization does not wait for Kafka or ledger projection.

## Shared operation definition

Extend `audit.Spec`. Each registration supplies a stable operation code, a read/database-change/external-operation class, a typed target extractor and safe input selection. Generated tools inherit their family definition. Node types and property names do not select handwritten audit rules.

The executor supplies identity, operation ID, event ID, timing and outcome. Writers derive actual changes inside their transactions. Registration rejects missing definitions. A read declaration cannot authorize a mutation.

Terminal outcomes are completed, refused, failed, partially completed and incomplete. Failed means that the operation did not commit effects. Partial completion identifies committed steps. Incomplete means that the available evidence cannot establish a terminal result. Preserve operation failure and audit-recording failure separately.

An outer recorder starts before authentication and dispatch. It covers malformed requests, unsupported methods, unknown tools, prompts, resources, health checks and diagnostics. Successful authentication supplies a verified identity. Earlier failures use an unauthenticated actor and the existing system audit organization. Record method, route, operation and safe reason fields. Exclude credentials, raw request bodies and raw database errors. Link authentication and business events by operation ID without conflating their outcomes.

## Read lifecycle

Add transient pending records and compact terminal receipts beside each outbox. Both adapters implement `BeginRead`, `RenewRead`, `FinalizeRead`, `RecoverExpired` and `AcknowledgePublished`. The relay reads finalized entries only.

| Transition | Required transaction |
|---|---|
| Begin | Create the event template, owner token, generation and expiry index. Accept an existing identity only for the same admission retry. |
| Renew | Compare owner and generation. Replace the lease deadline and expiry entry together. |
| Finalize | Compare owner and generation. Save the final event in the outbox and its digest in a terminal receipt. Remove pending state and its expiry entry. |
| Recover | Reread an expired candidate. Compare generation and indexed deadline. Save an incomplete event and receipt, then remove pending state. Do not rerun the read. |
| Acknowledge | After Kafka acknowledges the exact event, remove its outbox entry and mark the matching receipt published together. |
| Clean up | Delete published receipts after their retry window. Never delete active evidence solely because it is old. |

FoundationDB uses tuple keys for `audit_read_pending(event_id)`, `audit_read_expiry(bucket, deadline, event_id)`, `audit_read_receipt(event_id)` and `audit_read_cleanup(bucket, time, event_id)`. Finalization writes the versionstamped outbox key inside its transaction. The standalone append method is unsuitable because it commits separately.

YugabyteDB uses one transient table beside `public.ops_outbox`, indexed for expired work and published receipts. It uses database time and conditional updates. Narrow lifecycle functions grant application execution rights without direct ledger modification rights.

Each owner receives a random token and generation. Transaction comparisons decide ownership. Time only establishes recovery eligibility. Clock differences can cause early recovery, but a stale owner cannot finalize after another owner changes the generation. A handler that loses finalization discards its result and returns an audit-recording error.

Agent-selected initial settings are 64 recovery buckets, 32 candidates per scan, a 30-second lease, renewal every 10 seconds and cleanup batches of 256 receipts. Workers rotate buckets independently on every application instance. They do not acquire a global lock. Reuse the existing search worker's transaction pattern without its search-specific state.

Admission retries have a 30-second deadline and retain their original identity. The executor generates the ID internally; clients cannot submit an old audit ID as a new admission. Finalization retries retain the original bytes. A matching terminal digest confirms an uncertain commit. A different digest or incomplete terminal outcome rejects the retry. Finalization never recreates missing pending state. Retain published receipts for one hour after publication.

Permanent event-ID claims prevent duplicate ledger rows after relay retries. Finished events remain queued until delivery succeeds. When configured storage cannot accept another durable record, refuse new work rather than report an unaudited success.

## Read evidence and size bounds

Shared adapters collect resolved object identities. The executor records normalized selection criteria, result count and completion status. Compute SHA-256 over the exact serialized result payload before returning it. Store its digest and format identifier rather than another response copy. A digest permits comparison with a candidate payload. It does not reconstruct historical content or establish that timestamps uniquely identify data versions.

Bounded requests produce one final event. Streaming operations finalize each emitted page or chunk before release and link those records to the enclosing operation. This applies the read guarantee before any stream data becomes visible.

Large identity lists use ordered manifest parts associated with the event ID. Each part contains at most 256 identities and fingerprints and at most 64 KiB of encoded data. The canonical event stores the total count and digest of the ordered manifest. Store parts in the audit schema and include them in exports and backups. The consumer verifies the manifest before committing its event. Parts do not contain returned object bodies. Storage and broker delivery must preserve all parts without truncating evidence.

Changes produce one event per changed record under the same operation ID. Large event payloads use the same bounded part mechanism; the transaction commits their manifest and parts with the change. This handles complete old/new values that exceed one FoundationDB value. The relay publishes parts before the event manifest. Retain incomplete assemblies durably and retry missing parts; do not commit a ledger event with absent parts.

Part delivery uses explicit part and manifest message kinds, with a format version, event ID, part index, part digest and total part count. All messages for one event use its existing organization/shard Kafka key. Store accepted parts in `audit.event_parts`, keyed by event ID and part index. Store incomplete metadata in `audit.event_assemblies`. A part's bytes are immutable. An identical retry succeeds; conflicting bytes require invalid-event handling.

For each received part or manifest, commit its durable storage and `audit.consumer_offsets` together. The consumer may then advance the broker offset, even if the assembly is incomplete. This does not claim that the canonical event exists. Once all required parts exist, mark the assembly ready in that transaction. A bounded completion worker verifies the ordered digest and reconstructs event fields. It then claims the event ID, appends the canonical row and removes the ready marker in one transaction. Preserve permanent parts referenced by the canonical row; delete only transient assembly state. A crash repeats completion safely through the existing permanent event-ID claim.

Before accepting a replayed part, check permanent event identity. If the canonical event already exists, verify the matching part digest and advance the delivery offset without creating another assembly. Missing-part assemblies remain durable and raise a delivery alert. They do not block unrelated complete events. No audit evidence expires solely because an assembly has not completed.

## Ledger serialization

Add the complete typed target to `context.target` and preserve changed values in `delta`. Targets support node identity, relationship source/type/target, resource address and external resource identity. Derive searchable columns from the same serialized event.

Add an audit format version within hashed context. Upgrade consumers, readers and exports before new writers. Preserve omission of new fields for historical rows. Preserve protected personal-data storage and references; do not copy protected actor data into permanent context.

Validate required fields by operation class. An omitted outcome must not become success. Existing invalid-event handling retains malformed events for repair. Hash verification and export round trips must preserve and verify targets, manifests, outcomes and changed values.

## External operations

Save the exact migration range and migration-file digests, backup manifest key, or engine generation and index identities before the effect. Record observed step results. Recover from migration state, backup manifests or engine state. Do not manufacture old timestamps or repeat irreversible work merely because its result event is missing.

A scheduled job identifies its service and run. A job initiated by a person or agent also retains that initiating identity. Child steps retain the enclosing operation ID. Audit-delivery bookkeeping does not recursively produce application audit events.

## First-time setup boundary

The existing first-time setup rule remains outside this runtime correction. Ordinary runtime commands must not use that exception when authentication or audit storage fails. This work adds no setup journal, storage prerequisite or service-creation workflow.

## Retention and scaling

Preserve the existing retention policy. This correction does not add automatic deletion of permanent history. Deleting delivery receipts after confirmed publication does not delete ledger evidence.

Measure bytes per final event, manifest part and pending receipt, including indexes and replication. Measure writes, broker bytes, delivery lag and recovery under increasing concurrency. Compare one, two and four application/worker instances with the same event mix. Report throughput and latency; fewer rows alone do not prove proportional scaling.

The architecture must distribute independent operations without a global application lock. Separately size each installation for traffic, retention, copies, replication, backups and outage backlog. Current machine limits must not silently reduce audit coverage or define the architecture.

## Acceptance

Use the existing Go test harness and real temporary dependencies. Execute each registered operation and inspect its permanent audit evidence.

1. Create, edit, replace, move and delete records. Compare audited values with actual source and committed values, including relationships.
2. Execute seed, repair, backfill, account, token and membership operations. A required-event failure must prevent the corresponding transaction from committing.
3. Execute gets, lists, searches, prompts, protocol, health and diagnostic reads. Verify actor, selection, targets, digest and outcome. Recording failure must prevent response release.
4. Exercise failed authentication, malformed input, denied access, absent targets and backend errors. Verify safe typed reasons and verified versus unauthenticated identity.
5. Race finalization and recovery from two processes. Resume a stale owner and verify one terminal event plus refusal of its response.
6. Interrupt delivery after Kafka acknowledgement and before queue deletion. Verify permanent deduplication after restart. Verify digest-based recovery after an uncertain finalization commit.
7. Exercise large old/new values and identity manifests across part boundaries. Interrupt multipart delivery. Verify complete reconstruction, bounded memory and rejection of altered or absent parts.
8. Export new and historical formats. Alter a target, delta or manifest and require verification failure. Verify protected personal-data handling.
9. Verify that failed authentication or unavailable audit storage cannot enable the first-time setup exception for an ordinary runtime command.
10. Run the scaling measurements and separately report installation capacity. Mapping-only tests do not satisfy these checks.

## References

- [Audit defect](https://tack.home.goodkind.io/browse/TACK-583)
- [Operation declarations](../../../internal/audit/ops.go)
- [SQL outbox](../../../internal/audit/outbox_yb.go)
- [FoundationDB transactional outbox](../../../internal/adapters/foundationdb/ops_outbox_intent.go)
- [Relay](../../../internal/audit/relay.go)
- [Event identity and chain append](../../../internal/audit/chain_append.go)
- [Provisioning](../../../internal/ops/provision.go)
- [Bootstrap exception](../../../internal/clispec/audit_infrastructure.go)
- [Settled operator bootstrap design](../2026-06-08-audit-ops-commands.md)

## Review status

The recording contracts, complete values, shared definitions, one final event per bounded read and delivery order reflect approved decisions. Queue settings, fingerprint encoding and stream/manifest handling are agent-selected recommendations. The builder check's multipart-offset question is resolved by the durable assembly transactions above. Bootstrap follows the existing first-boot exception. Implementation planning must preserve these choices and specify file boundaries, signatures and exact verification commands.
