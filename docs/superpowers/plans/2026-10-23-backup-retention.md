# Implement fourteen-day backup retention

Use `subagent-driven-development` or `executing-plans` to implement each task.
Track completion with checkboxes. Follow the
[retention specification](../specs/2026-10-23-backup-retention-design.md).

Tack Go previews and removes expired backup sets under audit. Native
FoundationDB expiration preserves snapshot and mutation-log dependencies.
A companion Configs change schedules the existing operator container daily.
Use Go, Cobra, the existing recorder, AWS S3 SDK, Docker SDK, FoundationDB 7.4.6,
and existing Configs systemd templates.

## Constraints and review requirements

Use isolated worktrees and preserve concurrent changes. Keep secrets and backup
contents out of logs, chat, fixtures, and permanent evidence. Use real
dependencies through the existing runner without mocks, scripts, or another
test framework. Keep source files below 200 lines and split by responsibility.
Open ready-for-review PRs and validate QA cleanup before production activation.

Require a complete recent export before deleting expired ledger exports.
Preserve restore dependencies after native expiration refusal. Reject prefixes
outside the selected namespace. Report partial object deletion accurately and
record the partial outcome in its audit evidence. Prevent retention from deleting
backups that a concurrent restore drill still reads.

## Task 1: Implement audited selection and execution

Create internal/ops/backup_retention.go, backup_retention_ledger.go,
backup_retention_fdb.go, cli_backup_retention.go, and focused companion files.
Create internal/audit/verbs_backup_retention.go. Update cli_backup.go
registration and datagen_ledger_verbs.go.

BackupRetentionOptions requires positive KeepDays. Implement
`RunBackupRetention(context.Context, *config.Config, BackupRetentionOptions, io.Writer, audit.StepRecorder) (audit.OutcomeDetails, error)`
and a separate nonmutating preview with identical selection.
`backupRetentionCommand(*cli.Factory) *cobra.Command` registers
`ops backup retention --keep-days`, default 14. Use opsNow for time.

- [x] Read existing command, manifest, backup-window, object-store, and audit APIs.
- [x] Select ledger exports and native FDB sessions with validated bounded reads.
- [x] Resolve the UTC retention boundary through native restore dry-run. Select
  the newest restorable snapshot ending at or before its exact version and
  preserve the timestamp boundary with the native expiration guard.
- [x] Share process-lifetime locks between retention and supported restore drills.
- [x] Register default preview and explicit execution with AttachExternalPreview.
- [x] Record targets, partial outcomes, and errors without exposing secrets.
- [x] Supply private credential files to native tools and capture output without
  storing credentials in Docker metadata or logs.
- [x] Commit the runtime change with the Codex trailer.

## Task 2: Verify runtime behavior

Create focused internal/ops/backup_retention_*_test.go files and extend
internal/test/integration/runtime_audit_coverage_backup_test.go. Reuse real
SeaweedFS, FoundationDB, and ledger setup.

- [x] Test preview and expiration through public commands or exported runtime
  entry points against real object storage.
- [x] Cover cutoff boundaries, absent complete retained exports, malformed IDs,
  unrelated objects, pagination, repeat execution, and partial failure.
- [x] Run the pinned native FDB tool on temporary backups. Verify expiration
  followed by retained-boundary restoration, separate refusal without deletion,
  and closed-session deletion that preserves a usable surviving backup. Control
  the test clock rather than waiting fourteen days.
- [x] Run concurrent real retention and restore commands on shared temporary
  backups and require serialization through cancellation or completion.
- [x] Inspect permanent intent, steps, and outcomes and update datagen coverage.
- [x] Check private permissions, credential-free command/environment metadata,
  disabled persistent output, and cleanup using real native containers.
- [x] Run focused container checks and make build.
- [x] Commit verification changes and record exact results.

## Task 3: Schedule cleanup through Configs

Create common/services/tack-backup-retention.service.j2,
common/timers/tack-backup-retention.timer.j2, and
ansible/playbooks/tasks/tack-backup-retention.yml. Include the task in
deploy-tack.yml and declare schedule values beside ledger export configuration.

- [x] Follow existing export units and environment restrictions.
- [x] Schedule one audited command daily with a 14-day default.
- [x] Before enabling the timer, require the selected deployed image to pass
  `ops backup retention --help` through the deployment command interface.
- [x] Validate rendered units and Configs lint with existing tools.
- [x] Open the companion PR with the explicit Tack deployment prerequisite.

## Task 4: Review and publish evidence

- [x] Compare implementation and tests with every acceptance requirement.
- [x] Obtain independent review of deletion boundaries, native backup semantics,
  audit coverage, and scheduling.
- [x] Correct confirmed findings and rerun affected checks.
- [x] Publish the Tack PR with references, real verification evidence, and the
  Configs dependency.
- [x] Verify both PRs exist and distinguish unverified or undeployed behavior.
