# Backup retention implementation plan

> Use `subagent-driven-development` or `executing-plans` to implement the plan task by task. Track steps with checkbox syntax (`- [ ]`).

**Goal:** Preview and automatically enforce a 14-day backup restore window with audited cleanup.

**Architecture:** Tack Go selects and removes expired backup sets. Native FoundationDB expiration preserves snapshot/log dependencies. A Configs companion change schedules the existing operator container daily.

**Tech Stack:** Go, Cobra, the existing audit recorder, AWS S3 SDK, Docker SDK, FoundationDB 7.4.6, and Configs systemd templates.

**Spec:** [Fourteen-day backup retention](../specs/2026-10-23-backup-retention-design.md)

## Global constraints

Use isolated worktrees and preserve concurrent changes.
Keep secrets and backup contents out of logs, chat, fixtures, and permanent audit evidence.
Use real dependencies through the existing test runner. Do not add mocks or scripts.
Keep each new source file below 200 lines and split by responsibility.
Open ready-for-review PRs. Validate cleanup on QA before production activation.

## Review focus

- An absent complete recent ledger export must prevent deletion of expired exports.
- A native FoundationDB expiration refusal must preserve every required restore dependency.
- Invalid run prefixes must not select another namespace.
- A partial object-store deletion must report partial completion and retain audit evidence.
- A concurrent restore drill must finish reading its backup before cleanup can delete it.

## Task 1: Implement selection and audited execution

**Files:** Create `internal/ops/backup_retention.go`, `backup_retention_ledger.go`, `backup_retention_fdb.go`, and focused companion files as needed. Create `cli_backup_retention.go` and `internal/audit/verbs_backup_retention.go`. Modify registration in `cli_backup.go` and update `datagen_ledger_verbs.go`.

**Interfaces:** `BackupRetentionOptions` supplies a positive `KeepDays` value. `RunBackupRetention(context.Context, *config.Config, BackupRetentionOptions, io.Writer, audit.StepRecorder) (audit.OutcomeDetails, error)` executes cleanup. A separate preview entry point uses the same selection without effects. `backupRetentionCommand(*cli.Factory) *cobra.Command` registers `ops backup retention --keep-days`, default 14. Use the existing `opsNow` time source.

- [x] Read current command, manifest, backup-window, object-store, and audit helpers.
- [x] Implement validated, bounded selection for ledger exports and native FoundationDB sessions.
- [x] Resolve the retained UTC boundary to an exact version through native restore dry-run, select the newest restorable snapshot ending at or before that version, and preserve the timestamp boundary with the native expiration guard.
- [x] Add shared process-lifetime locking for cleanup and supported restore drills.
- [x] Register default preview and explicit execution through `AttachExternalPreview`.
- [x] Record effect targets, partial completion, and secret-safe errors.
- [x] Commit the coherent runtime change with the Codex trailer.

## Task 2: Verify the complete runtime behavior

**Files:** Create focused `internal/ops/backup_retention_*_test.go` files and extend runtime audit coverage in `internal/test/integration/runtime_audit_coverage_backup_test.go`. Reuse real SeaweedFS, FoundationDB, and ledger test helpers.

- [x] Test preview and actual expiration through the public command or exported runtime entry point against a real object store.
- [x] Cover cutoff boundaries, no complete retained export, malformed identifiers, unrelated objects, pagination, repeated execution, and partial failure.
- [x] Execute the pinned native FoundationDB tool against real temporary backup data. Prove successful expiration followed by restoration at the retained boundary, a separate native refusal without deletion, and closed-session deletion that preserves the surviving session. Control the test clock rather than waiting fourteen days.
- [x] Run real concurrent retention and restore commands against shared temporary backup storage and prove their operations serialize until cancellation or completion.
- [x] Verify real audit intent, step, and terminal records; update datagen reporting.
- [x] Run the smallest relevant container test commands and `make build`.
- [x] Commit the verification changes and record exact results.

## Task 3: Schedule cleanup in Configs

**Files:** Create `common/services/tack-backup-retention.service.j2`, `common/timers/tack-backup-retention.timer.j2`, and `ansible/playbooks/tasks/tack-backup-retention.yml`. Add the include to `deploy-tack.yml` and schedule variables beside the existing ledger export values.

- [x] Follow the existing ledger export units and environment gates.
- [x] Schedule one daily invocation of the audited Tack command, defaulting to 14 days.
- [x] Require the selected deployed image to pass `ops backup retention --help` through the existing deployment command mechanism before enabling the timer.
- [x] Validate rendered units and Configs lint with the existing tools.
- [x] Open a companion PR with an explicit runtime deployment prerequisite.

## Task 4: Review and publish

- [x] Compare implementation and tests with every acceptance requirement.
- [x] Obtain an independent review of deletion boundaries, native backup semantics, audit coverage, and scheduling.
- [x] Correct verified findings and rerun affected checks.
- [x] Publish the Tack PR with direct references, material test evidence, and the Configs dependency.
- [x] Verify both PRs exist and report unverified or undeployed behavior accurately.
