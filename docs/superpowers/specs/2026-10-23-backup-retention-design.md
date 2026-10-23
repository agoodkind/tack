# Fourteen-day backup retention

Tack must expire old backup data automatically while preserving usable restore points for the last 14 days. Daily ledger exports currently accumulate as independent full snapshots. FoundationDB stores snapshots and mutation logs with dependencies that its backup tool must preserve.

## Scope

Add a permanent Go operator command, `ops backup retention`, with a 14-day default and an explicit `--execute` flag. Its default invocation previews the selected backups without deleting data. Schedule the command daily through the existing Configs systemd deployment pattern. This change does not delete production backups, merge either pull request, or deploy either repository.

Retention applies to backup copies, not live database rows or the canonical audit ledger. Preserve backup status and version-to-time records. Do not use an object-store lifecycle rule over the whole bucket.

## Ledger exports

Use the existing `yugabyte-snapshot/` namespace, timestamp run identifiers, manifests, and artifact completeness checks. Select export runs strictly older than the UTC cutoff. Preserve runs at the boundary and all newer runs.

Before deleting any expired export, establish that at least one complete export survives within the retained window. Refuse deletion when retained exports are absent or incomplete. Validate run identifiers and all selected prefixes. Delete only keys under each selected run's complete prefix, with bounded S3 batches and pagination. Expired incomplete export runs may be removed once the retained complete export guard succeeds. Report API errors and partial completion rather than reporting success after a partial deletion.

## FoundationDB backups

Discover the existing backup marker objects through the production S3 helpers. Keep credential-bearing URLs in memory and redact tool errors. Use the pinned FoundationDB tool through the Docker SDK, never a host shell command.

Read the active backup identity and each backup's restorable window before selecting actions. Preserve active sessions and any session needed for the retained window. For a session spanning the cutoff, invoke `fdbbackup expire` with both the cutoff and a minimum restorability guarantee. Never pass `--force`. For a session entirely within the retained window, leave its data intact. Delete a closed session entirely older than the cutoff only when another verified restorable backup survives in the retained window. Refuse an action when timestamps, identity, or restoration evidence cannot establish safety.

The pinned 7.4.6 CLI supports both timestamp and relative-day expiration. This implementation uses the same exact UTC cutoff for `--expire-before-timestamp` and `--restorable-after-timestamp`, with the source cluster file. Refuse expiration when timestamp conversion fails. Do not substitute relative-day arguments after a failed conversion.

Retain a complete snapshot and any preceding mutation data that the native expiration operation requires at the cutoff. Fourteen-day retention therefore specifies a restore window rather than an exact file-age limit.

## Execution and audit

Use the existing external-operation choke point with a new registered audit verb and `AttachExternalPreview`. Preview performs the same read-only selection as execution. Execution recomputes its plan and saves audit intent before the first effect. Record each backup target and observed cleanup outcome, including partial completion, through the existing step recorder. Never place credentials or backup contents in audit evidence or output.

Retention and restore drills acquire the same process-lifetime filesystem lock under the existing backup root before discovering backup candidates. Retain the lock until every backup read, deletion, and one-shot Docker operation ends. On cancellation, stop and await an active one-shot operation before releasing the lock. Preserve bounded per-file responsibilities and existing logger conventions.

## Scheduling

Configs owns the systemd service and daily timer. The service invokes `docker compose run --rm tack-ops ops backup retention --execute --operator-service tack-backup`. Follow existing backup job identities, failure reporting, timer activation, and environment gates. Keep runtime cleanup logic in Tack Go. Do not add scripts or a Configs test framework. Before enabling the timer, check through the existing deployment command mechanism that the selected Tack image supports `ops backup retention --help`. Fail clearly when the command is absent. Make the companion PR depend explicitly on deploying the Tack command first.

## Acceptance

1. A preview reports selected old runs and preserves every object.
2. Execution removes expired ledger runs and preserves the boundary, recent complete runs, unrelated prefixes, and status objects.
3. Execution refuses ledger deletion when a complete retained export does not exist.
4. Invalid identifiers and malformed manifests cannot broaden deletion scope.
5. Paginated object deletion and partial S3 failures produce accurate counts and audit outcomes.
6. Successful native FoundationDB expiration permits a real restore at the retained boundary. A separate native-refusal case preserves the backup. Deleting an expired closed session preserves the verified surviving session. Active sessions survive, and a failed native guard does not become a forced deletion.
7. Concurrent cleanup and supported restore drills serialize safely. Cancellation releases their process lock.
8. The new command appears in QA datagen verb reporting and runtime audit coverage; command execution records actual intent, steps, and terminal outcome.
9. Real-dependency tests run through the existing container harness and clean up their test resources.

## References

- [TACK-609](https://tack.home.goodkind.io/browse/TACK-609)
- [FoundationDB expiration and restore guarantees](https://apple.github.io/foundationdb/backups.html#expire)
- [Existing backup operator commands](../../../internal/ops/cli_backup.go)
- [Existing export completeness contract](../../../internal/ops/backup_yb_manifest.go)
