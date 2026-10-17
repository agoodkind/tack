# Node integrity, search, and audit correction specification

Tack must validate node data against its stored definitions, search readable node content without product-specific rules, and record the changes that actually commit. Existing-data repairs must preserve useful content and remain separate from prevention work.

This draft covers the Tack investigation and the remaining delivery dependencies. It does not approve the proposed data repairs. The [implementation plan](../plans/2026-10-16-node-integrity.md) separates independent work from decisions that remain open.

## Use the generic node model

FoundationDB stores authoritative nodes, properties, metadata, and relationships. OpenSearch indexes bounded text pages and opaque access keys. Search must not interpret the business meaning of an issue, status, progress report, or another product-specific value.

The following requirements apply to public tools, services, imports, operator commands, repair operations, and storage transactions.

| Requirement | Required behavior | Responsible ticket |
| --- | --- | --- |
| N1 | Interpret stored type capabilities and property definitions through one generic contract. Remove runtime branches and exemptions based on concrete product types or property names. | [TACK-580](https://tack.home.goodkind.io/browse/TACK-580) |
| N2 | Validate the complete resulting node after defaults and partial updates. Check applicability, required fields, value shapes, enum keys, reference targets, and permitted removals before committing. | TACK-580 |
| N3 | Keep project scope separate from the canonical direct parent. Support valid nested issues and arbitrary-depth containment without adding product type-pair rules to search. | [TACK-579](https://tack.home.goodkind.io/browse/TACK-579) |
| N4 | Maintain matching canonical parent properties and containment edges during concurrent updates, relationship removal, and deletion. Reject cycles and stale writes. | [TACK-584](https://tack.home.goodkind.io/browse/TACK-584) |
| N5 | Include readable text from schema-defined properties by default. Omit invalid properties and report them separately while indexing valid content. Honor explicit exclusions and reuse generic text conversion. | TACK-580 |
| N6 | Repair existing records only through reviewed, bounded, retry-safe mappings with source checks and recorded results. | [TACK-582](https://tack.home.goodkind.io/browse/TACK-582) |
| N7 | Record actual committed node, repair, and fixed-metadata changes with before-and-after values and actor information in the existing audit ledger. | [TACK-583](https://tack.home.goodkind.io/browse/TACK-583) |
| N8 | Correct the demonstrated semantic retrieval failure and prove the complete search acceptance contract. | [TACK-520](https://tack.home.goodkind.io/browse/TACK-520), [TACK-544](https://tack.home.goodkind.io/browse/TACK-544) |
| N9 | Complete search-only and simultaneous search/write measurements independently of activation. | [TACK-574](https://tack.home.goodkind.io/browse/TACK-574), [TACK-575](https://tack.home.goodkind.io/browse/TACK-575) |
| N10 | Preserve the accepted provider boundary and complete QA adoption before production adoption. | [TACK-567](https://tack.home.goodkind.io/browse/TACK-567) |

Schema editing remains future work. This correction must not add a public schema editor, schema approval workflow, or automatic property-definition creation. A controlled correction of inconsistent built-in metadata does not authorize runtime schema editing.

## Separate project scope from containment

An issue's `scope_id` selects its project-wide reference and numbering scope. Its `parent_id` selects its direct containment parent. The implicit `child_of` edge must select that same parent. Other relationship types can represent additional associations.

For example, TACK-568 can retain project TACK as its scope while its direct parent is epic TACK-567. Another issue can use TACK-568 as its direct parent and retain the same project scope. A direct project edge and an epic edge do not establish that both are canonical parents.

The correction preserves the current single direct containment parent contract. It does not convert duplicate stored edges into a new multiple-parent feature. Generic relationship graphs remain supported.

Parent resolution must apply the same metadata contract to UUIDs and printed references. A mutation must validate existence, organization, scope constraints, and containment consistency against the committed state. Self-parenting and ancestor cycles must fail before persistence.

Concurrent moves must reread the relevant source state inside the transaction or reject a changed revision. Parent deletion, child movement, and sole-parent removal must follow one contract. Deletion must account for intended children even when old properties and edges disagree; it must either perform the declared child lifecycle or refuse before creating an orphan.

Search access must use the authoritative relationship and permission boundary. It must not reject a valid node because a product-specific parent pairing is absent from a list. An unresolved permission boundary must still prevent disclosure.

## Enforce the fixed schema on every write

Normal public creates and updates already reject undeclared property names. That protection does not prove full schema enforcement. The inspected release lacks general primitive and enum validation, and its update validation omits removed values before checking required fields.

An invalid request must return the node and property identifiers with a clear reason. The transaction must not change the node, read views, indexes, relationships, audit success events, or desired search work. A permitted partial update must preserve unspecified fields. Full-result validation would also reject unrelated edits to an already-invalid record until its repair completes; the rollout must resolve this transition explicitly.

The validator must interpret the data-defined property type and options. For example, a select option with key `high` and display label `High` must use the canonical key. Runtime code must not special-case the property `priority`. Required values must remain valid after a `null` removal request.

The current `scope_id` applicability definition and service-managed exemption disagree. Legitimate comment creation still writes that property. Resolve the fixed metadata and generic service contract together before strict validation becomes active. Preserve the existing values while that correction remains pending.

Investigate the historical write, seed, import, and repair paths that produced the old mismatches. Record an unknown cause when retained evidence cannot establish it. The current validator cannot establish what an older release allowed.

## Search declared content by default

The selected direction includes readable values under the fixed schema without requiring a separate inclusion declaration for every property. Explicit exclusions retain their meaning. The FoundationDB `Indexed` flag remains a secondary-lookup setting and must not decide search inclusion.

Reuse the existing generic text interpreter and bounded page reader. Derive readable representation from the generic schema contract; use existing explicit representations for structured values and labels. Do not index arbitrary JSON, reference targets, or excluded content merely because a value exists.

Missing optional search metadata must not invalidate an otherwise valid property or stop the entire rebuild. Invalid values and unknown source properties remain consistency defects. They must produce actionable diagnostics without inventing definitions or changing source records.

Index a node's valid declared content even when another property is invalid or undeclared. Omit the invalid property from searchable text and report its node, property, source revision, and reason separately for repair. Preserve the source value. The user selected this behavior. Continue enforcing access permissions and explicit search exclusions; unresolved authorization must still exclude the node.

Keep content warnings distinct from authorization exclusions and operational failures. A source-read or search-engine failure must retain pending work and return its existing explicit error. Do not reinterpret it as an invalid property. A verified property repair must refresh the affected content and clear its warning through the normal durable indexing process.

This direction amends the explicit-declaration requirement in the existing [search design](2026-09-19-search-design.md) and the Opaque metadata section of [search acceptance](2026-09-19-search-acceptance.md). Those documents still govern unrelated behavior. Before implementation, consolidate the chosen policy into those existing sections and update their conflicting tests. Do not maintain contradictory rules in parallel.

Existing intentional exclusions must survive that transition. A changed text projection must schedule affected content, preserve unaffected pages, and retire obsolete documents through the existing bounded process. A permission-only change must preserve page text and embeddings.

## Preserve authorization and complete search behavior

OpenSearch must filter opaque access keys before ranking. Tack must check current authoritative permissions before returning a result or replay. Membership, revoked permissions, deleted nodes, tenant boundaries, and scope boundaries remain mandatory checks.

The correction must preserve complete Unicode text coverage, distinct results, durable continuation and replay, one inference per session, bounded work, source-write durability during search outages, stale-write rejection, replacement recovery, and eventual cleanup. A successful exact-title query does not establish semantic acceptance.

The [search correction and delivery plan](../plans/2026-10-16-search-correction.md) maps every existing acceptance section to required evidence. The full criteria remain mandatory, including workload measurements and environment-specific recovery proof.

## Record committed changes in the audit ledger

Ordinary node updates, repairs, and fixed-metadata corrections must record changed field identifiers and their actual before-and-after values. Removal must remain distinguishable from an absent input. Parent changes and workflow-reference updates must use the same canonical recording path.

Build the delta from the committed mutation, including transaction retries and normalized values. A rejected request must not appear as an applied value. Commit the event intent with the FoundationDB mutation; retain the existing relay, Kafka, single consumer writer, ledger identity, and hash chain.

Preserve the [operator identity and audit contract](../../operator-identity-and-audit.md). Keep applicable data-protection rules and secret handling. Do not rewrite historical ledger rows or fabricate missing property timestamps, authors, or values. Future recording cannot reconstruct absent history.

## Repair existing data separately

The [backfill specification](2026-10-16-node-backfill.md) is the single home for the observed classes, proposed mappings, unresolved records, and evidence limits. A search exclusion alone does not establish a source-data violation.

Every repair must identify the exact record, source values and hash, expected revision, evidence, inferred intent, reviewed decision, resulting properties and relationships, and available provenance. A changed source must invalidate its preview. Retries must recognize completed work without duplicating comments or removing unrelated content.

Repair commands must reuse the current generic repair machinery and audit gate. A one-time command must declare its ticket and literal removal date under `clispec.Lifetime`. It must default to a nonmutating preview. Its explicit execution must honor the existing command gates.

## Keep the existing delivery work in scope

The accepted [declarative provider specification](2026-10-10-tack-provider.md) governs the remaining provider work. It requires generic provider reuse, read-only planning, visible differences, import without resetting data, one writer per managed field, locks, audit, and interrupted-apply recovery. This draft does not replace it.

TACK-568 retains application state contracts and read-only migration status. TACK-569 implements justified Tack resources. TACK-570 covers QA adoption, TACK-571 covers data/search/backup state, TACK-572 transfers ownership, and TACK-573 covers production adoption. A merged specification does not complete these implementation tasks.

[TACK-581](https://tack.home.goodkind.io/browse/TACK-581) retains deployment handling for confirmed absent, unenrolled guests. It must compare Proxmox node and VMID evidence, exclude confirmed absences from both locks and deployment selection, and fail when inventory is unreadable or an enrolled guest is missing.

The Tack implementing session is `tack-deployment-completion`. Alexander remains the accountable Tack member. The Proxmox migration session owns shared guest work. Coordinate shared QA windows, deployment locks, and provider dependencies directly. Continue independent coding and investigation while those dependencies wait.

## Distinguish the evidence from completion

The October 16 investigation inspected production release `460d25a99f5b4ab03f2e8c0ef44071737cc63f71`. The documentation base is `7f275f9d7fdd422ef949b41fb9b457e60635d7a8`. Recheck both before implementation or deployment.

Production exact-title search returned TACK-578 first. The query `restart containers one by one` omitted it from the first 25 results on three runs and returned an unrelated Dependabot record first. `rolling Docker restart` ranked it third; `restart containers sequentially` ranked it 24th. Continuation returned 25 additional distinct records, and replay returned the same page. TACK-520 contains these public probe results.

The main-organization audit export through October 17 at 05:30:15 UTC contained 4,038 `node.update` rows without property deltas: 1,820 had outcome `ok`, 29 had `error`, and 2,189 had `unrecorded`. These counts do not establish 4,038 successful writes. TACK-583 records the producer defect: `StageStateChange` sets `Delta` to `nil`; the consumer and reader preserve supplied deltas.

Source inspection identifies possible concurrent parent inconsistencies, but the investigation did not reproduce them with malformed production writes. Tests must establish those paths on real disposable dependencies.

## Resolve the remaining decisions

| Decision | Current position | Work that can continue |
| --- | --- | --- |
| Several parent and orphan mappings infer intent. | The backfill specification marks each proposal for review. | Graph validation and nonmutating previews can continue. |
| One synthetic Smoke comment has an absent parent. | Export and retirement remain proposed; deletion lacks approval. | Other reviewed repairs do not depend on that decision. |
| Full-result validation encounters an old invalid record. | Decide whether ordinary edits wait for a complete repair or require an explicitly bounded transition policy. Do not add a silent validation bypass. | Validation code, previews, and QA tests can continue before strict production activation. |

QA must prove the reviewed behavior before production. Record source commits, merged PRs, installed image digests, applied configuration, repair results, and public runtime acceptance separately. Close superseded tickets only after the replacement covers their entire scope and preserves their evidence.
