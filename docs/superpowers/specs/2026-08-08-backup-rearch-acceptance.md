# Backup rearchitecture: acceptance and empirical validation

These criteria cover the full workstream (spec 2026-08-06, phases 2 through 8).
Require every measurement to pass on QA, then observe it on production.
Run instruments outside the guest under test, using workstation probes,
hypervisor gauges, and object-store listings.

## 1. Verify unattended protection

- Require two consecutive scheduled ledger exports in the object store without
  an operator command. Require spacing between run keys to match the cadence
  within ten percent.
- Sample the FoundationDB restorable point at both ends of 24 unattended hours
  and require it to advance.
- Fail any protection that requires a person to remember a step.

## 2. Restore from the object store alone

- Require the scheduled rehearsal to rebuild both stores in throwaway
  containers from object-store artifacts only. Require rows in the auth tables,
  a non-empty product-store range read, and successful audit-chain verification.
- Use the live stores' image for both the throwaway engines and dump tooling.
  A different engine version tests a different recovery. The first export that
  included privileges could not restore: a 2025.2 scratch engine refused a
  2024.2 catalog signature.
- Read the restored ledger using a role with only the application's base role.
  Do not use the scratch database's bootstrap superuser. That user can read rows
  the application cannot read. Fail a restore that the application cannot read.
- Require a passing rehearsal within the last eight days.

## 3. Recover from a single-guest failure in seconds

- Kill each QA data guest in turn. Require external health probes to show that
  every non-200 window lasts at most 10 s. Read back a write that the service
  acknowledged before the kill. Require ledger leader election under 5 s and
  full replication in every cluster within 15 minutes of guest restoration.
- Measure every service on the guest. After phases 4 and 5, include its
  product-store process and broker under the same bounds.

## 4. Limit disaster data loss to seconds

- Restore product data to a point in time against a known write ladder.
  Require at most the last few seconds of writes to be missing.
- Verify quorum replication as the ledger's protection between exports.
  Document the cadence of exports that protect against loss of all replicas.

## 5. Alarm on staleness

- Pause each job without causing an error for the stream restorable point,
  last export, last passing rehearsal, and under-replication metrics. Require
  each alarm within its threshold plus 15 minutes. Resume the job and require
  the alarm to clear.
- Require the check interval plus randomized delay to fit within 15 minutes.
  A 30-minute cadence missed this bound by 14m56s.
- Require alarms for faults the system cannot correct. Require the nightly
  export and restore drill to retry failures within the day and containers to
  restart after failure. Require the object store to size its volume count
  from free disk rather than a fixed volume limit. On 2026-07-29, production's
  backup stopped when its single 1 GB bucket volume filled despite 91 GB of
  free disk. The system did not report the failure for five weeks.
- Send one email when a mechanism becomes stale. Do not send another while
  it remains stale or when it recovers. State what stopped, since when, and
  what to check. Report an unreadable record as unreadable rather than claiming
  the mechanism stopped. Omit metric names, thresholds in seconds, run
  identifiers, object-store endpoints, and credentials. Fail this criterion
  if one fault produces twenty-five identical machine reports.

## 6. Verify scalable backup work

- Require backup paths to collect engine-native snapshot files by tablet
  leadership rather than reading the dataset row by row.
- Check unit installation and execution locations. Require export orchestration
  and rehearsals on the non-serving guest. Limit backup work on serving nodes
  to archiving their own local files.

## 7. Bound disk use and verify storage placement

- Require an armed prune timer on every guest. Across five consecutive deploys,
  require per-guest disk usage to return within five percent of its pre-deploy
  baseline.
- Read placement from the hypervisor. Require the ledger guest roots,
  product-store data, and app guests on NVMe. Require the object-store guest,
  owner's drill scratch and export staging, and hypervisor guest backups on
  SATA SSD. Prohibit unthrottled bulk copies on the ledger's pool. Require the
  production drill and export to finish without slow ledger sync or leader
  elections during their runs.

## 8. Observe production directly

- Require an owner guest that only orchestrates, a second app guest, and three
  data guests running the ledger. After phases 4 and 5, require the data guests
  to run the product store and brokers too. Install backup units only after
  ledger consumers use the data tier. During the original migration, require
  operator confirmation before each step: prepare four guests, join three
  ledger nodes to the existing cluster without bootstrapping a second universe,
  wait for full replication, repoint consumers, and retire the legacy node.
- Read alarm state, timer state, completed exports, full replication, and prune
  state from running production rather than inferring them from the repository.

## 9. Verify distributed product storage

- Read cluster status and require one FoundationDB process and backup agent per
  data guest, redundancy mode double, and three coordinators, one per guest.
- Require every client to have write access to its cluster file and the current
  coordinator list. Change a coordinator and require each client to update its
  file without a restart.
- Kill any one data guest. Require product-store reads and writes to continue,
  every non-200 window to last at most 10 s, and the restorable point to keep
  advancing during the failure.

## 10. Verify the distributed event queue

- Read broker metadata and require three brokers, three copies of both the
  audit topic and consumer-position topic, and a two-live-copy write requirement.
  Require each broker to advertise its own routable address.
- Kill any one broker. Require state-change events to commit, the consumer to
  keep writing, and the ledger row count for events produced during the failure
  to equal their produced count.

## 11. Verify the distributed app tier

- Read proxy state and require both app guests to serve requests. Require the
  proxy to probe each datastore health endpoint and route only to live instances.
- Kill one app guest. Require external probes through the proxy to show that
  every non-200 window lasts at most 10 s.

## 12. Remove ledger and broker writes from read requests

- Trace one authenticated read on QA. Require zero ledger writes, at most one
  ledger read, and zero synchronous broker round trips. Trace a state change
  and require its event in the product-store transaction.
- Require the token's stored last-use time to match the call time within the
  flush interval. Use a token on both app guests within one interval and
  require the stored time to equal the later call time.
- Pause ledger writes for the flush interval. Require the read call to answer
  200 within its normal latency.

Hypervisor failure requires restoration. This workstream does not add a second
hypervisor for failover.
