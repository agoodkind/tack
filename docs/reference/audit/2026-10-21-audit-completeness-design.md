# Complete audit recording

Tack must record every application operation and committed change in its existing compliance ledger. Each event must establish the verified actor or explicit unauthenticated identity, operation, complete target, original time, committed order, and outcome. Change evidence must preserve actual old and new values. Read evidence must establish the request and returned identities.

These requirements apply after required services exist. They cover HTTP and MCP operations, operator commands, database writers, background jobs, provider hooks, and Tack commands that deployment invokes. They include refusals, failures, partial work, and interruptions. Initial service creation and surrounding container orchestration use their separate provisioning contract.

Complete audit recording first. Implement repository-triggered QA and production deployment second, with validation before merge. Apply production access restrictions third. User-facing history remains separate. Preserve historical ledger rows and chains. Do not invent missing historical values or introduce another permanent history store. Installation capacity must not reduce these guarantees.

## Recording by operation class

Each operation class requires durable evidence:

| Class | Required evidence |
| --- | --- |
| Database change | Commit actual changes and pending audit events in the same transaction. Missing audit context must prevent the write. |
| Deletion or move | Record complete deleted values and actual old and new moved values in each bounded transaction. Preserve the original actor and enclosing operation. |
| Read | Save durable admission before reading. Finalize evidence before releasing the result. Deliver the final event to the ledger asynchronously. |
| External operation | Save intent before effects. Record observed steps and results. Recovery must inspect actual state before repeating an effect. |
| SQL authentication-row change | Use `WriteOutboxTx` in the change transaction. The authentication repositories write encoded events through `WriteEncodedOutboxTx` in the change transaction. Record a failed attempt separately after rollback. |

Product reads require FoundationDB delivery state. Operator reads require YugabyteDB delivery state. The contract requires two durable local writes, admission and finalization, and one immutable final event per bounded request. A stream must finalize each page or chunk before release and link its event to the enclosing operation. Finalization must not wait for Kafka or permanent ledger projection.

For an MCP read spanning several organizations, the dispatcher currently creates an admission and distinct event ID for each organization under one operation ID. It runs the read once. Each final event uses the shared response digest and includes only that organization's returned targets and count. The dispatcher releases the result only after every admission finalizes. This queues several events for permanent recording for a single bounded request and differs from the required count.

## Shared operation definitions

Each registration must supply a stable operation code, the `read`, `database_change`, or `external` class, typed target extraction, and safe input selection through `audit.Spec`. A registration without input must explicitly define its empty input and target behavior. Generated tools inherit their family definition. Product type and property names must not select handwritten exceptions. Missing definitions must fail registration. A read definition must not authorize a mutation.

The executor must supply identity, operation ID, event ID, timing, and outcome. Writers must derive actual changes inside their committing transactions. Persistent jobs must derive stable change IDs from the enclosing operation, persistent step, and record identity. Transaction retries must reuse their allocated event IDs. Event context distinguishes intent, result, change, and read phases and records the parent operation and completion time when applicable.

Terminal outcomes distinguish completed, refused, failed, partially completed, and incomplete work. Failed means that the operation did not commit effects. Partial completion identifies committed steps. Incomplete means that available evidence cannot establish a terminal result. Recording must preserve operation failure and recording failure separately. If response rendering or transport fails after a mutation commits, preserve the change event and record a separate result event under the same operation ID. The result must identify the failure, the partial outcome, and the committed database step.

## Authentication and early refusals

An outer recorder must start before authentication and dispatch. It covers malformed requests, unsupported methods, unknown tools, prompts, resources, protocol metadata, health checks, and diagnostics. Successful authentication supplies the verified identity. Earlier failures use an explicit unauthenticated actor and the system audit organization.

Early evidence must include method, route, operation, and safe typed reasons. It must exclude credentials, raw request bodies, untrusted headers, and raw database errors. Authentication and business events share an operation ID while retaining distinct outcomes. The outer recorder must not duplicate the mutation's atomic success event.

After authentication, an MCP operation that uses system data selects the system organization. A read spanning the caller's organizations uses the membership information from authentication or looks it up when absent. For tools scoped by arguments, the handler resolves the workspace or node organization before admitting the read. This authorization lookup must not return product data to the client. A backend failure requires a system-organization refusal event with the verified actor, failed outcome, and `organization_unavailable` reason, followed by HTTP 503 without product data. A caller without memberships and a target in another organization are authorization cases rather than backend lookup failures.

## Read ownership and recovery

Each durable read admission identifies an owner token and a generation number. That number identifies the current period of ownership: recovery changes it to prevent a stale owner from releasing a result. When finalization or recovery ends the read, a terminal receipt stores its outcome and digest. The receipt lets a retry determine whether the same completion already committed.

Both storage adapters implement admission, renewal, finalization, expired-owner recovery, publication acknowledgment, and published-receipt cleanup. The relay can publish a read completion after finalization writes it to the outbox. Pending admissions are not outbox entries.

Each transition requires one transaction:

| Transition | Required behavior |
| --- | --- |
| Begin | Create the event template, random owner token, generation, deadline, and expiry index. Accept an existing identity only for the same admission retry. |
| Renew | Compare owner and generation. Replace the lease deadline and expiry index together. |
| Finalize | Compare owner and generation. Save the complete event and parts in the outbox and its digest in a terminal receipt. Remove pending state and its expiry index. |
| Recover | Reread the expired candidate and compare generation and indexed deadline. Save an incomplete event and receipt, then remove pending state. Do not rerun the read. |
| Acknowledge | After Kafka acknowledges the exact event, delete the outbox entry and mark its matching receipt published together. |
| Clean up | Delete published receipts after their retry window. Do not delete active evidence solely because it is old. |

FoundationDB stores pending, expiry, receipt, and cleanup records as tuple keys and uses its injected clock for recovery eligibility. Finalization must write the versionstamped outbox record in the same transaction as the terminal receipt; a standalone append that commits separately cannot satisfy this rule. YugabyteDB uses transient lifecycle state beside the operator outbox, database time, conditional updates, and indexes for expired work and published receipts. Narrow lifecycle functions permit application execution without direct ledger modification rights.

Transaction comparisons decide ownership. Time establishes recovery eligibility. Clock differences can cause early recovery, but a stale owner must not finalize after another owner changes the generation. A handler that loses finalization must discard its result and return a recording error.

The implementation uses bounded lifecycle settings:

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

| Subject | Requirement |
| --- | --- |
| Size | Each part contains up to 256 ordered identities in at most 64 KiB of encoded data. |
| Event evidence | The canonical event records the total identity count and a digest of the ordered manifest. The separate result fingerprint covers the serialized result payload. |
| Permanent part | Store each part's identities and the digest of its encoded bytes in `audit.event_parts`. Parts do not store an individual object fingerprint or a copy of a returned object. |
| Completeness | Preserve all parts in storage and transport. The consumer must verify the complete manifest before committing the event. Exports and backups must include the permanent parts. |
| Organization access | Only the audit reader and export interfaces may expose permanent parts. `Reader.ManifestParts` filters by organization ID and event ID and returns parts in index order. The exporter supplies both IDs from the event row. |

The `audit_reader` database role can select permanent parts for every organization. The reader query enforces the organization boundary. The row-level policies for transient assembly tables grant access only to `audit_writer`.

## Complete changes and protected values

Each changed record must produce an event under the enclosing operation ID. Writers must read actual old values inside the transaction rather than infer them from a request or prior plan. Changes include additions, replacements, removals, implicit parent changes, relationships, subtree steps, metadata, seeds, repairs, and backfills. Relationship targets must include source, relationship type, and target.

Node and relationship writers must preserve existing mutation plans, source checks, schema checks, and receipts. A required-event failure must prevent the change transaction from committing. A cascade or resumed job must preserve the initiating actor and enclosing operation.

`ops repair reference-uniqueness` also attempts to record the applied renames, counter seeds, and new key claims from its report after execution, including partial work. On a partial run it includes only keys that changed hands on nodes actually written. It derives an event ID from each change and writes it only if absent. A repeated report therefore does not duplicate those events. This post-run record cannot by itself satisfy the requirement to commit change evidence with the changed data.

The repair uses a 30-second recording deadline independent of request cancellation. If recording fails, it logs every applied identity in groups of up to 50 and returns the recording error together with any repair error. The `--fail-after-renames` test option requires `TACK_DATAGEN_ALLOW_TARGET` to be `qa` or `local`.

SQL repositories must read old values, apply changes, save audit events, and commit in one transaction. Token evidence must record identity, owner, and permissions without token bytes, hashes, or passwords. Membership evidence must record actual old and new roles. Accepted-token events may update last-use projections without recursive audit events.

Protected values use opaque references in hashed context and deltas. Separate protected storage preserves actor and subject ownership and permits redaction without rewriting the chain. Permanent context, exports, and manifests must not duplicate protected actor values. Multipart completion must delete transient actor and change copies after canonical storage. Redaction of protected storage does not establish deletion from Kafka, dead letters, or pending transport records.

## Multipart transport

When encoding a transport envelope for any operation class, `EncodeEvent` splits the serialized event envelope into transport parts with at most 64 KiB of payload each. The limit applies before the encoder adds transport message fields. The change transaction must commit the manifest and every part with the changed data, including old and new values larger than one FoundationDB value. The relay publishes parts before the manifest and retains them until acknowledgment. Incomplete assemblies must remain durable; the consumer must not commit a canonical event with absent parts.

Each message identifies its kind (part or manifest) and format version. Every publication and replay of a message for the same event must use the event's original organization and shard as its Kafka key. SHA-256 digests use lowercase hexadecimal.

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

Complete events serialize typed targets in `context.target` and actual changes in `delta`. Targets support node identity, relationship identity, resource address, and external resource identity. Searchable columns must derive from that serialized event.

The format version forms part of hashed context. Historical rows must preserve omission of new fields, historical outcome strings, and original hash semantics. Format 2 requires class-specific evidence and explicit terminal outcomes. An omitted outcome must not become success. Only external intent may use pending; pending read state must not enter the ledger. Invalid events must remain available for repair through existing invalid-event handling.

The permanent ledger must remain `audit.events`, and only the audit consumer may write it. Recovery must preserve `audit.chain_heads` and continue every organization/shard chain. Hash and signed export verification must detect alterations to targets, deltas, outcomes, and manifests for both historical and complete formats.

## External effects and first-time setup

External execution must use a durable outbox rather than a recorder that can buffer only in memory. External intent must identify the exact migration range and migration-file digests, deployment image digest, backup manifest, engine generation and index identities, role or privilege changes, or other planned effect. Each step records observed results and its enclosing operation. Stable parent and step IDs identify retried effects. Recovery must inspect migration state, backup manifests, or engine state before repeating effects. A missing result event must not invent an old timestamp or justify repeating irreversible work.

`ops audit seed-roles --execute` treats login-role changes as external effects. It refuses missing role passwords before changing any roles. Before executing the role changes, it reads their existence, login attributes, and audit base-role memberships, then records an intended step for each role. After running the changes it rereads those attributes and memberships. If the reread fails, the recorded command path reports an incomplete outcome.

For each role, the command records the observed before and after state. A successful run or a visible role change gives that step a completed outcome. If the run fails without a visible change to a role, its step is incomplete because the observations cannot establish whether its password changed. The evidence includes a password-set flag and excludes the password itself. The seed assigns each audit and application login its designated base role and revokes other audit base roles. It prepares the migration login separately.

Scheduled jobs must identify the service and unique run. Human- or agent-initiated jobs must also preserve the initiating identity. Audit delivery bookkeeping must not recursively create application audit events. Provider refreshes and previews that query state must use the same read lifecycle. A preview that only prints actions does not perform a state read.

Initial setup may defer evidence only while required audit infrastructure does not exist. Ordinary runtime authentication or recording failures must not enable that exception. This contract does not add a setup journal, storage prerequisite, replacement deployment command, or service-creation workflow. Recording a child Tack command does not prove that the parent Configs controller recorded its own effects.

## Retention and scale

Permanent history must not expire automatically. Published delivery-receipt cleanup does not delete permanent evidence. Independent operations must distribute without a global application lock. Installation sizing must account for traffic, retention, copies, indexes, replication, backups, and outage backlog without weakening coverage.

Scale evidence must compare identical event mixes under increasing concurrency. Required instance counts:

| Application instances | Worker instances |
| --- | --- |
| 1 | 1 |
| 2 | 2 |
| 4 | 4 |

Measurements must include:

- Throughput.
- Response latency.
- Backlog.
- Recovery time.
- Writes.
- Broker bytes.
- Bytes per event, including indexes and replicas.
- Bytes per manifest part, including indexes and replicas.
- Bytes per pending receipt, including indexes and replicas.

Fewer rows alone do not prove proportional scaling.

## Acceptance evidence

Acceptance must use the existing Go harness and real temporary dependencies through actual registered public operations. Production data and credentials must not supply fixtures. A tool-to-verb check resolves a list of tool names. A separate source check looks for references to declared verbs. Neither check executes the handlers. Declarations, log messages, helper-only tests, mapping checks, skipped integration suites, and unavailable databases do not prove accepted behavior. Provider operations require the existing provider acceptance environment.

1. Create, edit, replace, move, and delete records. Compare permanent evidence with source and committed values, including relationships and removals.
2. Run seed, repair, backfill, account, token, and membership operations. Require event failure to prevent the corresponding transaction from committing.
3. Run gets, lists, searches, prompts, protocol, health, diagnostic, operator, and provider reads. Verify actor, selection, complete targets, exact payload digest, and outcome. Recording failure must prevent result release.
4. Exercise failed authentication, malformed input, denied access, absent targets, and backend errors. Verify safe typed reasons and verified versus unauthenticated identity.
5. Race finalization and recovery in two processes. Resume the stale owner and require one terminal event plus refusal of the stale response. Check admission retry expiry and published-receipt cleanup.
6. Interrupt delivery after Kafka acknowledgment and before queue deletion. Require permanent deduplication after restart and digest-based confirmation after uncertain finalization commits. On disposable QA, exercise the application recorder during a broker outage and restore Kafka. Compare the spilled event IDs and original times with the permanent rows after relay delivery. Reconcile the recovery count with the events stored during that outage.
7. Exercise large old and new values, read manifests, and interruption across part boundaries. Require complete reconstruction, bounded memory, and rejection of missing, reordered, conflicting, or altered parts.
8. Export both formats. Alter targets, deltas, and manifests and require verification failure. Verify protected-value separation and redaction without breaking chain verification. Submit a format 2 event without an outcome through the real outbox, relay, and consumer. Require the ledger to reject it and the dead-letter reader to return its payload and a missing-outcome error.
9. Verify that ordinary runtime authentication and recording failures cannot enable the setup exception. Force a response failure after a mutation commits. Verify both the permanent change event and the separate partial result under the same operation ID, including the failed request and the committed database step. Exercise external recovery and background attribution.
10. Execute every reviewed runtime operation and require permanent-evidence assertions. Missing executable coverage must fail the required coverage check. New audit verbs and user-visible paths require `ops qa datagen` coverage in the same change. Run the scale measurements and report installation capacity separately.

QA acceptance requires a permanent `mcp.resource_read` event for the seed's authenticated read of `tack://getting-started`. With `ops qa datagen seed --execute --commit` on an allowed target, the generator makes that read first with the first actor's bearer token from the first workspace. If the seed lacks that workspace or actor, it logs the reason and skips the read. Without `--execute`, the command previews the operation. With `--execute` alone, the dry-run driver returns a synthetic result without sending an MCP request.

## Compatible activation

Schemas, consumers, readers, and exports must support the new format before any writer enables it. Release A installs that support while old writers retain their format. Use deployment inventory and Docker Compose service labels to identify every consumer. Every consumer instance must pass deployment verification, and Release A must pass its QA and production checks, before Release B enables pending reads and complete writers. The mixed-version acceptance test must refuse activation with two consumers when one still runs the old version. Both releases can use the existing shared image-tag convention. Rollback must retain Release A or newer consumers and preserve queued new-format events.

QA must validate each complete group before production activation. Runtime audit coverage and applicable integration checks must pass before merge. The acceptance ticket must identify the deployed revisions and observed permanent evidence. These observations establish live acceptance; code, review verdicts, image availability, and historic run dates do not establish deployment or current behavior.
