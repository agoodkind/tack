# Activate strict node validation

Activation applies to every organization in the FoundationDB cluster. It makes ordinary writes validate the entire resulting node. A command cannot remove the activation record. Complete this procedure on QA before activating strict validation in production.

## Verify cleanup and deployed writers

1. Confirm that reviewed cleanup completed on this environment, every changed record passed readback, and a second preview did not propose further repairs. Use the existing receipts for completed work. Require a separately approved frozen production manifest for any new production repair. Keep strict validation inactive while an unresolved violation remains.

2. For an agent run, set the shell variables below to the actual agent and accountable operator identity.

   Identity variables:

   | Shell variable | Identity flag |
   | --- | --- |
   | `OPERATOR_SERVICE` | `--operator-service` |
   | `OPERATOR_ID` | `--operator-id` |
   | `OPERATOR_EMAIL` | `--operator-email` |
   | `OPERATOR_NAME` | `--operator-name` |
   | `OPERATOR_SESSION` | `--operator-session` |

3. On each guest running `app`, preview image verification and inspect the printed identity. The example checks both services. On an app-only guest, append `--service app` to the preview command in step 3 and the execution command in step 4.

   ```bash
   docker compose run --rm -T tack-ops \
     --operator-service "$OPERATOR_SERVICE" \
     --operator-id "$OPERATOR_ID" \
     --operator-email "$OPERATOR_EMAIL" \
     --operator-name "$OPERATOR_NAME" \
     --operator-session "$OPERATOR_SESSION" \
     ops deploy verify
   ```

4. After confirming the identity, verify every running replica on each guest. The command compares the expected `tack-server` and, where applicable, `tack-audit-consumer` image digests with the running containers. Require every check to pass.

   ```bash
   docker compose run --rm -T tack-ops \
     --operator-service "$OPERATOR_SERVICE" \
     --operator-id "$OPERATOR_ID" \
     --operator-email "$OPERATOR_EMAIL" \
     --operator-name "$OPERATOR_NAME" \
     --operator-session "$OPERATOR_SESSION" \
     --execute ops deploy verify
   ```

5. Verify that the deployed revision advances the write fence, a counter that detects concurrent changes, for every product writer, including subtree deletion and child moves. A scan cannot detect a writer that skips this step. On QA, require an intervening invalid write to prevent activation and confirm that a mutation already in progress retries against the activated rules.

## Run a fresh scan

1. Run the scan in a one-shot `app` container. The command opens the configured SQL pool and FoundationDB stores. Its operator audit recorder also needs its configured connection. The `tack-ops` container uses host networking for deployment verification and cannot resolve the bridge-only `fdb` name. On its first pass, the command reads primary records across all organizations and validates each node against its organization's schema.

   ```bash
   docker compose run --rm app \
     --operator-service "$OPERATOR_SERVICE" \
     --operator-id "$OPERATOR_ID" \
     --operator-email "$OPERATOR_EMAIL" \
     --operator-name "$OPERATOR_NAME" \
     --operator-session "$OPERATOR_SESSION" \
     ops verify strict-validation
   ```

2. Check the printed identity and read the JSON output. Confirm `dry_run` is `true`. The scan fields are inside `result`. The top-level `max_violations` limits the number of entries in `result.first_violations`.

   The fields establish the scan status and reported violations.

   Scan checks:

   | Field in `result` | Check |
   | --- | --- |
   | `already_active` | If true, the command returns the stored scan ID and record count without scanning. Stop here. |
   | `activated` | A dry run must return false. |
   | `scan_id` | Save the scan identifier. |
   | `records_validated` | Check the number of records the scan validated. |
   | `passes` | Check how many passes the scan needed. |
   | `violation_count` | Require zero before activation. |
   | `first_violations` | Inspect `org_id`, `node_id`, `node_type` and `reason`. The list sorts by organization ID and then node ID. |

3. Stop before activation if `result.violation_count` is positive. A completed dry run prints its report and exits successfully even when it finds invalid records. The default list includes up to 20 violations. Append `--max-violations=<n>` after `ops verify strict-validation`. The command accepts limits from 1 through 5000. A limit at least as large as `violation_count` lists all violations only when the count is within that maximum. When the count exceeds 5000, use the complete repair inventory to identify records the report cannot list. Repair the invalid records through a [reviewed frozen manifest](../../reference/nodes/2026-10-16-node-backfill.md#frozen-manifests) for this environment. Repeat the dry run and require zero violations before activation.

## Activate strict validation

1. After a clean scan, run activation with the verified identity. The activation command starts its own scan. It cannot reuse the dry run as evidence of the current state. It compares the current write-fence counters and organization metadata revisions in the same FoundationDB transaction that writes the permanent activation record.

   ```bash
   docker compose run --rm app \
     --operator-service "$OPERATOR_SERVICE" \
     --operator-id "$OPERATOR_ID" \
     --operator-email "$OPERATOR_EMAIL" \
     --operator-name "$OPERATOR_NAME" \
     --operator-session "$OPERATOR_SESSION" \
     --execute ops verify strict-validation
   ```

2. Check `result.activated`. A new activation requires `true`. When a write-fence bucket changes, the command revalidates affected nodes and records that depend on their parents or reference targets. When an organization's metadata revision changes, the command discards that organization's cached schema and revalidates its records against the current schema.

3. If five passes each detect changed evidence, the command fails without activating. Start a fresh scan. A positive violation count also makes execution fail and print the report. Complete reviewed repairs before retrying. After a successful activation, a later invocation reports `result.already_active` without rescanning.

## Record the result

1. Save the Tack commit, installed image digest and verified operator identity with the activation evidence.
2. Save `result.scan_id`, `result.records_validated`, `result.passes` and the permanent audit event with verb `ops.strict_validation`.
