# Complete audit recording

Tack must record person-callable MCP tools, public APIs, and operator commands in its existing audit ledger. This includes calls from agents and API tokens. Records establish the actor, action, target and time of each access attempt. Changes preserve actual old and new values. Tack records authentication and authorization decisions separately.

This draft resolves engineering choices for the audit correction tracked by TACK-583. The source review used `d5b38f8a`.

## Scope

Cover person-callable operations after the required services exist, including Tack commands invoked during deployment. Record user-requested work under its original operation when a job completes it. Do not create separate compliance events for autonomous jobs, internal service calls, indexing, retries, polling, health checks, diagnostics, or protocol discovery. Use operational logs for internal failures. Record security refusals. Creating services from an empty installation and changing the surrounding container orchestration are outside this correction. Execute the reviewed Tack runtime operation inventory during acceptance; declarations alone do not prove coverage.

Complete audit recording first. Implement repository-triggered QA and production deployment second, with validation before merge. Complete production access restrictions third. User-facing history remains separate. Preserve existing ledger rows and hash chains. Do not reconstruct missing historical values or create a second permanent history store. Current installation capacity affects deployment sizing rather than product architecture.

## Approved recording contracts

| Operation | Required recording |
|---|---|
| A database write changes records. | Commit actual changes and pending audit events in the same transaction. Missing audit context prevents the write. |
| A deletion removes nodes or relationships or moves children. | Record complete deleted values and actual old/new moved values in each bounded transaction. Identify the original actor and enclosing operation. |
| An operator or user reads data. | Commit an access attempt with the safe selection before execution. Refuse the read if the outbox cannot store the attempt. |
| An external operation changes another system. | Record the operator command and its step targets before execution. Check actual state before repeating an effect. |
| A command changes SQL records. | Use `WriteOutboxTx` in the change transaction. |

Product requests store their access attempts in the FoundationDB outbox. Operator commands use the YugabyteDB operator outbox. The relay delivers each event to the permanent ledger.

## Shared operation definition

Extend `audit.Spec`. Each registration supplies a stable operation code, a read/database-change/external-operation class, a typed target extractor and safe input selection. Generated tools inherit their family definition. Node types and property names do not select handwritten audit rules.

The executor supplies identity, operation ID, event ID, timing and the evidence for the operation class. Writers derive actual changes inside their transactions. Registration rejects missing definitions. A read declaration cannot authorize a mutation.

Security decisions and results of user-requested jobs can have terminal outcomes. These are completed, refused, failed, partially completed and incomplete. Failed means that the operation did not commit effects. Partial completion identifies committed steps. Incomplete means that the available evidence cannot establish a terminal result. Preserve operation failure and audit-recording failure separately.

An outer recorder starts before authentication and dispatch. It covers malformed requests, unsupported methods, unknown tools, prompts, and resource reads. Successful authentication supplies a verified identity. Earlier failures use an unauthenticated actor and the existing system audit organization. Record method, route, operation and safe reason fields. Exclude credentials, raw request bodies and raw database errors. Link authentication and business events by operation ID without conflating their outcomes.

After authentication, the recorder reads the caller's organization memberships and, for a tool call, the organization of the named workspace or node. This lookup runs before the read admission because it selects the organization of each admission. The lookup is an authorization step and returns no product data to the client. When the lookup fails, the recorder stores the access attempt in the system organization with the verified actor and answers HTTP 503. A caller without memberships and a target in another organization do not cause a lookup failure.

## Historical read evidence

Earlier writers admitted a read into pending state and finalized its event with result identities and a payload digest. Keep recovery for existing pending admissions and delivery receipts. The consumer, export and verifier must preserve the original event bytes and ordered manifest parts. New access attempts do not create pending admissions or result manifests.

## Change evidence and delivery

Changes produce one event per changed record under the same operation ID. Large event payloads use the same bounded part mechanism; the transaction commits their manifest and parts with the change. This handles complete old/new values that exceed one FoundationDB value. The relay publishes parts before the event manifest. Retain incomplete assemblies durably and retry missing parts; do not commit a ledger event with absent parts.

Part delivery uses explicit part and manifest message kinds, with a format version, event ID, part index, part digest and total part count. All messages for one event use its existing organization/shard Kafka key. Store incoming transport parts in `audit.event_assembly_parts`, keyed by event ID and part index, and incomplete metadata in `audit.event_assemblies`. Only the consumer may read these transient tables. A part's bytes are immutable. An identical retry succeeds; conflicting bytes require invalid-event handling.

For each received part or manifest, commit its durable storage and `audit.consumer_offsets` together. The consumer may then advance the broker offset, even if the assembly is incomplete. This does not claim that the canonical event exists. Once all required parts exist, mark the assembly ready in that transaction. A bounded completion worker verifies the ordered digest and reconstructs event fields. It then claims the event ID, separates actor personal data, appends the canonical row and removes transient assembly state in one transaction. Save permanent read-identity manifest parts in `audit.event_parts` during that transaction. Store the complete change delta only in the canonical row. Do not retain transport copies of actor personal data or change deltas. A crash repeats completion safely through the existing permanent event-ID claim.

Before accepting a replayed part, check permanent event identity and the stored completed transport digest. If the canonical event already exists, verify that identity and digest and advance the delivery offset without creating another assembly. Missing-part assemblies remain durable and raise a delivery alert. They do not block unrelated complete events. No audit evidence expires solely because an assembly has not completed.

## Ledger serialization

Add the complete typed target to `context.target` and preserve changed values in `delta`. Targets support node identity, relationship source/type/target, resource address and external resource identity. Derive searchable columns from the same serialized event.

Add an audit format version within hashed context. Upgrade consumers, readers and exports before new writers. Preserve omission of new fields for historical rows. Preserve protected personal-data storage and references; do not copy protected actor data into permanent context.

Validate required fields by operation class. An omitted outcome must not become success. Existing invalid-event handling retains malformed events for repair. Hash verification and export round trips must preserve and verify targets, manifests, outcomes and changed values.

## External operations

Save the exact migration range and migration-file digests, backup manifest key, or engine generation and index identities before the effect. Recover from migration state, backup manifests or engine state. Do not manufacture old timestamps or repeat irreversible work merely because its result event is missing.

Work initiated by a person or agent retains the original identity, operation, user-data changes, and final outcome across job execution and retries. Internal implementation steps do not create separate user actions.

Finish pending search rebuilds and access rollouts on the previous release before upgrading. Older records contain actor and operation IDs without the complete initiating event. The new completion writer refuses those records because it cannot recover the original actor kind and command evidence from IDs alone.

## First-time setup boundary

The existing first-time setup rule remains outside this runtime correction. Ordinary runtime commands must not use that exception when authentication or audit storage fails. This work adds no setup journal, storage prerequisite or service-creation workflow.

## Retention and scaling

Preserve the existing retention policy. This correction does not add automatic deletion of permanent history. Deleting delivery receipts after confirmed publication does not delete ledger evidence.

Measure bytes per access event, change event and transport part, including indexes and replication. Measure writes, broker bytes, delivery lag and recovery under increasing concurrency. Compare one, two and four application/worker instances with the same event mix. Report throughput and latency; fewer rows alone do not prove proportional scaling.

The architecture must distribute independent operations without a global application lock. Separately size each installation for traffic, retention, copies, replication, backups and outage backlog. Current machine limits must not silently reduce audit coverage or define the architecture.

## Acceptance

Use the existing Go test harness and real temporary dependencies. Execute each registered operation and inspect its permanent audit evidence.

1. Create, edit, replace, move and delete records. Compare audited values with actual source and committed values, including relationships.
2. Execute seed, repair, backfill, account, token and membership operations. A required-event failure must prevent the corresponding transaction from committing.
3. Execute person-callable gets, lists, searches, prompts, and resource reads. Verify the actor, safe selection, target and attempt time. Failure to record the attempt must prevent execution.
4. Exercise failed authentication, malformed input, denied access, absent targets and backend errors. Verify security decisions, the verified or unauthenticated identity and ordinary access attempts.
5. Recover an existing pending read and verify its terminal event. New reads must not create pending state.
6. Interrupt delivery after Kafka acknowledgement and before queue deletion. Verify permanent deduplication after restart. Verify digest-based recovery after an uncertain finalization commit.
7. Exercise large old/new values and identity manifests across part boundaries. Interrupt multipart delivery. Verify complete reconstruction, bounded memory and rejection of altered or absent parts.
8. Export new and historical formats. Alter a target, delta or manifest and require verification failure. Verify protected personal-data handling.
9. Verify that failed authentication or unavailable audit storage cannot enable the first-time setup exception for an ordinary runtime command.
10. Verify that autonomous jobs, indexing, internal service calls, health checks, diagnostics, and discovery do not create compliance events. Run the scaling measurements and separately report installation capacity. Mapping-only tests do not satisfy these checks.

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
