# Update the YugabyteDB image

Validate an engine image change on QA before production. An application release
does not authorize a database engine upgrade.

1. Review the image change in [docker-compose.yml](../../../docker-compose.yml).
   Keep one engine image for the database and the backup and restore tools.
   Preserve the persistent base directory, stable advertised and listen
   identities, and the tserver flagfile argument when an override replaces the
   complete command list.
2. Fetch the [upstream launcher](https://github.com/yugabyte/yugabyte-db/blob/master/bin/yugabyted)
   from the source revision that matches the new image tag.
   Refresh the [launcher overlay](../../../yugabyte-overlay/yugabyted) and reapply
   the IPv6 port-check change from the
   [checked-in patch](../../../yugabyte-overlay/yugabyted.patch).
   Keep the `AF_UNSPEC` address lookup and matching socket family. Review the
   refreshed launcher against the new upstream version rather than copying an
   older complete launcher onto a new engine.
3. Check the read-only overlay mount and the backup tooling's engine image in
   the reviewed Compose configuration. Confirm that the restore drill receives
   the refreshed overlay path. Preserve the existing secret-scanner allowlists
   for the vendored launcher.
4. Deploy the reviewed Configs and Tack revisions to QA. Verify each result:

   - Startup succeeds on the IPv6-only network.
   - Database access succeeds after a container recreation.
   - The ledger replicates.
   - A backup restore drill succeeds with the matching engine and overlay.

   Record revisions, image digests, audited command results, and restored-ledger
   verification.
5. Require the applicable audit and integration checks and accepted QA evidence
   before a production release. Use the reviewed Configs deployment path for
   production; do not edit the guest configuration by hand.
