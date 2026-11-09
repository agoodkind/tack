# Repeat a backup alarm deliberately

Resetting alarm memory makes the next stale reading eligible to send mail again.
Preserve the existing evidence and use an approved, audited change for the reset.

Use the Configs-managed installation and backup settings. Confirm the alarm recipient and relay configuration. An empty `TACK_BACKUP_ALARM_EMAIL` disables mail; a nonzero check result still reports stale protection. Run `ops backup staleness-check` through `tack-ops` with the required execution flag and verified operator identity. Record the output and exit status.

1. Identify the configured backup bucket and backup root on every checker.
2. Remove the shared `backup-status/alarm-state.json` object and each checker's
   local `staleness-alarm-state.json` through that approved operation. Clearing
   only one copy can let another copy restore the remembered fault.
3. Run the check and inspect both its verdict and the mail outcome. Do not
   remove backup artifacts or version-to-time records while resetting alarms.

