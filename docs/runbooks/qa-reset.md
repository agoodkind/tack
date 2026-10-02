# Reset Tack QA and regenerate its data

This procedure replaces the Tack QA guests, boots empty stores, creates QA data through Tack, and rebuilds an empty OpenSearch index from FoundationDB. It does not restore product data from a backup. Run every command from reviewed Tack and Configs revisions and save the command output with the revision and image digest.

The reset destroys the existing QA FoundationDB, YugabyteDB, Kafka, ClickHouse, and OpenSearch state on the replaced guests. Identify and retain any evidence needed from QA before replacement. The existing SeaweedFS backup guest and its old backup objects are outside the six guest replacements; do not use an old QA backup as the new product data source.

## Confirm the QA boundary

1. Confirm that the target inventory contains only the six suburban QA guests: `tack-qa` (CT 217), `tack-data1` through `tack-data3` (CT 220 through 222), `tack-app2` (CT 223), and `tack-search1` (CT 227). Compare their resource addresses, hostnames, and network addresses with the live Proxmox inventory and the current Configs inventory.
2. Confirm that the reviewed Configs revision permits replacement of those six QA resources. Confirm that the Tack image and Configs revision include the audited agent identity, empty ledger audit bootstrap, serial ledger join and replication wait, and fresh guest preparation. If a prerequisite is missing, stop before replacement.
3. Record the operator, agent session, Configs commit, Tack commit, image tag and digest, current QA service state, and the evidence retained before deletion. Use the identity flags rendered by Configs. Reject a dry run that displays a shared or incorrect operator identity.
4. Review the [QA OpenSearch release requirements](../superpowers/plans/2026-09-19-opensearch-release.md) and the current capacity stop thresholds. Keep production operations and deployments paused. The Configs OpenTofu root also includes non-QA modules; target only `module.suburban` in the saved plan.
5. Before replacement, save a read-only listing of `/root/backups` from CT 217. On the suburban hypervisor, confirm that `zpool list -H -o name` includes `slowpool`, `pvesm status --storage slow-zfs` reports active, and `pct config 218 --current 1` reports its root filesystem on `slow-zfs`. Save these results for comparison before the slow-tier play.

## Replace the QA guests

1. In a Configs checkout at the reviewed `origin/main` commit, save an OpenTofu plan for the six verified resources:

   ```bash
   ./configsctl tofu plan -out=qa-reset.tfplan -target=module.suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_qa_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_data1_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_data2_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_data3_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_app2_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_search1_suburban
   ```

   Inspect the complete plan. Require exactly six QA replacements and no other change in `module.suburban`. Save the readable plan and the plan file SHA-256. Stop if current Configs resource addresses differ from this command.
2. Recheck the saved plan checksum immediately before `./configsctl tofu apply qa-reset.tfplan`. Apply that file only. Do not run a fresh, unsaved apply. Save the apply result, then run `./configsctl tofu plan -target=module.suburban` without `-replace` and require no pending change in `module.suburban`.
3. Run `./configsctl deploy prep-guests --limit tack_qa_all --extra-var target_hosts=tack_qa_all --check --diff`. Review its complete target and change list, then run the same command without `--check --diff`. Save both outputs.

## Bootstrap empty stores

Pass `--extra-var tack_image_tag=<reviewed-tag>`, `--extra-var tack_ops_agent_service=<agent>`, and `--extra-var tack_ops_agent_session=<session>` on each deploy below. Pass `tack_ops_accountable_email` when the accountable person's email differs from the control checkout's git identity. Record the exact values and verify the rendered identity before a host write.

1. Run `./configsctl deploy deploy-tack --limit tack_data1_suburban --extra-var tack_store_bootstrap=true --extra-var tack_ledger_bootstrap=true --extra-var tack_ledger_audit_bootstrap=true` with the image and identity variables above. The audited `ops ledger audit-bootstrap` command creates the empty ledger infrastructure before later ops commands record to its outbox. The command refuses a nonempty ledger. The first `deploy-tack` play refuses the audit bootstrap flag on a production guest or without `tack_ledger_bootstrap`.
2. Run `./configsctl deploy deploy-tack --limit tack_qa_all --extra-var tack_ledger_bootstrap=true` with the same image and identity variables. Require the audited wait after the second ledger node before starting the third. Require three live masters, three registered tablet servers, replication factor three, and zero under-replicated tablets after the third node. The owner guest runs audited `ops provision --allow-fdb-init` for the fresh FoundationDB cluster. Its ordered steps apply migrations, seed audit roles, start configured continuous backup, and create the initial product seed when `SEED_EMAIL` and `SEED_NAME` are set. This run also sets the CT 227 search heap. Review the QA heap trial stop thresholds before the run, monitor suburban available memory during it, and confirm the heap afterward. Stop if a threshold fails.
3. Run `./configsctl deploy deploy-tack --limit tack_qa_all` with no bootstrap variables and the same image and identity variables. Run audited `./server ops deploy verify` to compare the running app and audit-consumer digests with the reviewed registry images. Record the search container digest separately. Stop on a failed audited command instead of repairing a database with SQL or a shell script.
4. Immediately before `./configsctl deploy move-slow-tier --limit suburban`, repeat the pool, storage, and CT 218 reads from the preflight. Confirm that `pct config 217 --current 1` has no `mp0` mount and `pct status 217` reports running. In CT 217, check that no `/root/backups.hot-pool` exists and no `tack-*.service` is active. If `/root/backups` exists, require at least one enabled `tack-*.timer`; the play pauses and resumes those timers while it moves files. Configs revision `f1cf0926` or later creates an empty mount when both backup directories are absent. Confirm that the new CT 217 mount is the only predicted host write, then run the play and save its result.

## Regenerate QA data and search

Use the reviewed operator identity flags on every direct `docker compose run --rm app` invocation. Agent runs include `--operator-service`, `--operator-id`, `--operator-email`, `--operator-name`, and `--operator-session` with the actual agent and accountable operator values. Run an audited command without the global `--execute` flag first and inspect the printed identity and action. Add `--execute` to run the command. For datagen, `--commit` is a separate flag: it enables generated writes after the QA target check. The app container must receive a fresh command; its normal `serve` identity arguments do not apply to `docker compose run` overrides.

1. Check FoundationDB cluster status, ledger replication, current `audit.events` partitions, audit signer verification, backup session identity, and non-search public smoke behavior. Add the new QA audit signer through a reviewed Configs change if verification requires it. Keep the previous signer for retained backups.
2. From the QA installation, run `ops qa datagen seed --scale small` with `--execute` and the reviewed identity flags. Inspect its generated dry-run counts. Then create QA data with both action flags and the same verified identity values:

   ```bash
   docker compose run --rm app --execute \
     --operator-service <agent> --operator-id <operator-uuid> \
     --operator-email <operator-email> --operator-name <operator-name> \
     --operator-session <session> \
     ops qa datagen seed --scale small --commit
   ```

   The `app` service has FoundationDB access and audit DSNs. The target guard requires `TACK_DATAGEN_ALLOW_TARGET=qa`; never override the guard for production.
3. Review a complete search projection manifest. Run the audited `ops backfill once-search-projections --manifest <reviewed-file>` in dry-run mode, then execute that exact manifest. Rerun it and require zero changes and zero missing declarations. This expiring command must exist in the selected Tack image.
4. With public search disabled, run audited `ops search provision` against an empty physical index. Run audited `ops search reindex --mode full` so the new index reads product data from FoundationDB. Do not restore Meilisearch or an old OpenSearch index. Wait for the rebuild and queued source mutations to complete. Run `ops search verify` and check the pinned model, mapping, alias, shard settings, health, and work backlog.
5. Enable QA public search through a reviewed Configs change and deploy its exact revision and Tack image. Run `ops qa datagen search --commit` and `ops qa datagen soak --duration 60s --rate 3 --commit` with `--execute` and the reviewed identity flags. Record the QA target guard, generated counts, search result, and audit events.

## Close the reset

1. Run the public behavior, authorization, pagination, outage recovery, capacity, and two-process checks required by the QA release plan. Record deployment, search activation, and live acceptance as separate results. A successful datagen seed alone does not prove search acceptance.
2. Restore `prevent_destroy = true` for all six QA resources in a reviewed Configs change. Confirm the merged revision and require `./configsctl tofu plan -target=module.suburban` to show no pending change.
3. Record the final guest identities, Configs and Tack commits, image digests, operator provenance, audited command results, generated corpus size, OpenSearch generation, and unresolved failures in the QA evidence ledger. Preserve the production pause.
