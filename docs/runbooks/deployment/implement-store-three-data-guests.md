# Verify a three-guest product store

Run store verification on QA or in Docker containers. Production tests require
explicit operator authorization. Use audited commands with the verified
operator identity and record live results separately from deployment intent.

1. Run `ops store status` through `tack-ops`. Require three FoundationDB
   processes on three data guests, three guest zones and machines, `double`
   redundancy, and three coordinators at the guests' pinned addresses. Check
   fault tolerance and wait for replication to become healthy and moving data
   to fall to zero before retiring a process.
2. Check each data guest's running backup agent and the cluster's backup
   session. Require a restorable session and record its restorable point with
   `ops backup staleness-check` before each outage. A configured backup setting
   alone does not prove a running backup.
3. Inspect the app, audit consumer, backup agent, and operator containers.
   Confirm writable cluster-directory mounts and matching current coordinator
   lists in their cluster files. Record container start times when checking a
   coordinator change; verify that the cluster updates each connected client's
   file without restarting that container.
4. Before any coordinator change, confirm that processes already run at the
   reviewed destination addresses and that every cluster process can connect
   to every other process. Rehearse the coordinator change, process exclusion,
   and new-process join on a throwaway QA cluster with separate scratch data.
   A wrong coordinator list can make the cluster unavailable. Preserve the
   current coordinator and client-file evidence before changing it.
5. Use reviewed Configs changes to prepare each guest's persistent cluster
   directory, pinned process address, zone, and machine identity. Preserve a
   nonempty cluster file. Seed a new guest from the existing cluster's
   coordinator list before starting its process; do not create another cluster
   on a guest that must join the existing one.
6. Use `ops store set-redundancy`, `ops store set-coordinators`, and
   `ops store exclude` for reviewed cluster changes. Inspect the dry run and
   use `--execute` with the verified identity. Wait for exclusion to complete
   before stopping a process or deleting its data. Keep any temporary process
   and its persistent volume until the new replicas and coordinator quorum
   work without it.
7. Run probes outside the guest under test. Stop one data guest, then poll
   `/healthz` and perform a product write and read through the MCP interface
   once per second for two minutes. Record each probe's timestamp, HTTP status,
   and operation result. Measure every continuous failure interval and confirm
   that a write acknowledged before the guest stopped reads back afterward.
   Apply section 9 of the [product-store acceptance specification](../../reference/recovery/2026-08-08-backup-rearch-acceptance.md#9-product-store-distributed):
   reads and writes must continue without a non-200 interval longer than ten
   seconds.
8. Run `ops backup staleness-check` fifteen minutes after stopping the guest.
   Compare the FoundationDB restorable point with the reading before that
   outage and require the later reading to report a newer point. Repeat the
   outage and its measurements for each data guest in turn.
9. Record the observation interval, outage duration, request results, recovery
   time, and `FDB_TRANSACTION_TIMEOUT` with the test evidence. A positive
   transaction timeout bounds retries; a nonpositive value does not set that
   database option.

Use a reviewed deployment procedure for any topology change. The current
image-release module does not prepare store processes or distribute cluster
files. A completed image update does not prove a three-guest store migration.
