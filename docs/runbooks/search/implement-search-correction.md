# Validate a search correction

Use public QA reproduction and current source evidence to choose a correction. A passing new regression does not complete the full search acceptance contract. This procedure does not authorize production source-data repairs.

## Reproduce the defect

1. Preserve the failing public request, expected content, exact deployment and model configuration, and plausible distractors. Reproduce the same content shape on QA before selecting a fix.
2. Capture indexed text, projection identity, sparse fields, inference output, structured access and type filters, lexical boosts, and rank traces. Identify the failed production operation from those observations. Do not add phrase-specific branches, maintained synonyms, a replacement tokenizer, or weaker rank bounds.
3. For missing content, compare explicit declarations and supported defaults. Verify generic hierarchy and separate project scope. Preserve valid content beside invalid, undeclared, or inapplicable properties. Keep warnings distinct from authorization exclusions and operational errors.

## Correct metadata and source data safely

1. For changes to an active projection, follow [Verify corrections after activation](verify-projection-corrections.md). For node integrity requirements N3, N5, and N8 through N10, review source-data repairs separately against the [node integrity specification](../../reference/nodes/2026-10-16-node-integrity.md) and the [node and audit repair plan](../../reference/nodes/2026-10-16-node-integrity.md#source-repair-and-release).
2. If current source data needs a new repair, prepare a new manifest of affected records under the [source-data backfill contract](../../reference/nodes/2026-10-16-node-backfill.md#frozen-manifests). Deploy compatible audit, metadata, and repair prerequisites to QA while ordinary-write checks remain active. Correct `scope_id` applicability and verify legitimate comment creation before strict full-record activation. Execute only the reviewed QA repair manifest.
3. Verify complete repair coverage, retries, broad scans, nested-parent operations, native audit queries, and affected refresh. Activate strict validation only after verified cleanup. Exercise concurrent writes between scanning and activation and reject stale activation evidence.
4. If the correction requires a new production repair, verify the QA sequence and obtain approval for the frozen production manifest before execution. Recheck source changes and repair failures before strict activation. Do not undo repaired values or audit history with application rollback.

## Prove the complete result

1. Maintain one evidence row per [acceptance criterion](../../reference/search/2026-09-19-search-acceptance.md), with revision, environment, command, input, expected result, observed result, and artifact. Mark unrun criteria unverified.
2. Repeat the accepted relevance corpus three times and after reindexing. Include lexical controls, inference reuse, production wording, and the preserved regression. Explain any corpus-count mismatch.
3. Run every feature partition, dedicated group, and affected repeated tail through the current test targets. Use the required Mac runner and cluster opt-ins. Preserve tests and bounds. A skipped scenario does not supply evidence.
4. Verify installed app and consumer digests, required stores, model readiness, public results, current permission, continuation, audit deltas, and fresh exclusion and stuck-work reports. Classify unresolved exclusions without deleting content to force an empty count.
5. Run separate public search-only and mixed workloads with declared inputs and documented product thresholds. Count offered and completed queries separately from writes. Measure freshness, backlog age, latency, errors, memory, CPU, and disk. Ordinary soak and process-scaling tests do not establish search capacity.
6. Follow the [store recovery procedure](../recovery/verify-restores.md) for the authorized production drill. Include source writes and pending search work. Record merge, configuration apply, deployment, repair, workload, acceptance, and cleanup separately.

## Coordinate shared ownership

1. Use the [Tack provider specification](../../reference/tack-provider.md#provider-ownership) for application status and resource ownership. Use existing providers for generic state. Preserve first-boot ordering and explicit destructive initialization guards. Verify fresh deployment, import, drift, interrupted apply, removal, locks, secret handling, an unchanged subsequent plan, and a second apply without extra work on disposable systems.
2. Check the reviewed revisions of the [shared guest specification](https://github.com/agoodkind/configs/blob/8bed552067f2ae61dac78cca8aececf5c30f0d0c/docs/superpowers/gueststate/spec.md) and [shared guest plan](https://github.com/agoodkind/configs/blob/8bed552067f2ae61dac78cca8aececf5c30f0d0c/docs/superpowers/gueststate/plan.md) before applying changes. Adopt QA before production. Retire a previous writer only after each field has an established owner. General search resources must not independently switch Tack-owned aliases or delete indexes. Verify absent-guest handling with the Proxmox owner on QA and verify activation there.
3. Continue independent implementation while a guest or deployment lock waits. Preserve verified work and unresolved acceptance separately from unavailable resources or measurements.
