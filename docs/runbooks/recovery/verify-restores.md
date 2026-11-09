# Verify backup restores

Run restore tests on QA or in local Docker containers. Production tests require
explicit operator authorization. A restore drill writes temporary engines,
downloads backup artifacts, and records audit evidence and a rehearsal marker.
An untargeted drill does not read the live FoundationDB cluster; a targeted drill
reads source-cluster version metadata. The YugabyteDB leg reads its restored
temporary ledger.

## Prepare the drill

1. Use the Configs-managed installation and image. Confirm the backup bucket,
   object-store connection, credentials, and Docker network. Do not hand-edit
   live settings. Confirm that the configured FoundationDB and YugabyteDB
   overlays exist and that the YugabyteDB rocksdb path matches the image.
2. Confirm that the backup profile runs the `fdb-backup-agent` service for normal
   backup work. The drill starts a separate agent in its empty temporary cluster.
   Keep the service bridge IPv6-only and use the target's own YugabyteDB
   hostname. Do not replace stable node identities with container addresses.
3. Select the backup target. Without a YugabyteDB run key, the drill discovers
   the newest complete export and skips newer unfinished runs. An explicit
   `--yb-run-key` must identify a complete run; the drill refuses an incomplete
   selection instead of substituting another run.
4. Select the FoundationDB mode. Without a target, the drill restores the newest
   session's latest restorable point. With `--fdb-target-time`, use a whole
   second with an explicit offset, such as `2026-08-30T01:07:23Z` or
   `2026/08/30.01:07:23+0000`. The drill refuses fractions and selects the newest
   backup session covering that moment. The source cluster must remain readable
   for timestamp conversion.

## Run and inspect the drill

1. Invoke `ops backup restore-drill` through `tack-ops` with the required
   execution flag and verified operator identity. Use the command's help to
   supply the selected target flags. The CLI records the external operation
   before the drill starts.
2. Inspect each leg's result. Continuous-backup mode must enable the FoundationDB
   leg. If `TACK_BACKUP_FDB_CONTINUOUS` is false, the command skips that leg and
   records partial completion after a passing YugabyteDB leg. That result does
   not prove a FoundationDB restore.
3. Confirm a nonempty FoundationDB range read. Confirm authentication rows and
   the restored audit-chain verification under the temporary reader login.
   The drill exports every organization's full range, including rows with the
   nil organization identifier, without a row limit. It checks bundle counts
   against the restored ledger, hashes, links, and sequence gaps.
4. Inspect the structured verification report. Bundle row order does not affect
   the chain result; verification sorts each shard by sequence. The export
   streams rows rather than accumulating the ledger in memory. After checking
   each organization, the drill deletes that organization's bundle directory.
   A deletion failure stops the drill before it exports another organization.
5. Confirm that both required legs passed and that object storage contains the
   rehearsal marker. Marker-write failure fails the command even after successful
   restores. The marker writer retries separately without rerunning the restore
   legs. A drill attempts the second leg even when the first fails and reports
   all failed legs.
6. Confirm removal of temporary containers and scratch data. Cleanup continues
   after cancellation, and subsequent drills inspect abandoned runs. Retention
   and drills share a filesystem lock under the backup root; wait for active
   backup readers and native operations to end before cleanup can delete their
   artifacts.

The FoundationDB restore monitors progress without imposing a total duration.
A stalled restore reports its last counters and inactivity. Metadata lookup and
individual probes have their own time limits. A large restore can continue
while it advances. YugabyteDB readiness and restoration also stop on failure or
stalled progress rather than treat a large database as a failed restore.
