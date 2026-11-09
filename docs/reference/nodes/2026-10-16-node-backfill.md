# Node repair manifests and reviewed mappings

[TACK-582](https://tack.home.goodkind.io/browse/TACK-582) records completion of all 293 approved production repairs. The repeat preview of 34 manifests returned 336 completed entries across 293 distinct nodes. Those results confirm the recorded checkpoints. Separate normal readback, permanent audit rows, graph verification and search checks documented the effects of the repairs. The archived classifications explain those receipts. The completed repairs must not run again.

## Frozen manifests

Before execution, scan the authoritative node records for additional classes. Include every scanned case in the repair inventory. Report each unrecognized class without automatically mapping it.

The reviewed manifest must enumerate every scanned case and record its decision, including cases that do not need a change and cases that remain unresolved. It must distinguish observations, inferred intent and approved decisions. Reviewers must reconcile this manifest with the complete inventory. They must check each frozen entry against its reviewed decision and account for approved changes that the frozen files omit.

The command freezes only the entries in its request. Each entry identifies its node, type, repair ID and classes and includes at least one evidence reference. The parser accepts an `executable` entry only if it declares a change. An `unresolved` entry requires a reason and must not declare a change. The parser does not accept `unchanged` as an input status.

The parser checks evidence references but does not require a dated decision item. A valid frozen file may cover only a subset of the reviewed manifest. Validation cannot establish approval or complete scan coverage. Unknown or unreviewed actions must not execute.

TACK-582 superseded the TACK-220 repair classes and the TACK-221 reporting task. Repairs must reuse the generic TACK-206 tooling and its audit gate. Add only missing general capabilities. A one-time repair must preview without mutation by default and require explicit execution through the existing command gates. Inventory review compares those classes with the actual records.

A search exclusion alone does not establish a data violation.

A frozen entry stores the repaired node's primary record bytes and digest, the source ID, type and target ID of each incoming and outgoing relationship, the related node IDs, the parent chain, the graph digest and the organization's metadata revision. The manifest digest identifies the exact contents reviewed for execution. For each executable change, reviewers must record the complete before and after values, affected relationships, available provenance and reason for the change.

Before any write, the command checks the manifest digest, validates every frozen source and checks every entry, including duplicate IDs and conflicting declarations. If any of those checks fails, it rejects the whole manifest.

During execution, a conflict with current records or a checkpoint stops the affected entry. Other independently reviewed entries can proceed.

The freezer records a related node's primary digest only when it finds that primary in the repaired node's organization. A missing or out-of-organization primary leaves an empty digest beside the ID. The ancestor reader includes a missing ancestor's ID in the chain before it stops.

The graph check requires the repaired node to exist with the declared type. Delete and unresolved entries skip the other graph checks at this stage. The protected export stores the relationship payloads. Before deleting the node, the command compares the exported payloads with the current incoming and outgoing relationships.

`ops backfill once-node-integrity` declares TACK-582 and a removal deadline of December 31, 2026 UTC. Its installed release must still list the command and its parameters. Exactly one of `--request` and `--manifest` selects an absolute path inside `TACK_REPAIR_DIR`. By default the command previews without changing product records. A request freezes a manifest only in a dry run. A manifest preview validates the frozen contents and reports each entry against its checkpoint and, when work remains, its current source. Execution requires a frozen manifest, explicit production approval for that manifest when applicable, and the global `--execute` flag. The operator identity and audit gates apply to each invocation.

Preview handles each entry in this order.

| Entry | Preview behavior |
| --- | --- |
| Unresolved | It returns `unresolved` before reading a checkpoint. |
| Checkpoint with another primary digest, node ID or a phase the entry does not recognize | It returns `conflicted`. |
| Matching terminal checkpoint | It returns `completed` when the entry does not require another phase. It does not reread the node, its relationships or current metadata. |
| Work remains | It compares current primary, graph and metadata evidence with the frozen entry. A mismatch produces `conflicted`. Matching evidence produces `in_progress` with a checkpoint or `planned` without one. |

The manifest must combine overlapping approved patches for one node and validate the complete result before writing. If an unresolved field prevents validation, the node must remain unchanged. A repair must preserve unrelated identities, properties, timestamps, provenance and relationships. Each mutation must reconcile the primary record, read view, resolution record, indexes, claims, matching edges, desired search work and audit intent atomically. A node mutation commits its checkpoint with the database change. The export phase writes and verifies the file before recording its checkpoint. Deletion reads the export again and compares it with the checkpoint and current relationships before deleting the node. An interrupted run resumes from that checkpoint.

Execution reports `applied`, `unchanged`, `conflicted`, `unresolved` and `failed`, with the phases committed for each entry. An `unchanged` result means the entry had already completed and this run did not commit another phase for it. Verification requires per-class counts and distinct-node counts, normal readback of every changed record, verification of report attachments, graph simulation, another broad scan and search checks. A second preview must not propose repeated changes or duplicate comments. A successful exit alone cannot establish that every entry completed.

## Interpreting the captured inventory

The October 16 inventory and the final repair list measured different populations. The inventory read 5,090 views across 11 registered types in the main organization. It compared 454 suspect primary records with their views. Every property map matched. Three parent UUIDs lacked primary, view and resolution records. Search reported 450 exclusions. Of these, 443 concerned the hierarchy and seven concerned undeclared properties. Search reported zero stuck items. The inventory classified 2,909 distinct records. Classes overlapped. Each captured record included the evidence, intended action, source hash, review reasons and an explicit marker that disabled execution. TACK-582 preserves the inventory and all 178 parent mappings. Its classifications alone could not authorize execution.

The captured classifications support comparison with the protected production exports. The exports remained outside the repository.

| Observed class | Count |
| --- | ---: |
| Valid issue-under-issue roots | 82 |
| Nodes with several direct parent edges | 178 |
| Comments with missing parents | 2 |
| Issues with a legacy `status` | 7 |
| Narrative `progress` values on those issues | 2 |
| Select values stored as labels | 3 |
| Printed references in parent properties | 2 |
| Missing parent with a surviving project edge | 1 |
| Scope-applicability exceptions | 2,638 |

## Parent and scope decisions

The reviewed decision preserved the 82 valid issue-under-issue roots and their descendants. A search access failure did not justify reparenting them. For nodes with several direct edges, the approved rule selected the unique closest linked container and removed only redundant direct containment links to its ancestors. It preserved project scope and unrelated relationships. The ancestor graph supported that grouping decision but could not establish the order of earlier writes.

The captured multiple-edge mappings comprised three patterns.

| Cases | Stored pattern | Reviewed mapping |
| ---: | --- | --- |
| 156 | The project was the stored parent and an edge identified an epic. | The decision selected the epic and removed the redundant project edge. |
| 18 | The stored parent already selected the epic and an extra edge identified the project. | The decision kept the parent property and removed the project edge. |
| 4 | CLYDE-345 through CLYDE-348 each linked the project, CLYDE-661 and CLYDE-344. | The decision selected CLYDE-344 and preserved the ancestry through CLYDE-661 to project CLYDE. |

The simulation changed 160 parent properties, kept 18 and removed 182 redundant edges. It retained all 360 ancestor associations without a cycle. Execution required another check against the actual graph and refusal of the affected repair if a dependency had changed or the closest parent was ambiguous.

The reviewed parent decisions used evidence from the individual records.

| Node | Evidence and reviewed result |
| --- | --- |
| MWAN-237 | Its printed parent and edge identified MWAN-308. The description recorded the operator's direction to move it on August 8. The decision selected the UUID of MWAN-308 and kept that edge. |
| MWAN-332 | Its printed parent identified MWAN-324. Its description connected the follow-up to that provider-set epic, while the old edge selected project MWAN. The October 26 approval superseded the temporary preservation decision. The repair selected MWAN-324 and kept the project scope. |
| TACK-278 | The old parent was absent. Both scope and the surviving edge selected project TACK. The decision restored that project's UUID as the parent and retained the edge, scope, Done state and other values. |

The 2,638 scope exceptions comprised 2,532 comments, 47 activities, 55 states and four projects. The former definition limited `scope_id` to `has_sequence_id`, but the validator made an exception for service-managed properties. An ordinary comment create and other service operations used scope more widely. The decision required preserving these scope values and verifying legitimate callers before classifying an individual value as invalid. The October 24 fixed-definition correction was separate from the 293 repairs. The seed now declares `scope_id` without the former feature restriction.

## Reports, states and select values

CLYDE-174 contained a 980-character `progress` report. CLYDE-188 contained a 1,108-character report. The approved mapping required a comment containing the complete original text on each original node. The repair had to use a stable identity, verify the stored text and attachment, and then remove the obsolete property. A retry had to reuse that comment. The decision preserved the available provenance and required the actual repair actor and time. It did not supply a missing historical author or creation time.

For a legacy `status`, the decision used property-level history to establish which value changed last. If that evidence could not establish order, the fallback used the latest explicit dated decision for that record. The preview had to identify the decision, source and date. The mapping normalized the selected value into `state_id`, required a write only when that value differed, and required verification before removing `status`.

The decision left a record unchanged when both kinds of evidence were absent, the latest decisions conflicted without a clear order, or the sanctioned value could not map unambiguously to the schema. A node-wide `UpdatedAt` could not establish property edit order. The captured audit rows did not record the missing property values or their order.

Two of the seven issues, CLYDE-174 and CLYDE-188, already agreed on Done. The reviewed mapping removed their duplicate `status` after checking both values. Five conflicted. MWAN did not define an Open state for this mapping. The decision did not authorize creating one for the obsolete literal.

The dated decisions determined the sanctioned state in the five conflicts.

| Node | Legacy value | Selected state | Evidence |
| --- | --- | --- | --- |
| MWAN-76 | `open` | Cancelled | The July 12 operator decision canceled obsolete pinning work. |
| MWAN-77 | `open` | Cancelled | The same July 12 decision canceled this pinning work. |
| MWAN-78 | `open` | Done | The August 7 correction identified the shipped watchdog implementation. |
| MWAN-79 | `open` | Cancelled | The July 12 decision recorded supersession by the documentation rewrite. |
| CLYDE-184 | `Todo` | Done | A May 2 completion comment preceded a node update by one second. |

The approved mapping required the current metadata to contain each destination key. It selected `high` for CLYDE-184 and CLYDE-185, which stored the `priority` label `High`, and `medium` for CLYDE-188, which stored `Medium`. It did not authorize general case-insensitive conversion, new options or changes to unrelated values.

## Comments with missing parents

The `Duplicate of TACK-252` notice, `019e6f65-01f2-7c34-8bc2-a11e09895740`, had lost its parent. The reviewed mapping attached the existing comment to TACK-252. It required preserving the original UUID, text, creator, creation time and unrelated relationships while correcting the parent property, scope and matching edge. Verification required normal reads and search, and the audit event had to identify the actual repair actor and time. A retry could not replace or duplicate the comment.

The description identified `Smoke comment` as a post-deployment test fixture. Its UUID was `01a0bfcb-461c-799d-923f-9148c01f9253`. Its reviewed deletion required a protected export of that exact record, its properties, relationships and available provenance outside the repository. The repair had to verify readability and the current revision before deleting only that UUID. A failed export or changed source had to prevent deletion. Verification required normal reads and search, preservation of other records, and recognition of an already completed deletion on retry. TACK-582 recorded the verified export and the permanent `node.delete` event.
