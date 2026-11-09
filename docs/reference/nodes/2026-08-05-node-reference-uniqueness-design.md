# Reference uniqueness requirements

A rendered reference must identify exactly one node within its organization and template. Independent numbering by type requires the type to appear in the printed reference. On a conflict, the diagnostic must identify both the existing owner and the proposed node. The current FoundationDB error identifies only the existing owner. Generic addressing must not depend on built-in type names, fixed property names or built-in templates. Public rendering, parsing and lookup must agree for every supported template.

## Duplicate detection and repair

The `ops batch reference.duplicates` read must cover every organization and project. It must record durable admission before scanning and stop if admission fails. It must finalize read evidence before releasing any result. The detector must apply to every declared template. It renders each template it finds and groups nodes across types by organization, template name and rendered value. The reviewed inventory must record each group's rendered reference, the project for each candidate, every candidate UUID and its creation time.

Without `--execute`, the command previews the read without scanning. With `--execute`, it scans the organizations it obtains from `org_members`. Inside the read callback, it logs each group's organization, template name, rendered reference and sorted candidate UUIDs, then the group count. These logs precede audit finalization. It does not submit those UUIDs to the captured result for the read event. The report does not include project identifiers or creation times. A rendering failure produces a warning and omits the value. A zero group count cannot account for omitted organizations or unrenderable values.

Batch operation names use lowercase, dot-separated components. A component can contain underscores, as in `repair.reference_uniqueness`. `ops.Register` rejects empty names, missing implementations and duplicate names. The batch CLI uses the registered name.

Renumbering changes references that people may have written down. Before the first change, the repair must print the complete proposed mapping and record every reviewed old and new reference in the permanent ledger. Reviewers must compare the mapping with the audited group list. The repair ticket must retain the approved mapping. An extra rename, a missing group or an unexpected result requires a new inventory before a separate execution.

`ops repair reference-uniqueness` previews by default. It prints a placeholder identifying the next counter value, without allocating a concrete replacement number. Execution requires the global `--execute` flag. The run recorder operates after the mutations. Neither output provides the required pre-application ledger mapping.

The design requires repair of duplicate references for every template a deployment declares, including secondary identifiers. Execution first raises derived counters below the observed maximum for their key. It then groups duplicates by the template and value that collided.

For each node selected for renumbering, the current repair chooses that type's primary template. Preview and execution both fail if the type lacks a primary template with a generated property. Execution changes that property and submits only the newly rendered primary claim as the replacement set in the node update. After renumbering, execution reasserts all templates' claims. Preview skips that last step. If two nodes share a secondary identifier independent of the primary's generated property, changing the number does not resolve their secondary collision.

The default `--keep oldest` retains the lexically smallest UUID string in the group. `--keep newest` retains the largest. UUIDv7 encodes time in milliseconds. The command does not check UUID versions or compare creation timestamps. A random legacy UUID does not encode creation time. UUIDv7 values from the same millisecond do not guarantee the actual order of two creates. The storage constraint rejects a conflicting claim regardless of the repair policy.

A formerly duplicated address identifies the node that kept it. It cannot forward to one successor because several nodes used it. TACK-365 separately proposes forwarding for a move from a previously unique address. Under that proposal, the resolver would first check the direct claim and would consult a forwarding record only after a direct miss.

## Production repair and reconstruction records

The printed mapping identifies the UUID and replacement address for each renamed node. If an archived note gives only an address that several nodes shared, the mapping cannot establish which node its author meant. The production execution and ledger cross-check appear in [TACK-342](https://tack.home.goodkind.io/browse/TACK-342) and [TACK-391](https://tack.home.goodkind.io/browse/TACK-391).

| Date | What the production evidence establishes |
| --- | --- |
| August 7, 2026 | The operator authorized a backup followed by execution. The backup completed. The operators did not run the restore drill. Preview and execution ran back to back without a separate human review of the mapping. Detection found 102 groups in seven projects. The run renamed 103 nodes, seeded 11 counters, backfilled 1,620 keys and ended with zero duplicate groups. |
| August 8, 2026 | The ledger cross-check did not find contemporaneous rows for the August 7 repair. An app still running the old image issued a duplicate `TACK-403`. After an image update, a separate repair renamed that node `TACK-420`. |

The reconstruction covered 104 renames, 11 counter seeds, 3,241 reference keys and two production seed runs. It required the ordinary audit path to append each event in current chain order, mark it as reconstructed, retain its historical occurrence time and cite the printed mapping and execution log. A separate event had to identify the operator who ran reconstruction and what it covered. The resulting chain had to pass verification.

The testbed executed the reconstruction and verified its chain. A repeat execution did not add rows. Production ran only a dry run. Five classes matched their expected counts. One class produced 1,619 of 1,620 expected key backfills. The dry run did not write reconstructed events. [TACK-429](https://tack.home.goodkind.io/browse/TACK-429)

Retiring the property scan required complete reference claims and zero duplicate groups in both QA and production. A sequence lookup now returns not found for a missing claim even if the primary record exists.

## Acceptance evidence

Reference acceptance requires real create, update, delete and lookup operations against FoundationDB. Domain tests verify rendering, parsing and counter-key derivation without a database. The `make test-integration` target enables the `integration` build tag and runs the suite with single-node FoundationDB and YugabyteDB containers for these cases.

| Case | Required observation |
| --- | --- |
| Cross-type create | An ordinary create of an epic followed by an issue in one project must assign successive numbers and render different references. Confirm the detector includes the fixture organization and reports zero groups. |
| Occupied destination in a same-type scope repair | A resident issue owns `QAT-1`. A second issue has the destination project as its parent, the original project as its scope and sequence 1. `RepairSequenceScopeIDs` derives the destination scope from its parent and preserves the sequence. The occupied claim must cause `ErrConflict` and zero repairs. The stale issue must retain its original scope, and the resident must retain `QAT-1`. |
| Unoccupied destination | The stale issue owns `FAN-1`. When `QAT-1` is available, the same repair must transfer the issue's claim to `QAT-1` and release `FAN-1`. |
| Claim lifecycle | A retry by the same owner succeeds. An update or deletion releases the old value, and lookup cannot return its former owner. A rejected create must not leave a partial node. |
| Nonstandard property | For a template that generates `property(number)`, creation must allocate into `number`, rendering must read `number`, and resolution must use `number`. These operations must not read the literal `sequence` property on this path. |
| Per-type numbering and parsing | A template containing `node_type` permits an epic and an issue to use the same number and prints distinct references. Parsing must recover the rendered parts, including a hyphenated scope, and reject a missing delimiter. |
| Legacy cross-type collision | In a project with identifier `FAN`, store an epic and an issue with sequence 1 and the same project parent and scope. Detection must report exactly one group under template `reference` and value `FAN-1`, with both fixture UUIDs. |

If a create repeats the original cross-type collision and attempts to issue the same reference, the design requires an invalid-argument error. Ordinary creates that receive successive numbers do not exercise this refusal. A separate storage test must submit the same rendered reference for two nodes and verify that the second claim fails with `ErrConflict`. The storage result alone cannot establish the error that a caller receives from a create.

QA and production acceptance require a lookup of every rendered reference after repair, including newly assigned, retained and otherwise unchanged addresses. Each lookup must identify exactly one node with the expected UUID. The detector groups rendered values from node views without checking forward claims. A zero-group report cannot verify that those lookups succeed.

QA acceptance requires a disposable environment, detection of both duplicate mechanisms and a complete candidate UUID inventory. The preview must leave data unchanged. The executed mapping must match permanent audit evidence. Detection must report zero groups after the repair. QA must then create a new issue through MCP in the repaired project and verify that its number exceeds that counter's previous maximum. A subsequent duplicate scan must again report zero groups. A newly provisioned QA environment must also pass detection.

A future production renumber requires an authorized inventory of every project and organization, a fresh backup, a successful restore drill and separate approval of the complete reviewed mapping before execution. Final detection must report zero groups. The retained mapping and counts must agree with the ledger.

Each rename, counter change and reference claim requires atomic evidence of the targets, before and after values, actor and outcome. If execution fails partway, the operator must be able to account for each committed change, including after interruption or retry. The command commits each rename separately. A run report alone cannot prove that each change and its required evidence committed together.
