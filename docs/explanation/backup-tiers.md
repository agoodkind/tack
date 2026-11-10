# Backup and recovery tiers

Quorum replication across fault domains protects acknowledged writes after a
node or zone fails. Replication also copies accidental deletions and corrupt
changes to other replicas. Backups provide an earlier recovery point for those
failures. Recovery time includes rebuilding and verifying the restored data.

FoundationDB stores product nodes, relationships, properties, and read models.
Its continuous backup writes snapshots and mutation logs to SeaweedFS object
storage. A restore can select a version within a backup session's restorable
window. The newest restorable version measures the protection actually available.
A stalled backup agent or an object store that refuses writes prevents that
version from advancing, even while the application continues serving.

The audited `fdb-continuous-init` operation starts the continuous session when
continuous backup is enabled. It leaves an active session at the configured
destination unchanged and refuses an active session at a different destination.
Ordered provisioning invokes this initialization when continuous backup is
enabled. Backup agents perform the ongoing snapshot and mutation work.

YugabyteDB backs up authentication and the compliance audit ledger together,
including audit events, chain heads, notarizations, and protected personal data.
`ops backup yb-snapshot-export` captures a distributed snapshot, grants, and roles.
Each data guest runs `ops backup yb-archive-node` for its tablet snapshot files.
The manifest identifies the required artifacts and nodes. A usable export must
contain every artifact and a complete copy of every tablet.

A YugabyteDB export restores the database to that snapshot. Writes after the
snapshot require another recovery source. The interval between complete exports,
including archive completion time, determines the potential loss from this tier.
Replication protects writes between exports against a single guest failure; it
does not provide an earlier copy after an accidental deletion. A missed export
increases the age of the newest usable snapshot.

YugabyteDB also supports in-cluster point-in-time recovery through a snapshot
schedule. The audited `yb-pitr-init` operation creates that schedule for the
complete authentication and audit database. Its interval and retention settings
use minutes. The schedule provides earlier recovery points for corruption or
accidental deletion within its retained window. Its snapshots depend on the
cluster's storage and do not replace off-host exports after storage loss.
Configuration does not prove that a live schedule exists or supplies a usable
recovery point; the running cluster must provide that evidence.

## Restore evidence

A restore drill creates temporary engines from backup artifacts and checks the
restored data. The YugabyteDB tooling and temporary engine use the same image as
the configured ledger service. Matching versions matters because a schema dump
can include catalog features that another engine version rejects.

The drill checks authentication rows and verifies complete audit exports under
the `audit_reader` base role. A superuser can bypass access rules and cannot
prove that the reader role can read the restored ledger. The drill signs each
temporary export with a temporary key; that signature verifies the export bundle,
not the production signing key.

A consistent restored hash chain does not prove that the backup contains every
source row. Losing the newest rows can leave the remaining chain consistent.
Either of these checks provides separate evidence of backup completeness:

- Source counts provide a basis for comparison.
- A known write sequence provides a basis for comparison.

Recovery must preserve `audit.chain_heads`, the per-organization and per-shard
values that subsequent audit writes use to continue each chain.

The untargeted FoundationDB drill restores the newest backup without reading the
source cluster. A drill with `--fdb-target-time` reads source-cluster metadata to
convert a time to a version. After total cluster loss, the `restore-version`
command reads recorded version-to-time pairs from object storage instead. That
lookup returns a recorded point at or before the requested time; it does not
perform a restore.

## Backup age and alarms

A mechanism that has never recorded success counts as stale. When a probe cannot
read a mechanism, the checker continues using its last local success timestamp,
including after its age exceeds the threshold. The report preserves the
unreadable cause and reports that older age as stale. An unreadable record does
not prove that the mechanism stopped. A successful fresh probe clears the alarm;
a remembered reading does not clear it.

The alarm sends one plain-language message when a fault becomes stale. It does
not repeat the message while the fault continues or send a recovery message. A
failed mail attempt remains eligible for retry. The primary checker and deputy
coordinate their alarm decisions, and local records support reporting when the
object store becomes unreadable.

Schedules and thresholds answer different questions. The schedule controls how
often protection advances; the threshold controls how long a missing advance can
remain unreported. Neither a timer definition nor a healthy application proves
that an export or rehearsal succeeded. Runtime observations must establish the
latest success and the current schedule.

## Retention

Retention preserves a restore window rather than a strict maximum file age.
FoundationDB can require older snapshot and mutation files to restore at the
retained boundary. Its native expiration guard verifies those dependencies.
Ledger retention preserves a complete recent export before deleting older runs.
Retention preserves live records, backup status objects, and version-to-time
records.

Search indexes derive from authoritative product data and can rebuild from it.
The backup commands do not capture a search-index archive or Temporal workflow
state. Object storage provides a backup destination; it does not provide a second
running hypervisor for automatic failover.
