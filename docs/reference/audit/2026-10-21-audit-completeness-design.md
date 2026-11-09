# Complete audit recording

Tack must record every application operation and committed change in its existing compliance ledger. Each event must establish:

- The verified actor or explicit unauthenticated identity.
- The operation.
- The complete target.
- The original time.
- The committed order.
- The outcome.

Change evidence must preserve actual old and new values. Read evidence must establish the request and returned identities.

These requirements apply after required services exist. They cover HTTP and MCP operations, operator commands, database writers, background jobs, provider hooks, and Tack commands that deployment invokes. They include refusals, failures, partial work, and interruptions. Initial service creation and surrounding container orchestration use their separate provisioning contract.

Implementation must follow this order:

1. Tack must complete audit recording.
2. Tack must implement repository-triggered QA and production deployment, with validation before merge.
3. Tack must apply production access restrictions.

User-facing history must remain separate. Tack must preserve historical ledger rows and chains. Tack must not invent missing historical values or introduce another permanent history store. Installation capacity must not reduce these guarantees.

Tack must satisfy the [recording lifecycle and format requirements](recording-lifecycle.md) and [recording acceptance and activation requirements](recording-acceptance.md) in addition to the operation requirements below.

## Recording by operation class

Each operation class requires durable evidence.

Operation evidence:

| Class | Required evidence |
| --- | --- |
| Database change | The database writer must commit actual changes and pending audit events in the same transaction. Missing audit context must prevent the write. |
| Deletion or move | The database writer must record complete deleted values and actual old and new moved values in each bounded transaction. The writer must preserve the original actor and enclosing operation. |
| Read | The read executor must save durable admission before reading and finalize evidence before releasing the result. The delivery system must deliver the final event to the ledger asynchronously. |
| External operation | The external executor must save intent before effects and record observed steps and results. Recovery must inspect actual state before repeating an effect. |
| SQL authentication-row change | The SQL writer must use `WriteOutboxTx` in the change transaction. The authentication repositories write encoded events through `WriteEncodedOutboxTx` in the change transaction. The SQL writer must record a failed attempt separately after rollback. |

Product reads require FoundationDB delivery state. Operator reads require YugabyteDB delivery state. The contract requires two durable local writes, admission and finalization, and one immutable final event per bounded request. A stream must finalize each page or chunk before release and link its event to the enclosing operation. Finalization must not wait for Kafka or permanent ledger projection.

For an MCP read spanning several organizations, the dispatcher currently creates an admission and distinct event ID for each organization under one operation ID. It runs the read once. Each final event uses the shared response digest and includes only that organization's returned targets and count. The dispatcher releases the result only after every admission finalizes. This queues several events for permanent recording for a single bounded request and differs from the required count.

## Shared operation definitions

Each registration must supply the following through `audit.Spec`:

- A stable operation code.
- The `read`, `database_change`, or `external` class.
- Typed target extraction.
- Safe input selection.

A registration without input must explicitly define its empty input and target behavior. Generated tools inherit their family definition. Product type and property names must not select handwritten exceptions. Missing definitions must fail registration. A read definition must not authorize a mutation.

The executor must supply:

- Identity.
- The operation ID.
- The event ID.
- Timing.
- The outcome.

Writers must derive actual changes inside their committing transactions. Persistent jobs must derive stable change IDs from the enclosing operation, persistent step, and record identity. Transaction retries must reuse their allocated event IDs. Event context distinguishes intent, result, change, and read phases and records the parent operation and completion time when applicable.

Terminal outcomes distinguish completed, refused, failed, partially completed, and incomplete work. Failed means that the operation did not commit effects. Partial completion identifies committed steps. Incomplete means that available evidence cannot establish a terminal result. Recording must preserve operation failure and recording failure separately. If response rendering or transport fails after a mutation commits, the executor must preserve the change event and record a separate result event under the same operation ID. The result must identify the failure, the partial outcome, and the committed database step.

## Authentication and early refusals

An outer recorder must start before authentication and dispatch. The outer recorder must cover:

- Malformed requests.
- Unsupported methods.
- Unknown tools.
- Prompts.
- Resources.
- Protocol metadata.
- Health checks.
- Diagnostics.

Successful authentication supplies the verified identity. Earlier failures use an explicit unauthenticated actor and the system audit organization.

Early evidence must include:

- The method.
- The route.
- The operation.
- Safe typed reasons.

Early evidence must exclude:

- Credentials.
- Raw request bodies.
- Untrusted headers.
- Raw database errors.

Authentication and business events share an operation ID while retaining distinct outcomes. The outer recorder must not duplicate the mutation's atomic success event.

After authentication, an MCP operation that uses system data selects the system organization. A read spanning the caller's organizations uses the membership information from authentication or looks it up when absent. For tools scoped by arguments, the handler resolves the workspace or node organization before admitting the read. This authorization lookup must not return product data to the client. A backend failure requires a system-organization refusal event with the verified actor, failed outcome, and `organization_unavailable` reason, followed by HTTP 503 without product data. A caller without memberships and a target in another organization are authorization cases rather than backend lookup failures.

## Complete changes and protected values

Each changed record must produce an event under the enclosing operation ID. Writers must read actual old values inside the transaction rather than infer them from a request or prior plan. Change evidence must cover:

- Additions.
- Replacements.
- Removals.
- Implicit parent changes.
- Relationships.
- Subtree steps.
- Metadata.
- Seeds.
- Repairs.
- Backfills.

Relationship targets must include source, relationship type, and target.

Node and relationship writers must preserve existing mutation plans, source checks, schema checks, and receipts. A required-event failure must prevent the change transaction from committing. A cascade or resumed job must preserve the initiating actor and enclosing operation.

`ops repair reference-uniqueness` also attempts to record the applied renames, counter seeds, and new key claims from its report after execution, including partial work. On a partial run it includes only keys that changed hands on nodes actually written. It derives an event ID from each change and writes it only if absent. A repeated report therefore does not duplicate those events. This post-run record cannot by itself satisfy the requirement to commit change evidence with the changed data.

The repair uses a 30-second recording deadline independent of request cancellation. If recording fails, it logs every applied identity in groups of up to 50 and returns the recording error together with any repair error. The `--fail-after-renames` test option requires `TACK_DATAGEN_ALLOW_TARGET` to be `qa` or `local`.

SQL repositories must read old values, apply changes, save audit events, and commit in one transaction. Token evidence must record identity, owner, and permissions without token bytes, hashes, or passwords. Membership evidence must record actual old and new roles. Accepted-token events may update last-use projections without recursive audit events.

Protected values use opaque references in hashed context and deltas. Separate protected storage preserves actor and subject ownership and permits redaction without rewriting the chain. Permanent context, exports, and manifests must not duplicate protected actor values. Multipart completion must delete transient actor and change copies after canonical storage. Redaction of protected storage does not establish deletion from Kafka, dead letters, or pending transport records.

## External effects and first-time setup

External execution must use a durable outbox rather than a recorder that can buffer only in memory. External intent must identify the planned effect with its applicable evidence:

- The exact migration range and migration-file digests.
- The deployment image digest.
- The backup manifest.
- The engine generation and index identities.
- Role or privilege changes.
- Another planned effect.

Each step records observed results and its enclosing operation. Stable parent and step IDs identify retried effects. Recovery must inspect migration state, backup manifests, or engine state before repeating effects. A missing result event must not invent an old timestamp or justify repeating irreversible work.

`ops audit seed-roles --execute` treats login-role changes as external effects. It refuses missing role passwords before changing any roles. Before executing the role changes, it reads their existence, login attributes, and audit base-role memberships, then records an intended step for each role. After running the changes it rereads those attributes and memberships. If the reread fails, the recorded command path reports an incomplete outcome.

For each role, the command records the observed before and after state. A successful run or a visible role change gives that step a completed outcome. If the run fails without a visible change to a role, its step is incomplete because the observations cannot establish whether its password changed. The evidence includes a password-set flag and excludes the password itself. The seed assigns each audit and application login its designated base role and revokes other audit base roles. It prepares the migration login separately.

Scheduled jobs must identify the service and unique run. Human- or agent-initiated jobs must also preserve the initiating identity. Audit delivery bookkeeping must not recursively create application audit events. Provider refreshes and previews that query state must use the same read lifecycle. A preview that only prints actions does not perform a state read.

Initial setup may defer evidence only while required audit infrastructure does not exist. Ordinary runtime authentication or recording failures must not enable that exception. This contract does not add a setup journal, storage prerequisite, replacement deployment command, or service-creation workflow. Recording a child Tack command does not prove that the parent Configs controller recorded its own effects.

## Retention and scale

Permanent history must not expire automatically. Published delivery-receipt cleanup does not delete permanent evidence. Independent operations must distribute without a global application lock. Installation sizing must account for the following without weakening coverage:

- Traffic.
- Retention.
- Copies.
- Indexes.
- Replication.
- Backups.
- Outage backlog.

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
