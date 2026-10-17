# Activate strict node validation

`ops verify strict-validation` is an audited `./server ops` command with audit verb `ops.strict_validation`. Without the global `--execute` flag, it validates every stored node record in every organization against its organization schema and prints a JSON report. Activation applies to every organization in the FoundationDB cluster. After activation, every ordinary write validates its complete resulting record and refuses an invalid result. Commands cannot remove the activation record.

## Confirm the prerequisites

1. Complete this procedure on QA before repeating it in production.

2. Confirm that the reviewed `ops backfill once-node-integrity` repair has completed on the environment. Run it a second time and confirm that it does not propose repairs. Complete reviewed cleanup before starting a fresh validation scan.

3. For agent runs, set the following shell variables to the actual agent and accountable operator values. The commands below supply the corresponding identity flags.

   | Shell variable | Identity flag |
   | --- | --- |
   | `OPERATOR_SERVICE` | `--operator-service` |
   | `OPERATOR_ID` | `--operator-id` |
   | `OPERATOR_EMAIL` | `--operator-email` |
   | `OPERATOR_NAME` | `--operator-name` |
   | `OPERATOR_SESSION` | `--operator-session` |

4. On every guest that runs the `app` service, run deployment verification without `--execute` and inspect the printed identity.

   ```bash
   docker compose run --rm -T tack-ops \
     --operator-service "$OPERATOR_SERVICE" \
     --operator-id "$OPERATOR_ID" \
     --operator-email "$OPERATOR_EMAIL" \
     --operator-name "$OPERATOR_NAME" \
     --operator-session "$OPERATOR_SESSION" \
     ops deploy verify
   ```

5. After confirming the identity, run deployment verification with `--execute` on every guest that runs the `app` service. Require a successful result on every guest before activation.

   ```bash
   docker compose run --rm -T tack-ops \
     --operator-service "$OPERATOR_SERVICE" \
     --operator-id "$OPERATOR_ID" \
     --operator-email "$OPERATOR_EMAIL" \
     --operator-name "$OPERATOR_NAME" \
     --operator-session "$OPERATOR_SESSION" \
     --execute ops deploy verify
   ```

   `ops deploy verify` compares the registry digests of the expected `tack-server` and `tack-audit-consumer` images with the digests that the `app` and `audit-consumer` containers run. Older product writers do not increment the write fence, which tracks writes during validation. The scan cannot detect their writes.

## Run the dry run

1. Run a fresh scan through the `app` service.

   ```bash
   docker compose run --rm app \
     --operator-service "$OPERATOR_SERVICE" \
     --operator-id "$OPERATOR_ID" \
     --operator-email "$OPERATOR_EMAIL" \
     --operator-name "$OPERATOR_NAME" \
     --operator-session "$OPERATOR_SESSION" \
     ops verify strict-validation
   ```

   The `app` service has FoundationDB access and the audit DSNs. The `tack-ops` service cannot resolve the FoundationDB cluster address.

2. Inspect the printed identity before running the activation command with `--execute`.

3. Inspect the JSON report.

   | JSON key | Check |
   | --- | --- |
   | `dry_run` | Confirm that the value is `true`. |
   | `already_active` | If the value is `true`, activation already exists and the command does not scan. |
   | `activated` | Confirm that the dry run did not activate validation. |
   | `scan_id` | Identify the scan. |
   | `records_validated` | Review the number of records that the command validated. |
   | `passes` | Review the number of validation passes. |
   | `violation_count` | Require zero violations before activation. |
   | `first_violations` | Inspect up to 20 entries. Each entry includes `org_id`, `node_id`, `node_type`, and `reason`. |

4. Stop if `violation_count` is nonzero. The command prints the report and exits with an error. Repair the invalid records through a reviewed manifest, then repeat the dry run before activation.

## Activate strict node validation

1. After the prerequisite checks and a successful fresh dry run, run the activation command.

   ```bash
   docker compose run --rm app \
     --operator-service "$OPERATOR_SERVICE" \
     --operator-id "$OPERATOR_ID" \
     --operator-email "$OPERATOR_EMAIL" \
     --operator-name "$OPERATOR_NAME" \
     --operator-session "$OPERATOR_SESSION" \
     --execute ops verify strict-validation
   ```

   This run performs its own scan. With zero invalid records, the command activates strict validation in one FoundationDB transaction that compares the write fence and organization metadata evidence again.

2. Require `activated: true` in the JSON report.

   When evidence changes during a scan, the command validates the affected records again. After five passes that each detect changed evidence, the command exits with an error and does not activate. Rerun the command to start a new scan.

   If the activation scan finds invalid records, stop and complete the reviewed repair procedure before retrying. A run after activation reports `already_active: true` and does not scan.

## Record the result

1. Save the following evidence from the activation run:

   - Record the Tack commit and image digest.
   - Record the operator identity.
   - Record `scan_id`.
   - Record `records_validated`.
   - Record `passes`.
   - Record the activation audit event.
