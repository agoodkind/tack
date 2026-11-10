# Node Reference Uniqueness Implementation Plan

<a id="global-constraints" name="global-constraints"></a><a id="how-this-plan-is-executed" name="how-this-plan-is-executed"></a><a id="file-structure" name="file-structure"></a><a id="task-9-end-to-end-regression-for-both-duplicate-mechanisms" name="task-9-end-to-end-regression-for-both-duplicate-mechanisms"></a><a id="self-review" name="self-review"></a>

Read the [project contract](../../../AGENTS.md) for project-wide requirements. The [reference uniqueness requirements and acceptance cases](../../reference/nodes/2026-08-05-node-reference-uniqueness-design.md) cover cross-type creates and scope repairs within one type.

<a id="task-1-reference-template-domain-types-render-parse-and-counter-derivation" name="task-1-reference-template-domain-types-render-parse-and-counter-derivation"></a><a id="task-4-render-reference-keys-in-the-service-and-allocate-from-the-derived-counter" name="task-4-render-reference-keys-in-the-service-and-allocate-from-the-derived-counter"></a><a id="task-8-write-a-starting-reference-template-in-the-seed" name="task-8-write-a-starting-reference-template-in-the-seed"></a>

Read about [reference templates, counters, and seed data](../../explanation/nodes/node-reference-uniqueness-design.md#templates-and-counters).

<a id="task-2-reference-key-families-and-in-transaction-storage-helpers" name="task-2-reference-key-families-and-in-transaction-storage-helpers"></a><a id="task-3-enforce-reference-keys-on-every-write-path" name="task-3-enforce-reference-keys-on-every-write-path"></a><a id="task-5-point-read-resolution-with-a-fail-loud-scan-fallback" name="task-5-point-read-resolution-with-a-fail-loud-scan-fallback"></a>

Read about [reference claims, mutations, and lookups](../../explanation/nodes/node-reference-uniqueness-design.md#claims-and-lookups).

<a id="task-6-duplicate-reference-detect-operation" name="task-6-duplicate-reference-detect-operation"></a><a id="task-7-renumber-and-backfill-operation" name="task-7-renumber-and-backfill-operation"></a>

Read the [duplicate detection and repair requirements](../../reference/nodes/2026-08-05-node-reference-uniqueness-design.md#duplicate-detection-and-repair).

### Task 10: QA validation, blast-radius audit, and production rollout

For steps 7 and 8, read the [backup, restore drill, mapping review, and separate production approval requirements](../../reference/nodes/2026-08-05-node-reference-uniqueness-design.md#acceptance-evidence). [TACK-342](https://tack.home.goodkind.io/browse/TACK-342) records the complete old-to-new mapping for the production repair. [TACK-391](https://tack.home.goodkind.io/browse/TACK-391) records the repair procedure.

### Task 11: Remove the scan fallback

Read the [production repair records and the conditions for retiring the property scan](../../reference/nodes/2026-08-05-node-reference-uniqueness-design.md#production-repair-and-reconstruction-records).
