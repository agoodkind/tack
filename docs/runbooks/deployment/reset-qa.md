# Reset Tack QA and regenerate its data

A QA replacement destroys the FoundationDB, YugabyteDB, Kafka, and OpenSearch
state on the replaced guests. Preserve required evidence before replacement.
Create new product data through Tack and rebuild search from FoundationDB;
do not restore an old QA backup or index as the new product data source.

## Confirm replacement prerequisites

1. Compare the [current QA resource configuration](https://github.com/agoodkind/configs/blob/main/opentofu/modules/suburban/tack-qa/main.tf)
   and [guest identities](https://github.com/agoodkind/configs/blob/main/opentofu/modules/suburban/tack-qa/configuration/main.tf)
   with live Proxmox inventory. Restrict replacement to `tack-qa` (CT 217),
   `tack-data1` through `tack-data3` (CT 220 through 222), `tack-app2` (CT 223),
   and `tack-search1` (CT 227). Preserve the separate SeaweedFS guest and its
   retained backup objects.
2. Establish a reviewed Configs procedure that recreates the stack, secret
   files, audit signer, network, cluster files, and backup storage after guest
   replacement. The [current QA root](https://github.com/agoodkind/configs/blob/main/opentofu/suburban/tack-qa/main.tf)
   prepares guests, and its release modules update images in an existing
   installation. Those release units require an existing Compose stack and
   credentials. Stop before replacing guests if the reviewed procedure does
   not supply those prerequisites.
3. Save the current QA service state and the evidence that must survive the
   reset. Record the operator, agent session, Configs and Tack commits, image
   tags and digests, and accepted capacity limits. Preserve the production
   pause. A QA reset does not authorize production operations or deployment.
4. Save a read-only listing of `/root/backups` on CT 217. Confirm the current
   pool, active storage, and SeaweedFS root filesystem before changing backup
   mounts. Preserve backup objects outside the replacement set. Verify the
   reviewed procedure's handling of existing mounts, active backup services,
   enabled timers, and an empty backup directory before any storage move.
5. Review the complete saved replacement plan and require exactly the six
   verified QA replacements without unrelated changes. Record the plan file's
   SHA-256 and recheck it immediately before application. Apply only that
   reviewed saved plan file. Do not run a fresh, unsaved apply. Require a
   subsequent plan without replacement flags to show that the QA resources
   match the reviewed configuration. Use current resource addresses and the
   current QA root.

## Provision the new installation

1. Follow [Provision an environment](provision-environment.md). Require the
   empty-ledger audit bootstrap, serial ledger joins and waits, guarded fresh
   FoundationDB initialization, migrations, role preparation, continuous backup,
   and product seed. Keep the selected revision and identity consistent across
   the operation.
2. Verify the running app and audit-consumer digests against the reviewed
   registry images. Record the search engine digest separately. Check the
   actual search heap and available hypervisor memory against the reviewed
   capacity limits; configured guest memory does not prove available memory.
3. Check FoundationDB status, ledger replication, current audit partitions,
   signer verification, backup session identity, and non-search public behavior.
   Add a new accepted QA signer through a reviewed Configs change when needed.
   Keep previous accepted signers for retained backups.

## Regenerate data and search

Use the verified identity on every direct app invocation. Inspect a command
without `--execute` first. The global `--execute` flag permits the audited
operation; datagen's separate `--commit` flag permits generated writes after
the target check. Require `TACK_DATAGEN_ALLOW_TARGET=qa` and never override the
guard for production.

1. Run `ops qa datagen seed --scale small` with `--execute` and the reviewed
   identity, then inspect the generated dry-run counts. Create the data with
   both action flags and the same identity:

   ```bash
   docker compose run --rm app --execute \
     --operator-service <agent> --operator-id <operator-uuid> \
     --operator-email <operator-email> --operator-name <operator-name> \
     --operator-session <session> \
     ops qa datagen seed --scale small --commit
   ```

   Run datagen through the `app` service with FoundationDB access and its
   required audit credentials.
2. Review a complete search projection manifest. Confirm that the selected
   image still provides `ops backfill once-search-projections`. Run its
   `--manifest` input in dry-run mode, execute the exact reviewed manifest, and
   rerun it. Require zero changes and zero missing declarations on the rerun.
3. Keep public search disabled while running audited `ops search provision`
   against an empty physical index and `ops search reindex --mode full`.
   Wait for the rebuild and queued source mutations to complete. Run
   `ops search verify` and check the pinned model, mapping, alias, shard
   settings, health, and work backlog. Do not restore Meilisearch or an old
   OpenSearch index.
4. Enable QA public search through a reviewed Configs change and deploy the
   exact revision and Tack image. Run `ops qa datagen search --commit` and
   `ops qa datagen soak --duration 60s --rate 3 --commit` with `--execute` and
   the verified identity. Record the target guard, generated counts, search
   result, and permanent audit events.

## Complete acceptance

1. Run the public behavior, authorization, pagination, outage recovery,
   capacity, and two-process checks in the [QA search release procedure](../search/implement-opensearch-release.md). Record deployment, search activation, and live acceptance as
   separate results. A successful seed does not prove search acceptance.
2. Restore `prevent_destroy = true` for all six QA resources through a reviewed
   Configs change and confirm the accepted revision. Require a subsequent QA
   plan to show that the resources match the reviewed configuration.
3. Record final guest identities, Configs and Tack commits, image digests,
   operator provenance, audited results, generated corpus size, OpenSearch
   generation, and unresolved failures with the QA acceptance evidence.
   Preserve the production pause.
