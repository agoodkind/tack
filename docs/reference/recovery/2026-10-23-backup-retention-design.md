# Backup retention contract

`ops backup retention` selects a positive number of days of restorable backups.
Its default window spans fourteen days. Preview performs read-only selection;
execution recomputes that selection before cleanup. The contract applies to
backup copies and excludes live rows, canonical audit rows, backup status
objects, and version-to-time records.

## Ledger selection

Ledger selection uses timestamp run identifiers under `yugabyte-snapshot/`.
Only runs strictly older than the UTC cutoff qualify. Runs at the cutoff and
newer runs survive. Before deleting any old run, selection must establish at
least one complete export within the retained window. A missing or incomplete
retained export must prevent deletion.

Run identifiers, manifests, and derived prefixes must pass validation. Cleanup
must delete only objects inside each selected run's complete prefix, using
bounded batches and pagination. Expired incomplete runs may qualify after the
retained-complete-export guard succeeds. Partial S3 failures must report actual
confirmed deletions and uncertain results.

## FoundationDB selection

Native snapshot and mutation dependencies determine the retained restore window.
File age alone cannot establish whether deletion preserves that window.
Selection discovers S3 backup markers and reads the active session identity
and each backup's numeric restorable versions. Both preview evidence and audit
evidence must include:

- `min_version`.
- `max_version`.
- The selected expiration version.

The numeric-window read omits `--version-timestamps`. That option converts all
snapshot versions in one transaction; a large snapshot history can exceed the
transaction lifetime and fail with error 1007. The version log instead supplies
a conservative comparison version from its newest reading at least one second
before the cutoff. Selection compares every window with that version and refuses a missing reading.

For a session spanning the cutoff, native `fdbrestore start --dryrun --timestamp`
must resolve the exact retained timestamp using the original cluster file and
backup URL. It must not configure a destination or submit a restore. Selection
must choose the newest restorable snapshot ending at or before that version and
use its numeric start version for `--expire-before-version`. Native expiration
must also receive the source cluster file and the UTC cutoff in `--restorable-after-timestamp`.

The native guard must preserve a complete snapshot and required preceding
mutation data at the boundary. Selection must refuse failed conversion, missing
snapshot evidence, unknown active identity, and unsupported restoration evidence.
It must not substitute displayed snapshot times, relative-day arguments,
padding, or `--force`.

Sessions entirely inside the window remain intact. Active sessions survive.
Deletion of a closed session entirely before the cutoff requires a newer
surviving session with a window containing the cutoff and a successful native
restore dry run at that exact timestamp. Execution must revalidate its selected
FoundationDB action before effects.

## Credentials and native execution

Tack Go must run the pinned engine tools through Docker SDK containers rather
than a host shell. Each native container must receive a private credential file
before its process starts. The directory mode must equal `0700`; the file mode
must equal `0600`. The native URL must omit both credentials and identify the
private account file instead.

Docker command and environment metadata must omit both secret values. Native
tracing and persistent Docker output logs must remain disabled. The command must
capture output in memory, redact errors, and remove the container and credential
file on completion or cancellation. Audit evidence and output must omit backup
contents and credentials.

## Audit and concurrency

Execution must use the shared external-operation audit gate and registered verb.
Preview uses `AttachExternalPreview` without cleanup effects. Execution must
record intent before effects and record each target's observed outcome,
including partial completion and uncertain results. The QA data generator and
runtime audit coverage must include the retention operation.

Retention and supported restore drills must acquire the same process-lifetime
filesystem lock under the backup root before discovering candidates. The lock
must remain acquired until backup reads, deletions, and native Docker operations
end. Cancellation must stop and await active native operations before releasing
the lock. A process exit releases the kernel lock.

## Scheduling and release acceptance

Configs owns the daily service and timer, environment gates, the `tack-backup`
service identity, and failure reporting. The deployed image must pass
`ops backup retention --help` before timer activation. QA cleanup validation must precede
production activation. Configuration alone does not prove an active timer.

Acceptance requires real-dependency checks through the existing container
harness. Required outcomes:

| Check | Required outcome |
| --- | --- |
| Preview | Selection appears without deleting any object. |
| Ledger cleanup | Expired runs disappear; boundary runs, complete recent runs, unrelated prefixes, and status objects survive. |
| Missing retained export | Cleanup refuses deletion. |
| Invalid identifiers or manifests | Selection cannot broaden deletion scope. |
| Pagination and partial failure | Counts and audit outcomes match actual effects. |
| Native expiration | A real restore succeeds at the retained boundary. |
| Native refusal | Every required backup object survives. |
| Closed-session deletion | The verified survivor still restores. |
| Concurrent restore and cancellation | Operations serialize and release the lock after native cleanup. |
| Audit | Permanent records contain actual intent, steps, and terminal outcome. |
| Native container metadata | Secrets remain absent; private permissions, disabled logs, and file cleanup pass. |

Test resources must end with their test. A failed native guard must never become
a forced deletion. A source inspection or a passing unrelated test cannot
replace these execution results.
