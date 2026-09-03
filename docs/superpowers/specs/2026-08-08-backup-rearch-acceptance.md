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
- Every read the rehearsal makes against the restored ledger runs as a role
  holding only the application's own base role, never as the scratch
  database's bootstrap user, whose superuser bypass reads a database the
  application cannot. A restore the application cannot read is a failed
  restore.
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
- The check runs often enough that its interval plus its randomized delay
  fits inside that 15 minutes; a 30 minute cadence cannot meet the bound
  and was measured missing it by 14m56s.
- An alarm means a fault the system could not fix on its own. The nightly
  export and the restore drill retry a failed run on their own before the
  day is lost; a container that dies restarts on its own; the object store
  grows its capacity from free disk rather than from a fixed volume count.
  Production's backup died on 2026-07-29 against a 1 GB store cap with
  91 GB free, and nothing said so for five weeks.
- One mail per fault. The mail goes out once, when a mechanism transitions
  into stale, and not again while it stays stale or when it recovers. It
  says in plain words what stopped, since when, and what to check, and it
  claims only what the reading supports: a record that could not be read is
  reported as unreadable, not as a mechanism that stopped. It carries no
  metric names, no thresholds in seconds, no run identifiers, and no
  object-store endpoint or credential. Twenty-five identical machine
  reports for one fault is a failure of this criterion.

## 6. Scalable, and placement holds

- No backup path reads the dataset row by row; the export collects
  engine-native snapshot files by tablet leadership.
- Export orchestration and rehearsals run on the non-serving guest, and no
  serving node performs backup work beyond archiving its own local files,
  verified by where the units are installed and where the runs execute.

## 7. Disk stays bounded

- The prune timer is armed on every guest; across five consecutive
  deploys, per-guest disk usage returns to within five percent of its
  pre-deploy baseline.

## 8. Production observed directly

- Production runs the same guest layout the criteria above assume: an
  owner guest that serves nothing but orchestrates, a second app guest, and
  three data guests carrying the ledger. Until the production data-tier
  cutover is done, none of the backup units install on production, because
  every one of them is gated on the ledger consumers being repointed to the
  data tier. The cutover is therefore a named step of this workstream, in
  order: prepare the four guests, join the three ledger nodes to the
  existing cluster (never bootstrap a second universe), wait for full
  replication, repoint the consumers, retire the legacy node. Each step is
  confirmed with the operator before it runs.
- Every state above (alarms armed, timers armed, exports flowing, full
  replication, prune armed) is read from the running production system,
  not inferred from the repo.

Residual: hypervisor loss is covered by restore only; there is no second
hypervisor to fail over to, and this workstream does not add one.
