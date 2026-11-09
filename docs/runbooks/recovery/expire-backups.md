# Expire old backups

Retention permanently deletes expired backup copies. Validate cleanup on QA
before production activation. The command preserves live rows, audit ledger
rows, backup status objects, and version-to-time records. Do not apply a bucket
lifecycle rule that deletes those records or FoundationDB restore dependencies.

Run the command from the Configs-managed installation with the selected image,
backup settings, and required operator identity. Do not hand-edit live
configuration. The default restore window spans fourteen days; `--keep-days`
selects another positive number of days.

Append the applicable identity flags to both the preview and the execution
command. For a human operator, supply the operator's nonzero UUID with
`--operator-id` and email with `--operator-email`. For a service, supply its
name with `--operator-service` and include the
[session and accountable operator flags](../../reference/audit/operator-identity.md#identity)
when the service acts for a human.

1. Preview the selected ledger exports and FoundationDB sessions:

   ```sh
   docker compose run --rm tack-ops ops backup retention
   ```

2. Review the UTC cutoff, selected targets, and FoundationDB `min_version`,
   `max_version`, and deletion version. Ledger runs exactly at the cutoff and
   newer runs must survive. At least one complete export must survive within
   the window before any expired ledger run can disappear. Cleanup may remove
   expired incomplete runs only after that guard succeeds.
3. Confirm that the FoundationDB version log contains a reading from at least
   one second before the cutoff. Cleanup refuses to select FoundationDB actions
   without that evidence. A session spanning the cutoff requires the native
   restore and expiration guards. Active sessions must survive. A closed session
   entirely before the cutoff requires a verified newer survivor covering the
   boundary.
4. Delete the selected expired backups:

   ```sh
   docker compose run --rm tack-ops ops backup retention --execute
   ```

5. Inspect the actual audit result and backup state. Execution recomputes its
   selection, records intent before each cleanup effect, and records confirmed
   deletions, requested deletions, and uncertain or partial outcomes. Inspect a
   failed result before retrying. Repeated execution recomputes remaining work;
   a canceled S3 request can have an uncertain result.

The native FoundationDB expiration guard preserves the snapshot and mutation
logs needed at the cutoff. Some required files can predate the window. A native
refusal must remain a refusal; do not force expiration or substitute relative
file ages. Native tools protect credentials in private files and omit them from
Docker command metadata, environment metadata, and persistent output logs.

Retention and supported restore drills serialize through a filesystem lock under
the backup root. Each command waits for the other to finish. Cancellation must
stop and await active native containers before releasing that lock. The lock
does not coordinate commands using a different filesystem or an unsupported
manual restore path.

Configs owns automatic scheduling. Confirm the selected deployed image supports
the retention command before enabling the daily timer. Read the timer and its
last outcome from the running guest; a repository definition does not prove
activation.
