# Existing node backfill specification

TACK-582 repairs existing records after the [node integrity contract](2026-10-16-node-integrity.md) establishes the correct interpretation. This draft classifies every observed case. It does not authorize production execution, inferred reparenting, or deletion.

## Interpret the inventory correctly

The October 16 pull read all 5,090 materialized nodes in the main organization across all 11 registered node types. It directly compared 454 suspect primary records with their views; every compared property map matched. Three referenced parent UUIDs lacked primary, view, and resolution records.

Search verification reported 450 exclusions and zero stuck work items. Hierarchy failures affected 443 excluded nodes. The remaining seven exclusions involved undeclared properties. Failure classes overlap; their counts must not be added as distinct nodes.

The complete classification covers 2,909 distinct records after combining the suspect set with metadata applicability exceptions. Each proposed record includes evidence, intended action, source hash, review reasons, and an execution-disabled marker. [TACK-582](https://tack.home.goodkind.io/browse/TACK-582) contains the classified inventory and all 178 proposed parent mappings. Production exports remain outside the repository.

| Observed class | Count | Interpretation |
| --- | --- | --- |
| An issue has another issue as its parent. | 82 failure roots. | This is supported nesting, not a data violation. |
| A node has several direct `child_of` edges. | 178 nodes. | The stored edges disagree with the single canonical-parent contract. |
| A comment references an absent parent. | Two nodes. | Each needs a separate intent-based decision. |
| Legacy `status` values remain. | Seven values across seven issues. | Two match the sanctioned state; five conflict. |
| Narrative `progress` values remain. | Two values on two of those seven issues. | Preserve the complete reports as comments. |
| Select values use display labels. | Three values. | Normalize only the identified values to existing option keys. |
| Printed parent text remains in a UUID property. | Two nodes. | Resolve the intended existing parent and reconcile its edge. |
| A parent UUID is absent, but scope and an edge select the current project. | One node. | Restore the supported parent only after reviewing that evidence. |
| `scope_id` applicability disagrees with current runtime behavior. | 2,638 nodes. | Correct the metadata/runtime contract and preserve legitimate scope values. |

## Preserve valid nesting and scope

Leave the 82 valid issue-under-issue roots and their descendants intact. Correct the generic reader and access logic rather than reparenting them to satisfy an old product-type rule.

For the 178 multiple-edge cases, the proposed rule selects the unique most specific linked container and retains project scope. This rule infers grouping intent from the observed ancestor graph. It does not prove the historical order of writes.

| Existing pattern | Cases | Proposed mapping |
| --- | --- | --- |
| `parent_id` selects the project, and an edge selects an epic. | 156. | Select the epic as direct parent and remove the redundant project edge. |
| `parent_id` already selects the epic, and an edge also selects the project. | 18. | Retain the parent property and remove the redundant project edge. |
| CLYDE-345, CLYDE-346, CLYDE-347, and CLYDE-348 each link the project, CLYDE-661, and CLYDE-344. | Four. | Select CLYDE-344 as direct parent; retain its ancestor chain through CLYDE-661 to project CLYDE. |

The complete proposal changes 160 parent properties, leaves 18 unchanged, and removes 182 redundant edges. It preserves project scopes. The graph simulation remains acyclic and represents all 360 original ancestor associations. Execution must repeat those checks against the current records and stop on a changed or ambiguous graph.

## Preserve narrative reports

CLYDE-174 contains a 980-character `progress` report. CLYDE-188 contains a 1,108-character report. The user selected this mapping:

1. Create a comment containing the complete original report on the original node.
2. Verify the stored comment text and attachment.
3. Remove the obsolete property only after that verification succeeds.

Use a stable repair identity and durable checkpoint to prevent duplicate comments after retries or interruption. Preserve original source evidence, but record the actual repair actor and time. Do not invent a historical author or creation date.

## Reconcile legacy states without inventing history

The user selected the most recently changed property value as the winner. Normalize that value into `state_id`, write only when it differs, verify the result, then remove `status`.

CLYDE-174 and CLYDE-188 already agree on Done. Their proposal removes only the duplicate legacy status after checking the current values. Five conflicts remain:

| Node | Legacy value | Current sanctioned state | Supporting evidence for the current state |
| --- | --- | --- | --- |
| MWAN-76 | `open` | Cancelled. | The July 12 operator decision cancels the obsolete pinning work. |
| MWAN-77 | `open` | Cancelled. | The July 12 operator decision cancels the obsolete pinning work. |
| MWAN-78 | `open` | Done. | The August 7 correction states that the shipped watchdog architecture implements the task. |
| MWAN-79 | `open` | Cancelled. | The July 12 decision records supersession by the documentation rewrite. |
| CLYDE-184 | `Todo` | Done. | A May 2 completion comment precedes a node update by one second. |

The proposal retains those current sanctioned states and removes the old properties. That proposal requires approval of an evidence fallback because the available audit rows lack property values and edit order. Node-wide `UpdatedAt` cannot establish which property changed last. MWAN does not currently define an Open state; a decision that selects `open` also requires an explicit sanctioned-state mapping.

Until the fallback is chosen, leave these conflicting fields unchanged and report them as unresolved. Implementing future audit deltas does not resolve these historical cases.

## Normalize the identified select values

| Node | Stored value | Proposed sanctioned key |
| --- | --- | --- |
| CLYDE-184 | `priority: High` | `high` |
| CLYDE-185 | `priority: High` | `high` |
| CLYDE-188 | `priority: Medium` | `medium` |

Check the current options before applying these mappings. Do not infer a general case-insensitive conversion for unrelated values or introduce new enum options.

## Resolve each malformed or absent parent

| Node | Evidence | Proposed action |
| --- | --- | --- |
| MWAN-237 | The parent property contains `MWAN-308`; the edge already selects that epic. The description records an operator-directed move on August 8. | Resolve the printed parent to its existing UUID and retain the matching edge. |
| MWAN-332 | The parent property contains `MWAN-324`; its edge still selects project MWAN. The description connects this follow-up to the provider-set epic. | Review that inferred intent, then select MWAN-324 as parent and retain MWAN as scope. |
| TACK-278 | Its parent UUID is absent; its scope and surviving edge both select current project TACK. | Review a parent-property repair to the existing TACK project and preserve its Done state. |
| Comment `019e6f65-01f2-7c34-8bc2-a11e09895740` | The text says `Duplicate of TACK-252`; the original parent is absent. | Review reattachment to TACK-252 while preserving the comment ID, text, author, and time. |
| Comment `01a0bfcb-461c-799d-923f-9148c01f9253` | The text is `Smoke comment`; the description identifies a post-deploy smoke fixture. | Export it and propose retirement of exactly this fixture. Deletion requires approval. |

## Preserve the scope applicability exceptions

The 2,638 scope exceptions comprise 2,532 comments, 47 activities, 55 states, and four projects. The definition applies `scope_id` only to `has_sequence_id`, while a service-managed property exemption permits wider use. A current ordinary comment create confirms that this disagreement still occurs.

Classify these records as metadata/runtime disagreement. Do not delete their scope values. TACK-580 must establish a consistent fixed contract and test its legitimate callers before a data repair classifies any individual value as invalid.

## Execute only reviewed mappings

The manifest must distinguish observed source values from inferred intent and approved decisions. It must enumerate every scanned case, including unchanged and unresolved records. A broad node scan must detect additional classes before execution; an unrecognized class must produce an explicit report rather than an automatic mapping. Combine overlapping approved patches for one node into one valid resulting record. If an unresolved field prevents full-result validation, leave that node unchanged rather than bypassing the validator.

The [implementation plan](../plans/2026-10-16-node-integrity.md) specifies the command boundary, transaction checks, restart handling, and QA proof. Preserve unrelated properties, identities, timestamps, provenance, and relationship types. Reconcile primary records, read views, resolution records, secondary indexes, and desired search work through the existing atomic write contract.

After execution, compare per-class and distinct-node counts, read every changed record, verify report comments, simulate the resulting graph, and rerun the broad scan and search checks. A second preview must propose neither repeated changes nor duplicate comments. Unresolved cases must remain explicit; a healthy command exit does not establish a complete repair.
