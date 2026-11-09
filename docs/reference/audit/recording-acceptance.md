# Audit recording acceptance requirements

Acceptance and activation must satisfy the [complete audit recording contract](2026-10-21-audit-completeness-design.md).

## Acceptance evidence

Acceptance must use the existing Go harness and real temporary dependencies through actual registered public operations. Production data and credentials must not supply fixtures. A tool-to-verb check resolves a list of tool names. A separate source check looks for references to declared verbs. Neither check executes the handlers. Declarations, log messages, helper-only tests, mapping checks, skipped integration suites, and unavailable databases do not prove accepted behavior. Provider operations require the existing provider acceptance environment.

1. Acceptance tests must create, edit, replace, move, and delete records. The tests must compare permanent evidence with source and committed values, including relationships and removals.
2. Acceptance tests must run seed, repair, backfill, account, token, and membership operations. The tests must establish that event failure prevents the corresponding transaction from committing.
3. Acceptance tests must run each read class:

   - The tests must run gets.
   - The tests must run lists.
   - The tests must run searches.
   - The tests must run prompt reads.
   - The tests must run protocol reads.
   - The tests must run health reads.
   - The tests must run diagnostic reads.
   - The tests must run operator reads.
   - The tests must run provider reads.

   The tests must verify each required part of the evidence:

   - The tests must verify the actor.
   - The tests must verify the selection.
   - The tests must verify the complete targets.
   - The tests must verify the exact digest of the returned payload.
   - The tests must verify the outcome.

   Recording failure must prevent result release.
4. Acceptance tests must exercise failed authentication, malformed input, denied access, absent targets, and backend errors. The tests must verify safe typed reasons and verified versus unauthenticated identity.
5. Acceptance tests must race finalization and recovery in two processes. The tests must resume the stale owner and require one terminal event plus refusal of the stale response. The tests must check admission retry expiry and published-receipt cleanup.
6. Acceptance tests must interrupt delivery after Kafka acknowledgment and before queue deletion. The tests must require permanent deduplication after restart and digest-based confirmation after uncertain finalization commits. On disposable QA, the tests must exercise the application recorder during a broker outage and restore Kafka. The tests must compare the spilled event IDs and original times with the permanent rows after relay delivery. The tests must reconcile the recovery count with the events stored during that outage.
7. Acceptance tests must exercise large old and new values, read manifests, and interruption across part boundaries. The tests must require complete reconstruction, bounded memory, and rejection of missing, reordered, conflicting, or altered parts.
8. Acceptance tests must export both formats. The tests must alter targets, deltas, and manifests and require verification failure. The tests must verify protected-value separation and redaction without breaking chain verification. The tests must submit a format 2 event without an outcome through the real outbox, relay, and consumer. The tests must require the ledger to reject it and the dead-letter reader to return its payload and a missing-outcome error.
9. Acceptance tests must verify that ordinary runtime authentication and recording failures cannot enable the setup exception. The tests must force a response failure after a mutation commits. The tests must verify both the permanent change event and the separate partial result under the same operation ID, including the failed request and the committed database step. The tests must exercise external recovery and background attribution.
10. Acceptance tests must execute every reviewed runtime operation and require permanent-evidence assertions. Missing executable coverage must fail the required coverage check. New audit verbs and user-visible paths require `ops qa datagen` coverage in the same change. Acceptance must include the scale measurements and a separate installation capacity report.

QA acceptance requires a permanent `mcp.resource_read` event for the seed's authenticated read of `tack://getting-started`. With `ops qa datagen seed --execute --commit` on an allowed target, the generator makes that read first with the first actor's bearer token from the first workspace. If the seed lacks that workspace or actor, it logs the reason and skips the read. Without `--execute`, the command previews the operation. With `--execute` alone, the dry-run driver returns a synthetic result without sending an MCP request.

## Compatible activation

Schemas, consumers, readers, and exports must support the new format before any writer enables it. Release A installs that support while old writers retain their format. The release operator must identify every consumer through deployment inventory and Docker Compose service labels. Every consumer instance must pass deployment verification, and Release A must pass its QA and production checks, before Release B enables pending reads and complete writers. The mixed-version acceptance test must refuse activation with two consumers when one still runs the old version. Both releases can use the existing shared image-tag convention. Rollback must retain Release A or newer consumers and preserve queued new-format events.

QA must validate each complete group before production activation. Runtime audit coverage and applicable integration checks must pass before merge. The acceptance ticket must identify the deployed revisions and observed permanent evidence. These observations establish live acceptance; code, review verdicts, image availability, and historic run dates do not establish deployment or current behavior.
