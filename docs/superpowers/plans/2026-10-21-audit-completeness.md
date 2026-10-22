# Runtime audit completeness implementation plan

> Use `subagent-driven-development` to implement these tasks. Track completion with checkbox syntax.

**Goal:** Every runtime operation produces complete, durable audit evidence with accurate identity, targets, changes and outcomes.

**Architecture:** Extend the existing operation definitions, transactional outboxes, relay and consumer. Mutations commit evidence with their changes. Reads save pending state and then one final event before releasing results.

**Tech Stack:** Go, FoundationDB, YugabyteDB, Kafka, MCP, the existing container test runner and Tack OpenTofu provider.

**Spec:** Read the [runtime audit design](../specs/2026-10-21-audit-completeness-design.md) with this plan.

## Global constraints

- Limit changes to operations after required services exist. Preserve first-time service creation behavior.
- Preserve historical ledger rows, hash verification and protected actor-data references.
- Use FoundationDB pending state for product reads and YugabyteDB pending state for operator reads.
- Finalize read evidence durably before returning data. Ordinary requests do not wait for Kafka or ledger projection.
- Produce one final event per bounded read; link streaming chunk events to their enclosing operation.
- Apply definitions by operation family without product-type or property-name exceptions.
- Preserve permanent retention. Measure current installation capacity separately from architecture scaling.
- Do not add scripts, replacement test frameworks, storage services or another audit database.
- Use real temporary dependencies through the existing test harness. Exclude production data and credentials.
- Keep new files under 200 lines. Extract changed responsibilities from oversized files without unrelated restructuring.
- Use 64 recovery buckets, 32 candidates per scan, a 30-second lease, renewal every 10 seconds, a 30-second admission retry deadline, one-hour published receipt retention and cleanup batches of 256.
- Limit encoded parts to 64 KiB and read-identity parts to 256 entries. Preserve evidence through bounded parts without truncation.

## Review focus

- Finalization and recovery can race. Only one terminal event may commit, and the stale owner must not release data.
- A mutation can commit before response rendering fails. Preserve its change event and record the request failure separately.
- Publication can succeed before the relay loses its connection. Redelivery must retain identities and produce one canonical event.
- Multipart delivery can stop before completion. Commit received parts with consumer offsets and recover the remaining assembly.
- New JSON fields can invalidate historical hashes or duplicate protected actor data. Preserve historical omission rules and delete transient actor copies after canonical completion.

## Work order

One lead owns shared contracts, integration and deployment evidence. Task 1 precedes all writers. Task 1 defines the shared `audit.Change` type for Tasks 4 and 5. Task 2 defines the common encoded-event interface before Tasks 3 through 5 use it; its adapter implementations can proceed in parallel with Task 3 after that interface commit. Tasks 4 and 5 can run in parallel after Task 2. Task 6 depends on Tasks 1 through 3. Task 7 uses Tasks 1 through 5. Task 8 integrates the complete result. Independent implementers use separate worktrees.

Each task ends with a logical commit and focused passing checks. Install compatible schemas, consumers and readers before enabling new writers. Testbed reservations affect live validation only; they do not stop independent code and review work.

## Task 1: Preserve complete event data

Modify [event types](../../../internal/audit/recorder.go), [operation definitions](../../../internal/audit/ops.go), [stored rows](../../../internal/audit/row.go), [chain insertion](../../../internal/audit/chain_append.go) and [export reconstruction](../../../internal/audit/export_rowhash.go). Add `internal/audit/event_contract.go` and `internal/audit/event_validate.go`.

Define the following shared types and functions, including `Change` for Tasks 4 and 5, in `internal/audit/event_contract.go`:

```go
type OperationClass string
type Target struct {
    Kind, NodeType, Reference, Name, URI, RelationType string
    ID, SourceID, TargetID uuid.UUID
}
type ReadEvidence struct {
    Count uint64
    PayloadSHA256, PayloadFormat, ManifestSHA256 string
    ManifestParts uint32
}
type OutcomeDetails struct {
    Outcome Outcome
    FailureCode, FailureMessage string
    CommittedSteps []string
}
type Change struct {
    EventID uuid.UUID
    Target Target
    Before, After json.RawMessage
    Changed []string
}
func ValidateEvent(event Event) error
func ValidateSpec(spec Spec) error
```

Extend `Spec` with `Class OperationClass`, `Targets func(context.Context, json.RawMessage) ([]Target, error)` and `SafeInput func(json.RawMessage) (json.RawMessage, error)`. Use exact class values `read`, `database_change` and `external`. Generated registration supplies the typed-input adapters. A definition without input still supplies explicit empty-input and target behavior.

Extend `EventContext` with `AuditFormatVersion`, `OperationClass`, `Phase`, operation/parent operation IDs, start/completion times, `Target` and `Read`. Phases are `intent`, `result`, `change` and `read`. Use zero-omitting UUID/time tags and optional pointers. Version zero retains existing semantics; version two requires class-specific evidence. Keep historical outcome strings unchanged. New terminal outcomes are `completed`, `refused`, `failed`, `partially_completed` and `incomplete`. Permit `pending` only for external intent events; pending read state never enters the ledger. Add an unauthenticated actor kind without a false user identity.

- [ ] Implement one serializer and validator for typed targets, searchable columns, context and deltas. Preserve personal-data separation.
- [ ] Make consumers, readers and exports accept both formats without enabling new writers yet.
- [ ] Add `TestRuntimeAuditTargetRoundTrip` and `TestRuntimeAuditHistoricalHashCompatibility` through real consumer/reader/export boundaries. Assert complete relationship identities and unchanged historical hashes. Add `TestRuntimeAuditMissingOutcomeRejected` to write a version-two event without an outcome to the outbox, publish it to Kafka through the real relay and process it with the real audit consumer. Assert that `audit.events` does not contain a row for that event ID and that `audit.Reader.ListDeadLetters` returns one row from `audit.events_dlq` with that event's payload and an error that identifies the missing outcome.
- [ ] Run focused audit integration checks and commit compatible readers with the contract.

## Task 2: Deliver complete large events

Extend [FoundationDB outbox transactions](../../../internal/adapters/foundationdb/ops_outbox_intent.go), [SQL outbox writes](../../../internal/audit/outbox_yb.go), [relay](../../../internal/audit/relay.go) and [consumer](../../../internal/audit/consumer.go). Add `event_parts.go`, `consumer_parts.go` and `consumer_assembly.go` under `internal/audit`. Add `migrations/020_audit_event_parts.sql` for transient assemblies/parts, permanent read manifests and completed transport digests.

```go
type EventPart struct {
    EventID uuid.UUID
    Index, Count uint32
    Digest, TransportDigest string
    Payload []byte
}
type EncodedEvent struct {
    EventID uuid.UUID
    TransportDigest string
    Header []byte
    Parts []EventPart
}
func EncodeEvent(event Event, readTargets []Target) (EncodedEvent, error)
func VerifyEventParts(header []byte, parts []EventPart) (Event, []Target, error)
```

Use SHA-256 with lowercase hexadecimal output. Serialize a typed envelope containing `Event` and the ordered `ReadTargets` list. Part digests cover exact part bytes; the transport digest covers the ordered envelope bytes. Compute the target-manifest digest from the ordered target list and reject mismatched declared evidence. Header fields include format version, event ID, organization/shard key, part count and transport digest. Events that fit remain a single record. Multipart delivery preserves logical event identity. Mutation and external events pass an empty read-target list.

- [ ] Save all parts atomically with the corresponding change. Publish parts before their manifest and retain outbox records until Kafka acknowledges them.
- [ ] Commit received parts and consumer offsets together. Mark complete assemblies ready. Complete them through the permanent event-ID claim and canonical append transaction.
- [ ] Split actor data before canonical storage. Delete transient transport copies after completion. Retain only permanent read-identity manifests; store full deltas once in the ledger.
- [ ] Restrict transient assembly tables to the consumer. Apply organization isolation to permanent manifests and expose them only through the existing audit reader/export permissions.
- [ ] Extend export and verification to authenticate manifest roots and reject missing, reordered or altered parts.
- [ ] Add `TestRuntimeAuditLargeDeltaDelivery`, `TestRuntimeAuditPartCrashRecovery` and `TestRuntimeAuditPartConflict` using real databases and Kafka. Include a combined old/new record larger than one FoundationDB value. Assert one complete canonical event after interrupted delivery and redelivery.
- [ ] Run those checks and commit schema, relay and consumer support together.

## Task 3: Implement pending read state

Add `read_lifecycle.go`, `read_recovery.go` and `read_pending_yb.go` under `internal/audit`. Add `audit_read_pending.go`, `audit_read_finalize.go` and `audit_read_recovery.go` under `internal/adapters/foundationdb`. Add `migrations/021_audit_read_pending.sql` with pending/receipt indexes and narrow SQL lifecycle grants.

```go
type ReadToken struct {
    EventID, Owner uuid.UUID
    Generation uint64
    AdmissionDeadline time.Time
}
type ReadAdmission struct { Token ReadToken; Template Event }
type ReadCompletion struct {
    Token ReadToken
    Event Event
    Targets []Target
    Digest string
}
type PublishedRead struct { EventID uuid.UUID; Digest string }
type ReadLifecycle interface {
    BeginRead(context.Context, ReadAdmission) (ReadToken, error)
    RenewRead(context.Context, ReadToken) error
    FinalizeRead(context.Context, ReadCompletion) error
    RecoverExpired(context.Context, uint16, int) (int, error)
    AcknowledgePublished(context.Context, []PublishedRead) error
    CleanupPublished(context.Context, uint16, int) (int, error)
}
```

SQL uses database time. FoundationDB uses the existing injected clock for eligibility and transaction conflicts plus generations for safety. Finalization checks actor, identity and start fields against stored admission state and rejects changes to them. It calls `EncodeEvent` with the completion event and ordered targets. The terminal receipt stores the resulting transport digest, including the target list. An uncertain-commit retry must supply identical final data and digest.

Select the recovery bucket from the first SHA-256 byte of the event UUID modulo 64. Start each owner at generation one. Renewals preserve the generation; recovery increments it transactionally before finalizing. This avoids concentrating UUIDv7 admissions in time-based buckets.

- [ ] Implement every transition from the spec in both adapters. Reject expired admission retries. Finalization must never recreate missing pending state.
- [ ] Start independent bounded recovery workers on every application instance. Renew ownership only while the request remains active.
- [ ] Update each relay acknowledgment transaction to mark terminal receipts published and delete accepted outbox records together.
- [ ] Add `TestRuntimeAuditReadFinalizeRecoveryRace`, `TestRuntimeAuditReadUnknownCommit`, `TestRuntimeAuditReadStaleOwner` and `TestRuntimeAuditReadReceiptCleanup` against both real backends and two processes. Assert one terminal event, rejection of stale responses and safe uncertain-commit retries.
- [ ] Run the focused checks and commit both adapters after their shared contract passes.

## Task 4: Require complete FoundationDB change events

Extend [ApplyMutation](../../../internal/adapters/foundationdb/node_mutation_apply.go), [single-node deletion](../../../internal/adapters/foundationdb/node_mutation_delete.go), [relationship storage](../../../internal/adapters/foundationdb/relationship.go), [subtree steps](../../../internal/adapters/foundationdb/node_delete_step.go) and [audit finalization](../../../internal/audit/mutation_event.go). Include metadata writers and direct seed, repair and backfill paths.

Use the `audit.Change` type from Task 1. Add `audit.RequireMutationContext(context.Context) (audit.Event, error)`. In the FoundationDB adapter, add `AppendChanges(context.Context, fdb.Transaction, audit.Event, []audit.Change) error`. Keep the shared audit package independent of the adapter.

- [ ] Require context at every canonical writer and derive actual old/new values inside the committing transaction.
- [ ] Allocate event IDs before transaction retries. Persistent job steps derive stable IDs from the enclosing operation, persistent step and record identity.
- [ ] Record complete added, replaced and removed edges, including implicit parent changes and deletion effects. Preserve existing mutation plans, source checks, schema checks and receipts.
- [ ] Replace inferred previous values in cascades with actual transaction reads. Preserve actor and enclosing operation across job recovery.
- [ ] Update direct seed, repair, reference repair and backfill callers. Remove post-commit-only recording where atomic recording is available.
- [ ] Add `TestRuntimeAuditMutationMissingContext`, `TestRuntimeAuditRelationshipReplacement` and `TestRuntimeAuditCascadeEvidence` through the existing integration harness. Audit persistence failure must leave nodes and relationships unchanged. Verify complete deleted values and resumed-job attribution.
- [ ] Run mutation/cascade groups and commit enforcement with its callers.

## Task 5: Require complete SQL change events

Modify [users](../../../internal/adapters/postgres/user.go), [tokens](../../../internal/adapters/postgres/token.go), [memberships](../../../internal/adapters/postgres/org_member.go) and their operator/seed/datagen callers. Add `internal/adapters/postgres/audit_write.go` with `WriteChanges(ctx context.Context, tx pgx.Tx, operation audit.Event, changes []audit.Change) error`.

- [ ] Let each repository method own one transaction: read old values, write changes, write audit events, then commit. Record failed attempts separately after rollback.
- [ ] Record token identity, owner and permissions without token bytes, hashes or passwords. Record actual old/new membership roles.
- [ ] Preserve token last-use updates as a projection of accepted-token events without recursive events.
- [ ] Add `TestRuntimeAuditSQLAtomicChange`, `TestRuntimeAuditMembershipRoleReplacement` and `TestRuntimeAuditTokenMetadata`. Invoke public operator/service paths against real YugabyteDB and compare permanent evidence with actual state.
- [ ] Run the SQL audit checks and commit affected callers together.

## Task 6: Record public reads and rejections

Modify [MCP dispatch](../../../internal/adapters/mcp/server.go), [tool wrapping](../../../internal/adapters/mcp/tools/helpers.go), [authentication](../../../internal/auth/middleware.go) and [HTTP registration](../../../cmd/server/server_runtime.go). Add `internal/audit/read_executor.go` and `internal/adapters/mcp/audit_request.go`.

Define `ExecuteRead[T any](ctx context.Context, store ReadLifecycle, admission ReadAdmission, read func(context.Context) (ReadResult[T], error)) (EncodedRead, error)`. `ReadResult[T]` contains `Value T`, `Targets []Target`, `Count uint64` and `Encode func(io.Writer, T) error`. `EncodedRead` contains `Payload []byte`, `ContentType string` and `Evidence ReadEvidence`. The encoder serializes the actual result once into the existing response buffer while computing its digest. The transport emits those saved payload bytes without serializing the result again; transport envelopes remain outside the declared payload format. Apply the same contract to each publicly emitted stream chunk.

- [ ] Persist admission before reading. Include validation and authorization failures. Use a 30-second detached deadline for final recording after request cancellation.
- [ ] Persist final evidence before releasing data. Discard the response when finalization fails or recovery replaced its owner. Completed means the server prepared the result; it does not claim client receipt.
- [ ] Record safe early-rejection metadata with the system organization and unauthenticated actor until authentication establishes identity. Exclude untrusted headers, credentials and raw errors.
- [ ] Cover prompts, resources, protocol metadata, unknown operations, health and diagnostics through shared registration. Link authentication and business events by operation ID without duplicating atomic mutation success events.
- [ ] Add `TestRuntimeAuditPublicRead`, `TestRuntimeAuditEarlyRefusal`, `TestRuntimeAuditReadRecordingFailure` and `TestRuntimeAuditProtocolCoverage`. Invoke the real HTTP/MCP server and compare permanent records with known fixture targets and returned payload digests.
- [ ] Run public-boundary checks and commit dispatch, identity and read execution together.

## Task 7: Record runtime commands and background effects

Modify [the command gate](../../../internal/clispec/audit.go), [command outcomes](../../../internal/clispec/audit_event.go), [migrations](../../../internal/adapters/postgres/migrate.go), [provider commands](../../../provider/internal/tackprovider/command.go) and [provider refresh/apply](../../../provider/internal/tackprovider/schema_apply.go). Extend each existing backup, search-maintenance, deployment and recurring-job implementation in its current package.

Add `RunExternal(ctx context.Context, outbox OutboxWriter, intent Event, run func(context.Context, StepRecorder) (OutcomeDetails, error)) error`. Require the durable outbox interface rather than the general recorder interface that permits in-memory buffering. `StepRecorder` exposes `BeginStep(context.Context, Event) error` and `FinishStep(context.Context, Event) error` using that outbox. Stable parent/step IDs identify retries. Existing engine adapters perform state reconciliation.

- [ ] Route actual runtime reads, including provider refresh and previews that query state, through Task 3. A preview that only prints proposed actions does not read system state.
- [ ] Preserve first-time setup behavior. Broken runtime authentication or audit storage must not enable its exception.
- [ ] Replace generic success after skipped, deferred or partial work with observed typed outcomes. Preserve effects committed before rendering or transport failures.
- [ ] Record migration ranges/digests, backup manifests, index generations, role/privilege changes and completed steps. Exclude credential values.
- [ ] Give recurring jobs stable service identity, unique run IDs and initiating identity when applicable.
- [ ] Cover Tack runtime commands invoked by deployment and provider workflows. Preserve current Configs/OpenTofu ownership of container orchestration. Changes to the surrounding orchestration belong to the separately ordered deployment workstream; this audit repair must not create a replacement deployment command or imply that auditing a child command audits its parent controller.
- [ ] Add `TestRuntimeAuditCommandOutcomes`, `TestRuntimeAuditProviderRefresh`, `TestRuntimeAuditExternalRecovery` and `TestRuntimeAuditBackgroundIdentity`. Use real temporary engines and the existing provider acceptance environment. Assert exact effects, partial progress and safe resume behavior.
- [ ] Run affected groups and commit coherent operation families separately.

## Task 8: Prove coverage and integrate

Extend the existing harness with `runtime_audit_coverage_test.go` and `runtime_audit_scaling_test.go` under `internal/test/integration`. Derive cases from actual tool/command registration. Each case supplies safe fixture input, expected targets/outcome and permanent-evidence assertions. Missing executable coverage fails the test.

- [ ] Map every reviewed runtime operation to an executed case, including current shared mutation methods.
- [ ] Execute the spec's full acceptance list, including large parts, historical verification, missing context, stale ownership and failures after committed changes.
- [ ] Measure identical generated workloads at one, two and four app/worker instances. Record throughput, response latency, backlog, recovery time and database/broker/index/replica bytes.
- [ ] Report current installation needs separately. Preserve retention and runtime guarantees regardless of its present disk size.
- [ ] Require the coverage suite in merge checks. Automatic deployment implementation remains the next separately scoped project.
- [ ] Deploy Release A with Task 1 readers and Task 2 schema/consumer support while existing production writers keep their current event format. Verify every consumer instance runs Release A before activating any new writer. Use deployment inventory and the Docker Compose service labels with the existing read-only deployment verifier; remove its single-replica assumption for this check.
- [ ] Deploy Release B with pending-read adapters and the complete writers only after Release A passes testbed and production checks. Both server and consumer can keep the existing shared image-tag convention because these are two complete release revisions. Add `TestRuntimeAuditMixedVersionRollout` with two consumers, including one deliberately stale consumer; the transition must refuse that fleet. Rollback from Release B must retain Release A or newer consumers and preserve queued new-format events.
- [ ] Validate each complete group on the testbed before production activation. Record exact image revisions and observed audit evidence in tickets. Continue deployment automation and access work in the approved order afterward.

## Verification commands

Run these commands through the existing repository tooling. Set `TACK_TEST_ROOT` to the active worktree's absolute path.

```text
make build
make test-unit
make test-integration
TACK_TEST_ROOT=/absolute/checkout docker compose -f docker-compose.test.yml --profile runner run --rm tests test -count=1 -timeout 10m ./internal/test/integration -run '^TestRuntimeAudit'
TACK_TEST_ROOT=/absolute/checkout docker compose -f docker-compose.test.yml --profile runner run --rm tests test -run '^$' ./...
```

Run provider checks from its module with its existing Makefile and acceptance configuration. A skipped integration suite or a database that never becomes ready is not a pass. Diagnose failed prerequisites without weakening the required checks.

## Coverage map

| Design requirement | Task |
|---|---|
| Complete targets, outcomes, historical format and protected actor data | Task 1 defines the contract; Task 2 preserves large data. |
| One final read event, concurrent ownership and crash recovery | Task 3 persists state; Task 6 controls response release. |
| Node, relationship, metadata and cascade changes | Task 4 enforces FoundationDB boundaries. |
| Account, token and membership changes | Task 5 enforces SQL boundaries. |
| Rejections, prompts, protocol and operational reads | Task 6 records public execution. |
| Commands, jobs, provider and external effects | Task 7 records runtime work and recovery. |
| Complete coverage, scaling and deployed proof | Task 8 verifies and integrates the result. |

The lead checks this map, signatures and dependencies before dispatch. Independent tasks use subagents after their prerequisites pass. The lead reviews diffs and verification before integration. Initial service creation is outside this plan.
