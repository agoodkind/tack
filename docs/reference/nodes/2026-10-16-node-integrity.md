# Node integrity requirements

FoundationDB stores the authoritative nodes, properties, type definitions and relationships. OpenSearch stores bounded pages of searchable text and opaque permission keys. Search must not interpret the business meaning of an issue, status, progress report or other product-specific value. These requirements apply to public tools, services, imports, operator commands, repair jobs and storage transactions.

## Type definitions and validation

N1 (TACK-580). A single interpreter must derive capabilities, applicable properties, required values, defaults, option keys, reference constraints and containment rules from stored metadata. Product definitions select the applicable rules. The interpreter must work for unfamiliar type and property identifiers without a product-specific exemption. A controlled correction to a built-in definition does not authorize a public schema editor, an approval workflow or automatic definition creation.

N2 (TACK-580). For a create, apply declared defaults before validation. For an update, start with the committed node, apply only the supplied fields and removals, then validate the complete result. Check property names, applicability, required values, primitive JSON types and select or multiselect keys. Verify the existence, type and organization of each reference target. Check whether the caller may clear each field. For example, `High` is a label and `high` is an option key. Translate a label only when the input contract permits it. A `null` that clears a required value must fail.

Before strict activation, ordinary creates and updates keep their existing checks. Updates check the properties they change, while repairs must validate the complete proposed record. After activation, all ordinary writes must satisfy full-record validation. Temporary exceptions for invalid records cannot satisfy this requirement.

Generated tools, workflow-reference changes, imports, repairs and operator writes must use the same checked mutation. Apply validation inside the storage transaction and check for callers that bypass it. The transaction must recheck the source revision and current schema. A refusal must identify the node, property and reason. It must not commit changes to the primary record, view, resolution record, indexes, reference claims, relationships, desired search work or a success audit event.

Public create, read and partial-update tests require real FoundationDB and unfamiliar type and property definitions. A skipped integration case cannot establish any of these results.

| Operation | Evidence required |
| --- | --- |
| Valid create and update | Creation applies declared defaults. A partial update preserves unrelated stored values. Legitimate scope properties remain valid on comments, activities, states and projects. |
| Rejected write | For each undeclared name, wrong JSON type, label substituted for an option key, invalid reference and removal of a required property, the entire stored result matches its initial state. |

## Scope, containment and deletion

N3 (TACK-579). `scope_id` identifies the container that supplies the reference and numbering scope. `parent_id` identifies the direct containment parent. Exactly one `child_of` edge must identify that parent when the node requires one. Other relationship types allow additional associations. The type hierarchy permits several possible parent types without cycles. A node has one canonical direct parent, even if older records contain two direct edges.

For example, TACK-568 can keep project TACK as its scope while epic TACK-567 serves as its direct parent. A child of TACK-568 can keep the same project scope. Support legitimate same-type nesting and containment at arbitrary depth. A same-project move must preserve the node's UUID, reference and numbering scope. Any supported scope change must update its reference claims, indexes, access and desired search work together.

N4 (TACK-584). UUIDs and printed references must select parents under the same rules. Check existence, organization, scope and the proposed ancestor chain. Refuse self-parenting and deeper cycles. In each transaction attempt, reread the source and current parent. Recompute changes or reject stale preparation, including changes to an ancestor or a competing deletion. Concurrent successful moves must leave one property and one matching edge.

Relationship removal must not remove the only canonical parent edge while `parent_id` still selects it, even if older metadata omits that type pairing. Deletion must find intended children from their canonical records, including children with a missing edge. Apply the declared cascade or move the child to a permitted surviving parent. Refuse deletion before creating an orphan if the records cannot support either action. Preserve other relationship types. A move must update the child, view, parent index, matching edges, audit evidence and desired search work together.

The containment tests must enter through public operations against real FoundationDB. Each case requires a complete valid graph or a clear refusal with unchanged records.

| Case | Required evidence |
| --- | --- |
| Parent selection | UUID and printed references apply the same constraints. Unfamiliar types and valid nesting succeed. Self-parenting and deeper cycles fail. |
| Concurrent moves or a move during deletion | Repeated concurrent public calls produce consistent primary records, views and relationships regardless of scheduling order. |
| Sole-edge removal or inconsistent stored parents | Removal and deletion respect the canonical parent property even when older properties and edges disagree. The resulting records do not orphan a child. |

## Search content and authorization

N5 (TACK-580). Search must include readable, schema-defined text by default. Use the generic text conversion and bounded page reader, including declared representations for structured values and labels. An explicit exclusion still applies. The FoundationDB `Indexed` property controls secondary lookup. It does not select searchable text. A missing optional projection must not invalidate a property or halt a rebuild. Never index arbitrary JSON or reference targets merely because they exist.

When a node has an invalid or undeclared property, index the valid title, description and other permitted text. Omit that property, preserve its source value and report its node, property, source revision and reason. Distinguish a content warning from an authorization exclusion or a source-read or engine failure. An operational failure must return its error and keep the work pending. A verified repair must refresh the affected content and clear the warning through durable indexing. A change that alters projected text must schedule the affected nodes, retain unaffected pages and retire obsolete documents. A permission-only change must preserve page text and embeddings.

Search must derive access from authoritative relationships and permissions. A product-specific pair of types cannot disqualify a valid parent. An unrelated edge cannot grant access. OpenSearch must filter opaque permission keys before ranking. Tack must recheck current organization membership, scope, revocation and deletion before returning each result or replay. Exclude the node when its authorization cannot be established.

N8 (TACK-520 and TACK-544). Semantic acceptance requires the full retrieval criteria, including a QA reproduction of the reported TACK-578 miss with realistic distractors. An exact-title match cannot prove semantic retrieval. Preserve full Unicode content, distinct results, durable continuation and replay, one inference per session, bounded work, source writes during search outages, stale-write rejection, index replacement recovery and eventual cleanup.

The QA retrieval regression compares the original public results for TACK-578.

| Production query or action | Observed result |
| --- | --- |
| Exact title | TACK-578 ranked first. |
| `restart containers one by one` | In three runs an unrelated Dependabot record ranked first. TACK-578 did not appear in the first 25 results. |
| `rolling Docker restart` | TACK-578 ranked third. |
| `restart containers sequentially` | TACK-578 ranked 24th. |
| Continue, then replay | Continuation returned 25 further distinct results. Replay returned the same page. |

N9 (TACK-574 and TACK-575). Measure search-only workloads and simultaneous searches and writes independently of activation. Record environment-specific performance and recovery evidence against the [search acceptance criteria](../search/2026-09-19-search-acceptance.md).

## Audit evidence

N7 (TACK-583). For ordinary edits, workflow references, removals, parent changes, repairs and fixed metadata corrections, compute the changed field identifiers and actual old and new values from the committed transaction. Distinguish an explicit removal from an omitted input. Include the full identities of changed relationships, normalized references, all targets, actor, time, order and outcome. Record refusals, failures, interruptions and partial work. Never record a rejected or abandoned attempt as an applied value.

Commit the event intent with the FoundationDB change. Preserve event and actor identities when the relay retries delivery through Kafka. Require `audit-consumer` as the sole writer of `audit.events` and continue each organization and shard hash chain. Honor protected-value and redaction rules. Exclude credentials from audit evidence. Do not rewrite historical rows or invent missing times, authors or values.

Verify the permanent ledger through the native audit reader for a public generic edit, workflow reference, removal, parent change, rejected request, transaction retry and repair. Use real FoundationDB, Kafka and YugabyteDB. Compare the stored values with their serialized representations in the intent, the Kafka event and the permanent ledger, verify the actor and chain, and retain an ordinary QA update for comparison with production.

The main organization's audit export through October 17, 2026 at 05:30:15 UTC contained 4,038 `node.update` rows without field deltas. Their outcomes matter when interpreting that history.

| Outcome | Rows |
| --- | ---: |
| `ok` | 1,820 |
| `error` | 29 |
| `unrecorded` | 2,189 |

The export does not establish 4,038 successful writes. These rows cannot reconstruct the missing values or their edit times.

## Source repair and release

N6 (TACK-582). Each supported repair requires a command-boundary regression on disposable QA using real FoundationDB and audit dependencies. Run the actual command against synthetic test fixtures through the integration suite. QA execution requires reviewed mappings. Private production exports remain outside the repository. Work must be bounded and retry-safe under a reviewed [frozen manifest](2026-10-16-node-backfill.md#frozen-manifests). Design approval and a successful QA run do not authorize production writes. Production requires separate approval for the exact manifest frozen from its current records.

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

Keep ordinary write checks active during cleanup. Verify all reviewed repairs, read back each changed record, confirm a repeat preview does not propose further changes and complete a fresh broad scan. Exercise the scan-to-activation boundary with a concurrent invalid write on QA. A changed source, failed repair or outstanding violation prevents [strict validation activation](../../runbooks/nodes/activate-strict-validation.md). Independent audit or search work may continue.

After QA acceptance, verify production separately under its deployment locks. Record commits and merged changes, applied configuration, installed service digests, store and model readiness, public queries, current authorization, continuation, audit deltas, workload results and every remaining exclusion or stuck item. Recheck conditions that could invalidate QA evidence. Schema prevention, completed source repairs, search recovery and measured capacity each require their own evidence.

A failed correction stops its dependent deployment. Preserve authoritative data. Roll back only to a release or configuration compatible with the applied migrations. Resume indexing from durable state. An application rollback cannot undo repaired values or rewrite the audit ledger. Require the full production recovery drill with source writes and pending search work. A sequential restart does not establish that result.

N10 (TACK-567). The [provider contract](../tack-provider.md#provider-ownership) governs provider reuse. Planning must be read-only, expose differences and support import without resetting state. Assign one writer per field and require locks, audit and recovery after an interrupted apply. Prove QA adoption before production. Coordinate shared QA windows and locks with the infrastructure owner.

For TACK-581, confirm guest absence by Proxmox node and VMID before excluding an unenrolled guest from both lock acquisition and deployment. Fail when inventory is unreadable or an enrolled guest is missing.

TACK-568 covers read-only application and migration status. LAB-92 owns reusable database, search, credential, certificate, backup and scheduling capabilities. TACK-569 covers justified Tack-specific schema, audit, compatibility and coordinated search behavior. These scopes supersede TACK-571's combined assignment. TACK-570 covers QA adoption, TACK-572 transfers ownership, and TACK-573 covers production adoption. Retire each earlier writer before its replacement starts writing.

Attribute old mismatches to a write, import, seed, backfill or internal operation only when retained evidence identifies it. Attach the records that establish the cause. If they do not establish it, record the cause as unknown.
Before closing an issue, attach the canonical requirement and plan links, the relevant merged commits, installed revisions and direct acceptance evidence.
For a superseded issue, verify that its replacement covers its complete scope. An unresolved or unapproved record remains visible.
