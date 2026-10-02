# Reset Tack QA and regenerate its data

This procedure replaces the Tack QA guests, boots empty stores, creates QA data through Tack, and rebuilds an empty OpenSearch index from FoundationDB. It does not restore product data from a backup. Run every command from reviewed Tack and Configs revisions and save the command output with the revision and image digest.

The reset destroys the existing QA FoundationDB, YugabyteDB, Kafka, ClickHouse, and OpenSearch state on the replaced guests. Identify and retain any evidence needed from QA before replacement. The existing SeaweedFS backup guest and its old backup objects are outside the six guest replacements; do not use an old QA backup as the new product data source.

## Confirm the QA boundary

1. Confirm that the target inventory contains only the six suburban QA guests: `tack-qa` (CT 217), `tack-data1` through `tack-data3` (CT 220 through 222), `tack-app2` (CT 223), and `tack-search1` (CT 227). Compare their resource addresses, hostnames, and network addresses with the live Proxmox inventory and the current Configs inventory.
2. Confirm that the reviewed Configs revision permits replacement of those six QA resources. Confirm that the Tack image and Configs revision include the audited agent identity, empty ledger audit bootstrap, serial ledger join and replication wait, and fresh guest preparation. If a prerequisite is missing, stop before replacement.
3. Record the operator, agent session, Configs commit, Tack commit, image tag and digest, current QA service state, and the evidence retained before deletion. Use the identity flags rendered by Configs. Reject a dry run that displays a shared or incorrect operator identity.
4. Review the [QA OpenSearch release requirements](../superpowers/plans/2026-09-19-opensearch-release.md) and the current capacity stop thresholds. Keep production operations and deployments paused. The Configs OpenTofu root also includes non-QA modules, so any planned change outside the six QA guests stops this procedure.

## Replace the QA guests

1. In a Configs checkout at the reviewed `origin/main` commit, save an OpenTofu plan for the six verified resources:

   ```bash
   ./configsctl tofu plan -out=qa-reset.tfplan \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_qa_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_data1_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_data2_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_data3_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_app2_suburban \
     -replace=module.suburban.proxmox_virtual_environment_container.tack_search1_suburban
   ```

   Inspect the complete plan. Require exactly six QA replacements and no other change. Save the readable plan and the plan file SHA-256. Stop if current Configs resource addresses differ from this command.
2. Recheck the saved plan checksum immediately before `./configsctl tofu apply qa-reset.tfplan`. Apply that file only. Do not run a fresh, unsaved apply. Save the apply result, then run a read-only suburban plan without `-replace` and require no pending change.
3. Run `./configsctl deploy prep-guests --limit tack_qa_all --extra-var target_hosts=tack_qa_all --check --diff`. Review its complete target and change list, then run the same command without `--check --diff`. Save both outputs.

## Bootstrap empty stores

1. Run `./configsctl deploy deploy-tack --limit tack_data1_suburban --extra-var tack_store_bootstrap=true --extra-var tack_ledger_bootstrap=true --extra-var tack_ledger_audit_bootstrap=true` with the reviewed image and agent identity variables. The audited `ops ledger audit-bootstrap` command creates the empty ledger infrastructure before later ops commands record to its outbox. Require the command to reject a nonempty ledger and a production target.
2. Run `./configsctl deploy deploy-tack --limit tack_qa_all --extra-var tack_ledger_bootstrap=true` with the same image and identity variables. Require the audited wait after the second ledger node before starting the third. Require three live masters, three registered tablet servers, replication factor three, and zero under-replicated tablets after the third node. The owner guest runs audited `ops provision --allow-fdb-init` for the fresh FoundationDB cluster. Its ordered steps apply migrations, seed audit roles, start configured continuous backup, and create the initial product seed when `SEED_EMAIL` and `SEED_NAME` are set.
3. Run `./configsctl deploy deploy-tack --limit tack_qa_all` with no bootstrap variables and the same image and identity variables. Record the image digest running on every guest. Stop on a failed audited command instead of repairing a database with SQL or a shell script.
4. Recheck the slow storage pool and CT 218 root filesystem immediately before `./configsctl deploy move-slow-tier --limit suburban`. Confirm that its only predicted host write is the new CT 217 scratch mount. Check whether `/root/backups` or `/root/backups.hot-pool` exists inside CT 217. The reviewed play creates an empty mount when both are absent; it copies old files only when a directory exists. Run the play after those checks and save its result.

## Regenerate QA data and search

Use the reviewed operator identity flags on every direct `docker compose run --rm app` invocation. Agent runs include `--operator-service`, `--operator-id`, `--operator-email`, `--operator-name`, and `--operator-session` with the actual agent and accountable operator values. Run an audited command without the global `--execute` flag first and inspect the printed identity and action. Add `--execute` to run the command. For datagen, `--commit` is a separate flag: it enables generated writes after the QA target check. The app container must receive a fresh command; its normal `serve` identity arguments do not apply to `docker compose run` overrides.

1. Check FoundationDB cluster status, ledger replication, current `audit.events` partitions, audit signer verification, backup session identity, and non-search public smoke behavior. Add the new QA audit signer through a reviewed Configs change if verification requires it. Keep the previous signer for retained backups.
2. From the QA installation, run `ops qa datagen seed --scale small` with `--execute` and the reviewed identity flags. Inspect its generated dry-run counts. Repeat with `--commit` to create QA data. The `app` service has FoundationDB access and audit DSNs. The target guard requires `TACK_DATAGEN_ALLOW_TARGET=qa`; never override the guard for production.
3. Review a complete search projection manifest. Run the audited `ops backfill once-search-projections --manifest <reviewed-file>` in dry-run mode, then execute that exact manifest. Rerun it and require zero changes and zero missing declarations. This expiring command must exist in the selected Tack image.
4. With public search disabled, run audited `ops search provision` against an empty physical index. Run audited `ops search reindex --mode full` so the new index reads product data from FoundationDB. Do not restore Meilisearch or an old OpenSearch index. Wait for the rebuild and queued source mutations to complete. Run `ops search verify` and check the pinned model, mapping, alias, shard settings, health, and work backlog.
5. Enable QA public search through a reviewed Configs change and deploy its exact revision and Tack image. Run `ops qa datagen search --commit` and `ops qa datagen soak --duration 60s --rate 3 --commit` with `--execute` and the reviewed identity flags. Record the QA target guard, generated counts, search result, and audit events.

## Close the reset

1. Run the public behavior, authorization, pagination, outage recovery, capacity, and two-process checks required by the QA release plan. Record deployment, search activation, and live acceptance as separate results. A successful datagen seed alone does not prove search acceptance.
2. Restore `prevent_destroy = true` for all six QA resources in a reviewed Configs change. Confirm the merged revision and verify the live OpenTofu plan has no pending change.
3. Record the final guest identities, Configs and Tack commits, image digests, operator provenance, audited command results, generated corpus size, OpenSearch generation, and unresolved failures in the QA evidence ledger. Preserve the production pause.
