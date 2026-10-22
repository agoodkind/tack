package schemastatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
)

const (
	// migrationsReference is the external reference of the migration state
	// of the database.
	migrationsReference = "database/migrations"
	// guardRefusedCode is the error code for a run that fails a version
	// requirement before applying any migration.
	guardRefusedCode = "version_guard_refused"
	// statusUnreadableCode is the error code of a run that could not read the
	// migration state.
	statusUnreadableCode = "migration_state_unreadable"
	// migrationFailedCode is the error code of a run that stopped before the
	// target version.
	migrationFailedCode = "migration_failed"
)

// MigrationsTarget returns the external target of the database migration
// state.
func MigrationsTarget() audit.Target {
	return audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Reference: migrationsReference, Name: "", URI: "",
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// migrationRange contains the migration plan that BeginStep saves before
// the run.
type migrationRange struct {
	FromVersion int64       `json:"from_version"`
	ToVersion   int64       `json:"to_version"`
	ImageCommit string      `json:"image_commit"`
	Migrations  []Migration `json:"migrations"`
}

// migrationResult is the evidence that the step saves after the run: the
// applied version that the database reports.
type migrationResult struct {
	AppliedVersion int64 `json:"applied_version"`
}

// ApplyRecorded checks guard against the database migration state at dsn
// before applying migrations through the target version. It returns refused
// if guard rejects the run before any migration runs.
//
// Before the run, ApplyRecorded saves one step with the version range and
// each planned migration file's SHA-256 digest. After the run, it reads the
// applied version again and saves the step result. It determines the outcome:
//
//   - It returns completed at the target version.
//   - It returns partially_completed with the file names of applied migrations
//     if the run stops below the target after applying some migrations.
//   - It returns failed if the run stops below the target without applying any
//     migration.
//
// If the second read fails, ApplyRecorded returns incomplete.
func ApplyRecorded(
	ctx context.Context,
	dsn string,
	migrationsFS fs.FS,
	guard Guard,
	steps audit.StepRecorder,
) (int64, audit.OutcomeDetails, error) {
	before, err := Read(ctx, dsn, migrationsFS)
	if err != nil {
		return 0, failedOutcome(audit.OutcomeFailed, statusUnreadableCode, err), err
	}
	target, err := Target(ctx, before, guard)
	if err != nil {
		return 0, failedOutcome(audit.OutcomeRefused, guardRefusedCode, err), err
	}
	planned := plannedMigrations(before, target)
	if len(planned) == 0 {
		return before.AppliedVersion, completedOutcome(), nil
	}
	evidence, err := json.Marshal(migrationRange{
		FromVersion: before.AppliedVersion, ToVersion: target, ImageCommit: before.ImageCommit, Migrations: planned,
	})
	if err != nil {
		slog.ErrorContext(ctx, "schemastatus.evidence_encode_failed", slog.String("err", err.Error()))
		return 0, failedOutcome(audit.OutcomeFailed, migrationFailedCode, err), fmt.Errorf("encode the migration range evidence: %w", err)
	}
	step := audit.ExternalStep(rangeTarget(before.AppliedVersion, target), evidence)
	if err := steps.BeginStep(ctx, step); err != nil {
		slog.ErrorContext(ctx, "schemastatus.step_record_failed", slog.String("err", err.Error()))
		return 0, failedOutcome(audit.OutcomeFailed, migrationFailedCode, err), fmt.Errorf("record the migration range: %w", err)
	}
	slog.InfoContext(ctx, "schemastatus.apply_started",
		slog.Int64("applied_version", before.AppliedVersion), slog.Int64("target_version", target))
	migrateErr := postgres.MigrateTo(ctx, dsn, migrationsFS, target)
	after, readErr := Read(ctx, dsn, migrationsFS)
	if readErr != nil {
		return 0, failedOutcome(audit.OutcomeIncomplete, statusUnreadableCode, readErr), errors.Join(migrateErr, readErr)
	}
	details := observedOutcome(planned, after.AppliedVersion, target, migrateErr)
	result, err := json.Marshal(migrationResult{AppliedVersion: after.AppliedVersion})
	if err != nil {
		slog.ErrorContext(ctx, "schemastatus.evidence_encode_failed", slog.String("err", err.Error()))
		return 0, details, fmt.Errorf("encode the migration result evidence: %w", err)
	}
	if err := steps.FinishStep(ctx, audit.FinishedStep(step, details, result)); err != nil {
		slog.ErrorContext(ctx, "schemastatus.step_record_failed", slog.String("err", err.Error()))
		return 0, details, errors.Join(migrateErr, fmt.Errorf("record the migration result: %w", err))
	}
	return applyResult(ctx, after.AppliedVersion, target, details, migrateErr)
}
