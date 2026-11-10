# Node repair manifests and reviewed mappings

[TACK-582](https://tack.home.goodkind.io/browse/TACK-582) records completion of all approved production repairs. The repeat preview confirms the recorded checkpoints.

Completed repair results:

| Measurement | Count |
| --- | ---: |
| Approved production repairs | 293 |
| Manifests in the repeat preview | 34 |
| Completed entries in the repeat preview | 336 |
| Distinct nodes in the repeat preview | 293 |

Separate normal readback, permanent audit rows, graph verification and search checks documented the effects of the repairs. The archived classifications explain those receipts. The completed repairs must not run again.

## Frozen manifests

Repair execution must satisfy the [frozen manifest requirements](repair-manifests.md).

## Interpreting the captured inventory

The October 16 inventory and the final repair list measured different populations. The inventory measured records in the main organization.

Inventory measurements:

| Measurement | Result and scope |
| --- | --- |
| Views read | The inventory read 5,090 views across 11 registered types. |
| Primary records compared with views | The inventory compared 454 suspect primary records. Every property map matched. |
| Missing parent records | Three parent UUIDs lacked primary, view and resolution records. |
| Search exclusions | Search reported 450 exclusions: 443 concerned the hierarchy and seven concerned undeclared properties. |
| Stuck search items | Search reported zero stuck items. |
| Classified records | The inventory classified 2,909 distinct records. Classes overlapped. |

Each captured record included these items:

- The evidence.
- The intended action.
- The source hash.
- The review reasons.
- An explicit marker that disabled execution.

TACK-582 preserves the inventory and all 178 parent mappings. Its classifications alone could not authorize execution.

The captured classifications support comparison with the protected production exports. The exports remained outside the repository.

Captured classifications:

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

The reviewed mappings distinguish the stored parent and containment edges.

Multiple-edge mappings:

| Cases | Stored pattern | Reviewed mapping |
| ---: | --- | --- |
| 156 | The project was the stored parent and an edge identified an epic. | The decision selected the epic and removed the redundant project edge. |
| 18 | The stored parent already selected the epic and an extra edge identified the project. | The decision kept the parent property and removed the project edge. |
| 4 | CLYDE-345 through CLYDE-348 each linked the project, CLYDE-661 and CLYDE-344. | The decision selected CLYDE-344 and preserved the ancestry through CLYDE-661 to project CLYDE. |

The simulation measured the proposed graph changes and preserved associations.

Simulation results:

| Measurement | Count |
| --- | ---: |
| Parent properties changed | 160 |
| Parent properties kept | 18 |
| Redundant edges removed | 182 |
| Ancestor associations retained without a cycle | 360 |

Execution required another check against the actual graph and refusal of the affected repair if a dependency had changed or the closest parent was ambiguous.

The reviewed parent decisions used evidence from the individual records.

Reviewed parent decisions:

| Node | Evidence and reviewed result |
| --- | --- |
| MWAN-237 | Its printed parent and edge identified MWAN-308. The description recorded the operator's direction to move it on August 8. The decision selected the UUID of MWAN-308 and kept that edge. |
| MWAN-332 | Its printed parent identified MWAN-324. Its description connected the follow-up to that provider-set epic, while the old edge selected project MWAN. The October 26 approval superseded the temporary preservation decision. The repair selected MWAN-324 and kept the project scope. |
| TACK-278 | The old parent was absent. Both scope and the surviving edge selected project TACK. The decision restored that project's UUID as the parent and retained the edge, scope, Done state and other values. |

The scope exceptions covered several node types.

Scope exceptions:

| Type | Count |
| --- | ---: |
| Comments | 2,532 |
| Activities | 47 |
| States | 55 |
| Projects | 4 |
| Total | 2,638 |

The former definition limited `scope_id` to `has_sequence_id`, but the validator made an exception for service-managed properties. An ordinary comment create and other service operations used scope more widely. The decision required preserving these scope values and verifying legitimate callers before classifying an individual value as invalid. The October 24 fixed-definition correction was separate from the 293 repairs. The seed now declares `scope_id` without the former feature restriction.

## Reports, states and select values

The two original nodes contained narrative `progress` reports.

Report lengths:

| Node | Characters |
| --- | ---: |
| CLYDE-174 | 980 |
| CLYDE-188 | 1,108 |

The approved mapping required a comment containing the complete original text on each original node. The repair had to use a stable identity, verify the stored text and attachment, and then remove the obsolete property. A retry had to reuse that comment. The decision preserved the available provenance and required the actual repair actor and time. It did not supply a missing historical author or creation time.

For a legacy `status`, the decision used property-level history to establish which value changed last. If that evidence could not establish order, the fallback used the latest explicit dated decision for that record. The preview had to identify the decision, source and date. The mapping normalized the selected value into `state_id`, required a write only when that value differed, and required verification before removing `status`.

The decision left a record unchanged when both kinds of evidence were absent, the latest decisions conflicted without a clear order, or the sanctioned value could not map unambiguously to the schema. A node-wide `UpdatedAt` could not establish property edit order. The captured audit rows did not record the missing property values or their order.

The reviewed mapping distinguished agreement from conflicting values among the seven issues.

Status comparison:

| Result | Issues | Reviewed action |
| --- | --- | --- |
| Agreement on Done | CLYDE-174 and CLYDE-188, two issues. | The mapping removed the duplicate `status` after checking both values. |
| Conflict | Five issues. | The dated decisions below determined the sanctioned state. |

MWAN did not define an Open state for this mapping. The decision did not authorize creating one for the obsolete literal.

The dated decisions determined the sanctioned state in the five conflicts.

Dated state decisions:

| Node | Legacy value | Selected state | Evidence |
| --- | --- | --- | --- |
| MWAN-76 | `open` | Cancelled | The July 12 operator decision canceled obsolete pinning work. |
| MWAN-77 | `open` | Cancelled | The same July 12 decision canceled this pinning work. |
| MWAN-78 | `open` | Done | The August 7 correction identified the shipped watchdog implementation. |
| MWAN-79 | `open` | Cancelled | The July 12 decision recorded supersession by the documentation rewrite. |
| CLYDE-184 | `Todo` | Done | A May 2 completion comment preceded a node update by one second. |

The approved mapping required the current metadata to contain each destination key.

The reviewed conversions replace the stored `priority` labels with these option keys.

Priority mappings:

| Node | Stored label | Destination key |
| --- | --- | --- |
| CLYDE-184 | `High` | `high` |
| CLYDE-185 | `High` | `high` |
| CLYDE-188 | `Medium` | `medium` |

The mapping did not authorize general case-insensitive conversion, new options or changes to unrelated values.

## Comments with missing parents

The `Duplicate of TACK-252` notice, `019e6f65-01f2-7c34-8bc2-a11e09895740`, had lost its parent. The reviewed mapping attached the existing comment to TACK-252. It required preserving the original UUID, text, creator, creation time and unrelated relationships while correcting the parent property, scope and matching edge. Verification required normal reads and search, and the audit event had to identify the actual repair actor and time. A retry could not replace or duplicate the comment.

The description identified `Smoke comment` as a post-deployment test fixture. Its UUID was `01a0bfcb-461c-799d-923f-9148c01f9253`. Its reviewed deletion required a protected export of that exact record, its properties, relationships and available provenance outside the repository. The repair had to verify readability and the current revision before deleting only that UUID. A failed export or changed source had to prevent deletion. Verification required normal reads and search, preservation of other records, and recognition of an already completed deletion on retry. TACK-582 recorded the verified export and the permanent `node.delete` event.
