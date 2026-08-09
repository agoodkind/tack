# Backup rearchitecture: acceptance and empirical validation

Scope: the full workstream (spec 2026-08-06, phases 2 through 7), not any
one slice. Done means every criterion below passes its stated measurement,
QA first, then observed on production. Instruments live outside the guest
under test (workstation probes, hypervisor gauges, object-store listings),
so a failing guest cannot destroy its own evidence.

## 1. Continuous: no human in the loop

- Two consecutive scheduled ledger exports appear in the object store with
  no operator command; spacing between run keys matches the cadence within
  ten percent.
- The FoundationDB restorable point advances across 24 unattended hours,
  sampled at both ends.
- FAIL if any protection requires a person remembering to run a step.

## 2. Durable: restores from the object store alone

- The scheduled restore rehearsal rebuilds both stores into throwaway
  containers from object-store artifacts only: auth tables hold rows, a
  product-store range read is non-empty, the audit chain verifies.
- The last passing rehearsal is never older than eight days.

## 3. Single-guest loss heals in seconds

- QA guest-kill rehearsal, each data guest in turn: external health probes
  show no non-200 window longer than 10 s; a write acknowledged before the
  kill reads back after it; ledger leader election completes under 5 s;
  every cluster reports full replication within 15 minutes of restore.

## 4. Disaster loses at most seconds

- A point-in-time product-store restore against a known write ladder shows
  at most the last few seconds missing.
- The ledger's between-export protection is quorum replication; the export
  is the survives-everything tier, and its cadence is documented.

## 5. Alarms fire on staleness, not failure

- For each metric (stream restorable point, last export, last rehearsal
  pass, under-replication): induce silent staleness by pausing the job so
  nothing errors; the alarm fires within threshold plus 15 minutes; resume,
  and it clears. A silently broken mechanism can never stay dark.

## 6. Scalable, and placement holds

- No backup path reads the dataset row by row; the export collects
  engine-native snapshot files by tablet leadership.
- Export and rehearsal run on a non-serving guest, verified by where the
  units are installed and where the runs execute.

## 7. Disk stays bounded

- The prune timer is armed on every guest; across five consecutive
  deploys, per-guest disk usage returns to within five percent of its
  pre-deploy baseline.

## 8. Production observed directly

- Every state above (alarms armed, timers armed, exports flowing, full
  replication, prune armed) is read from the running production system,
  not inferred from the repo.

Residual: hypervisor loss is covered by restore only; there is no second
hypervisor to fail over to, and this workstream does not add one.
