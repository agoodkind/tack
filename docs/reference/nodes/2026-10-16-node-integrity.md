# Node integrity requirements

FoundationDB stores the authoritative nodes, properties, type definitions and relationships. OpenSearch stores bounded pages of searchable text and opaque permission keys. Search must not interpret the business meaning of an issue, status, progress report or other product-specific value. These requirements apply to public tools, services, imports, operator commands, repair jobs and storage transactions.
## Type definitions and validation

N1 (TACK-580). A single interpreter must derive these rules from stored metadata:

- Capabilities.
- Applicable properties.
- Required values.
- Defaults.
- Option keys.
- Reference constraints.
- Containment rules.

Product definitions select the applicable rules. The interpreter must work for unfamiliar type and property identifiers without a product-specific exemption. A controlled correction to a built-in definition does not authorize a public schema editor, an approval workflow or automatic definition creation.

N2 (TACK-580). The validator must apply declared defaults before validating a create. For an update, it must start with the committed node, apply only the supplied fields and removals, then validate the complete result.

The validator must check each property:

- Its name and applicability.
- Its required values.
- Its primitive JSON type.
- Its select or multiselect keys.
- Whether the caller may clear it.

The validator must verify each reference target's existence, type and organization. For example, `High` is a label and `high` is an option key. The validator must translate a label only when the input contract permits it. A `null` that clears a required value must fail.

Before strict activation, ordinary creates and updates keep their existing checks. Updates check the properties they change, while repairs must validate the complete proposed record. After activation, all ordinary writes must satisfy full-record validation. Temporary exceptions for invalid records cannot satisfy this requirement.

Generated tools, workflow-reference changes, imports, repairs and operator writes must use the same checked mutation. The storage transaction must validate the mutation. Reviewers must check for callers that bypass that validation. The transaction must recheck the source revision and current schema. A refusal must identify the node, property and reason. It must not commit any of these changes:

- The primary record.
- The view.
- The resolution record.
- The indexes.
- The reference claims.
- The relationships.
- The desired search work.
- A success audit event.

Public create, read and partial-update tests require real FoundationDB and unfamiliar type and property definitions. A skipped integration case cannot establish any of these results.

Validation evidence:

| Operation | Evidence required |
| --- | --- |
| Valid create and update | Creation applies declared defaults. A partial update preserves unrelated stored values. Legitimate scope properties remain valid on comments, activities, states and projects. |
| Rejected write | For each undeclared name, wrong JSON type, label substituted for an option key, invalid reference and removal of a required property, the entire stored result matches its initial state. |
## Scope, containment and deletion

N3 (TACK-579). `scope_id` identifies the container that supplies the reference and numbering scope. `parent_id` identifies the direct containment parent. Exactly one `child_of` edge must identify that parent when the node requires one. Other relationship types allow additional associations. The type hierarchy permits several possible parent types without cycles. A node has one canonical direct parent, even if older records contain two direct edges.

For example, TACK-568 can keep project TACK as its scope while epic TACK-567 serves as its direct parent. A child of TACK-568 can keep the same project scope. The hierarchy must support legitimate same-type nesting. It must permit containment at arbitrary depth. A same-project move must preserve the node's UUID, reference and numbering scope. Any supported scope change must update its reference claims, indexes, access and desired search work together.

N4 (TACK-584). UUIDs and printed references must select parents under the same rules. The mutation must check existence, organization, scope and the proposed ancestor chain. It must refuse self-parenting and deeper cycles. Each transaction attempt must reread the source and current parent. It must recompute changes or reject stale preparation, including changes to an ancestor or a competing deletion. Concurrent successful moves must leave one property and one matching edge.

Relationship removal must not remove the only canonical parent edge while `parent_id` still selects it, even if older metadata omits that type pairing. Deletion must find intended children from their canonical records, including children with a missing edge. It must apply the declared cascade or move the child to a permitted surviving parent. It must refuse deletion before creating an orphan if the records cannot support either action. It must preserve other relationship types. A move must update these records together:

- Update the child.
- Update the view.
- Update the parent index.
- Update the matching edges.
- Update the audit evidence.
- Update the desired search work.

The containment tests must enter through public operations against real FoundationDB. Each case requires a complete valid graph or a clear refusal with unchanged records.

Containment evidence:

| Case | Required evidence |
| --- | --- |
| Parent selection | UUID and printed references apply the same constraints. Unfamiliar types and valid nesting succeed. Self-parenting and deeper cycles fail. |
| Concurrent moves or a move during deletion | Repeated concurrent public calls produce consistent primary records, views and relationships regardless of scheduling order. |
| Sole-edge removal or inconsistent stored parents | Removal and deletion respect the canonical parent property even when older properties and edges disagree. The resulting records do not orphan a child. |
## Search content and authorization

N5 (TACK-580). Search must include readable, schema-defined text by default. It must use the generic text conversion and bounded page reader, including declared representations for structured values and labels. An explicit exclusion still applies. The FoundationDB `Indexed` property controls secondary lookup. It does not select searchable text. A missing optional projection must not invalidate a property or halt a rebuild. Search must not index arbitrary JSON or reference targets merely because they exist.

When a node has an invalid or undeclared property, indexing must include the valid title, description and other permitted text. It must omit that property, preserve its source value and report its node, property, source revision and reason. The report must distinguish a content warning from an authorization exclusion or a source-read or engine failure. An operational failure must return its error and keep the work pending. A verified repair must refresh the affected content and clear the warning through durable indexing. A change that alters projected text must schedule the affected nodes, retain unaffected pages and retire obsolete documents. A permission-only change must preserve page text and embeddings.

Search must derive access from authoritative relationships and permissions. A product-specific pair of types cannot disqualify a valid parent. An unrelated edge cannot grant access. OpenSearch must filter opaque permission keys before ranking. Tack must recheck current organization membership, scope, revocation and deletion before returning each result or replay. Tack must exclude a node when it cannot establish the node's authorization.

N8 (TACK-520 and TACK-544). Semantic acceptance requires the full retrieval criteria, including a QA reproduction of the reported TACK-578 miss with realistic distractors. An exact-title match cannot prove semantic retrieval. Search must preserve these guarantees:

- Full Unicode content.
- Distinct results.
- Durable continuation and replay.
- One inference per session.
- Bounded work.
- Source writes during search outages.
- Stale-write rejection.
- Index replacement recovery.
- Eventual cleanup.

The QA retrieval regression compares the original public results for TACK-578.

Retrieval observations:

| Production query or action | Observed result |
| --- | --- |
| Exact title | TACK-578 ranked first. |
| `restart containers one by one` | In three runs an unrelated Dependabot record ranked first. TACK-578 did not appear in the first 25 results. |
| `rolling Docker restart` | TACK-578 ranked third. |
| `restart containers sequentially` | TACK-578 ranked 24th. |
| Continue, then replay | Continuation returned 25 further distinct results. Replay returned the same page. |

N9 (TACK-574 and TACK-575). Performance acceptance must measure search-only workloads and simultaneous searches and writes independently of activation. It must record environment-specific performance and recovery evidence against the [search acceptance criteria](../search/2026-09-19-search-acceptance.md).
## Audit evidence

N7 (TACK-583). For ordinary edits, workflow references, removals, parent changes, repairs and fixed metadata corrections, the mutation must compute the changed field identifiers and actual old and new values from the committed transaction. It must distinguish an explicit removal from an omitted input. Audit evidence must include these details:

- The full identities of changed relationships.
- Normalized references.
- All targets.
- The actor.
- The time.
- The order.
- The outcome.

Recording must include refusals, failures, interruptions and partial work. It must never record a rejected or abandoned attempt as an applied value.

The mutation must commit the event intent with the FoundationDB change. The relay must preserve event and actor identities when it retries delivery through Kafka. The `audit-consumer` must remain the sole writer of `audit.events` and continue each organization and shard hash chain. Audit recording must honor protected-value and redaction rules and exclude credentials. Audit writers must not rewrite historical rows or invent missing times, authors or values.

Audit acceptance must verify the permanent ledger through the native audit reader for a public generic edit, workflow reference, removal, parent change, rejected request, transaction retry and repair. It must use real FoundationDB, Kafka and YugabyteDB. It must compare the stored values with their serialized representations in the intent, the Kafka event and the permanent ledger, verify the actor and chain, and retain an ordinary QA update for comparison with production.

The main organization's audit export through October 17, 2026 at 05:30:15 UTC contained 4,038 `node.update` rows without field deltas. Their outcomes matter when interpreting that history.

Historical audit outcomes:

| Outcome | Rows |
| --- | ---: |
| `ok` | 1,820 |
| `error` | 29 |
| `unrecorded` | 2,189 |

The export does not establish 4,038 successful writes. These rows cannot reconstruct the missing values or their edit times.
## Source repair and release

N6 (TACK-582). Each supported repair requires a command-boundary regression on disposable QA using real FoundationDB and audit dependencies. The integration suite must run the actual command against synthetic test fixtures. QA execution requires reviewed mappings. Private production exports remain outside the repository. Work must be bounded and retry-safe under a reviewed [frozen manifest](2026-10-16-node-backfill.md#frozen-manifests). Design approval and a successful QA run do not authorize production writes. Production requires separate approval for the exact manifest frozen from its current records.

The command regression must establish the required repair behavior.

Repair evidence:

| Command-boundary case | Required observation |
| --- | --- |
| Dry run | The real command's help and preview report the available parameters, proposed results and unresolved entries. Preview does not modify records. |
| Changed source or graph ambiguity | The affected entry cannot execute its old mapping. Unresolved records stay unchanged and appear in the report. |
| Partial failure | Output identifies committed phases and failed or conflicted entries. A retry resumes at the recorded checkpoint. |
| Interrupted report migration | A retry reuses the original report comment. The command verifies its full text and attachment before removing the obsolete field. |
| Property decision | Tests cover property-level history, the approved latest explicit dated decision, missing evidence and conflicting decisions. |
| Smoke-comment deletion | A failed export or stale source prevents deletion of the exact UUID. A verified export precedes deletion, and repeat execution preserves other records. |
| Duplicate-notice reattachment | Normal reads and search show the corrected parent and matching representations. The record keeps its ID, text, creator, creation time and unrelated relationships. A retry does not replace or duplicate it. |
| Repeat execution | Each completed repair remains unchanged. A second preview proposes neither another change nor another comment. |

Cleanup must keep ordinary write checks active. Cleanup acceptance must verify all reviewed repairs, read back each changed record, confirm a repeat preview does not propose further changes and complete a fresh broad scan. QA acceptance must exercise the scan-to-activation boundary with a concurrent invalid write. A changed source, failed repair or outstanding violation prevents [strict validation activation](../../runbooks/nodes/activate-strict-validation.md). Independent audit or search work may continue.

After QA acceptance, production verification must run separately under its deployment locks. Its evidence must include these details:

- Commits and merged changes.
- Applied configuration.
- Installed service digests.
- Store and model readiness.
- Public queries.
- Current authorization.
- Continuation.
- Audit deltas.
- Workload results.
- Every remaining exclusion or stuck item.

Production verification must recheck conditions that could invalidate QA evidence. Schema prevention, completed source repairs, search recovery and measured capacity each require their own evidence.

A failed correction stops its dependent deployment. Recovery must preserve authoritative data. A rollback must use only a release or configuration compatible with the applied migrations. Indexing must resume from durable state. An application rollback cannot undo repaired values or rewrite the audit ledger. Production acceptance requires the full recovery drill with source writes and pending search work. A sequential restart does not establish that result.

N10 (TACK-567). The [provider contract](../tack-provider.md#provider-ownership) governs provider reuse. Planning must be read-only, expose differences and support import without resetting state. Each field must have one writer. Apply must use locks and audit recording and support recovery after an interruption. Adoption requires QA proof before production. Shared QA windows and locks require coordination with the infrastructure owner.

For TACK-581, deployment must confirm guest absence by Proxmox node and VMID before excluding an unenrolled guest from both lock acquisition and deployment. It must fail when inventory is unreadable or an enrolled guest is missing.

The provider assignments separate read-only status, reusable capabilities and Tack-specific behavior. They supersede TACK-571's combined assignment.

Provider scopes:

| Ticket | Scope |
| --- | --- |
| TACK-568 | Read-only application and migration status. |
| LAB-92 | Reusable database, search, credential, certificate, backup and scheduling capabilities. |
| TACK-569 | Justified Tack-specific schema, audit, compatibility and coordinated search behavior. |
| TACK-570 | QA adoption. |
| TACK-572 | Ownership transfer. |
| TACK-573 | Production adoption. |

Each earlier writer must retire before its replacement starts writing.

Attribution of old mismatches to a write, import, seed, backfill or internal operation requires retained evidence that identifies that operation. The evidence must include attached records that establish the cause. The records must identify the cause as unknown when retained evidence does not establish it.

Issue closure requires attached canonical requirement and plan links, relevant merged commits, installed revisions and direct acceptance evidence.

A superseded issue requires verification that its replacement covers its complete scope. An unresolved or unapproved record must remain visible.
