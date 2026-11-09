# Frozen node repair manifests

Before execution, repair reviewers must scan the authoritative node records for additional classes. The repair inventory must include every scanned case. Reviewers must report each unrecognized class without automatically mapping it.

The reviewed manifest must enumerate every scanned case and record its decision, including cases that do not need a change and cases that remain unresolved. It must distinguish observations, inferred intent and approved decisions. Reviewers must reconcile this manifest with the complete inventory. They must check each frozen entry against its reviewed decision and account for approved changes that the frozen files omit.

The command freezes only the entries in its request. Each entry identifies its node, type, repair ID and classes and includes at least one evidence reference. The parser accepts an `executable` entry only if it declares a change. An `unresolved` entry requires a reason and must not declare a change. The parser does not accept `unchanged` as an input status.

The parser checks evidence references but does not require a dated decision item. A valid frozen file may cover only a subset of the reviewed manifest. Validation cannot establish approval or complete scan coverage. Unknown or unreviewed actions must not execute.

TACK-582 superseded the TACK-220 repair classes and the TACK-221 reporting task. Repairs must reuse the generic TACK-206 tooling and its audit gate. Implementers must add only missing general capabilities. A one-time repair must preview without mutation by default and require explicit execution through the existing command gates. Inventory review compares those classes with the actual records.

A search exclusion alone does not establish a data violation.

A frozen entry stores the evidence needed to check its source.

- The repaired node's primary record bytes and digest.
- The source ID, type and target ID of each incoming and outgoing relationship.
- The related node IDs.
- The parent chain.
- The graph digest.
- The organization's metadata revision.

The manifest digest identifies the exact contents reviewed for execution. For each executable change, reviewers must record the following evidence:

- The complete before and after values.
- The affected relationships.
- The available provenance.
- The reason for the change.

Before any write, the command checks the manifest digest, validates every frozen source and checks every entry, including duplicate IDs and conflicting declarations. If any of those checks fails, it rejects the whole manifest.

During execution, a conflict with current records or a checkpoint stops the affected entry. Other independently reviewed entries can proceed.

The freezer records a related node's primary digest only when it finds that primary in the repaired node's organization. A missing or out-of-organization primary leaves an empty digest beside the ID. The ancestor reader includes a missing ancestor's ID in the chain before it stops.

The graph check requires the repaired node to exist with the declared type. Delete and unresolved entries skip the other graph checks at this stage. The protected export stores the relationship payloads. Before deleting the node, the command compares the exported payloads with the current incoming and outgoing relationships.

`ops backfill once-node-integrity` declares TACK-582 and a removal deadline of December 31, 2026 UTC. Its installed release must still list the command and its parameters. Exactly one of `--request` and `--manifest` selects an absolute path inside `TACK_REPAIR_DIR`. By default the command previews without changing product records. A request freezes a manifest only in a dry run. A manifest preview validates the frozen contents and reports each entry against its checkpoint and, when work remains, its current source. Execution requires a frozen manifest, explicit production approval for that manifest when applicable, and the global `--execute` flag. The operator identity and audit gates apply to each invocation.

Preview handles each entry in this order.

Preview outcomes:

| Entry | Preview behavior |
| --- | --- |
| Unresolved | It returns `unresolved` before reading a checkpoint. |
| Checkpoint with another primary digest, node ID or a phase the entry does not recognize | It returns `conflicted`. |
| Matching terminal checkpoint | It returns `completed` when the entry does not require another phase. It does not reread the node, its relationships or current metadata. |
| Work remains | It compares current primary, graph and metadata evidence with the frozen entry. A mismatch produces `conflicted`. Matching evidence produces `in_progress` with a checkpoint or `planned` without one. |

The manifest must combine overlapping approved patches for one node and validate the complete result before writing. If an unresolved field prevents validation, the node must remain unchanged. A repair must preserve unrelated identities, properties, timestamps, provenance and relationships.

Each mutation must reconcile these representations atomically:

- The primary record.
- The read view.
- The resolution record.
- The indexes.
- The claims.
- The matching edges.
- The desired search work.
- The audit intent.

A node mutation commits its checkpoint with the database change. The export phase writes and verifies the file before recording its checkpoint. Deletion reads the export again and compares it with the checkpoint and current relationships before deleting the node. An interrupted run resumes from that checkpoint.

Execution reports the phases committed for each entry and one of these outcomes:

- `applied`
- `unchanged`
- `conflicted`
- `unresolved`
- `failed`

An `unchanged` result means the entry had already completed and this run did not commit another phase for it. Verification requires the following evidence:

- Per-class counts and distinct-node counts.
- Normal readback of every changed record.
- Verification of report attachments.
- Graph simulation.
- Another broad scan.
- Search checks.

A second preview must not propose repeated changes or duplicate comments. A successful exit alone cannot establish that every entry completed.
