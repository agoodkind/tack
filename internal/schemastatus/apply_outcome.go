package schemastatus

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// plannedMigrations returns the embedded migrations above the applied
// version through target, in version order.
func plannedMigrations(status Status, target int64) []Migration {
	planned := make([]Migration, 0, len(status.Available))
	for _, migration := range status.Available {
		if migration.Version > status.AppliedVersion && migration.Version <= target {
			planned = append(planned, migration)
		}
	}
	return planned
}

// rangeTarget returns the external target of the migrations above
// fromVersion through toVersion.
func rangeTarget(fromVersion, toVersion int64) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Name: "", URI: "", RelationType: "",
		Reference: fmt.Sprintf("%s/%d-%d", migrationsReference, fromVersion, toVersion),
		ID:        uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// MigrationTargets returns one external target per migration, in the order of
// migrations.
func MigrationTargets(migrations []Migration) []audit.Target {
	targets := make([]audit.Target, 0, len(migrations))
	for _, migration := range migrations {
		targets = append(targets, audit.Target{
			Kind: audit.TargetKindExternal, NodeType: "", Name: "", URI: "", RelationType: "",
			Reference: migrationsReference + "/" + migration.Name,
			ID:        uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
		})
	}
	return targets
}

// observedOutcome returns the run outcome based on the final applied version.
// CommittedSteps lists the file names of planned migrations at or below that
// version.
func observedOutcome(planned []Migration, applied, target int64, migrateErr error) audit.OutcomeDetails {
	if applied == target {
		return completedOutcome()
	}
	committed := make([]string, 0, len(planned))
	for _, migration := range planned {
		if migration.Version <= applied {
			committed = append(committed, migration.Name)
		}
	}
	cause := migrateErr
	if cause == nil {
		cause = fmt.Errorf("the database is at migration version %d after the run, not the target version %d", applied, target)
	}
	if len(committed) == 0 {
		return failedOutcome(audit.OutcomeFailed, migrationFailedCode, cause)
	}
	details := failedOutcome(audit.OutcomePartiallyCompleted, migrationFailedCode, cause)
	details.CommittedSteps = committed
	return details
}

func applyResult(ctx context.Context, applied, target int64, details audit.OutcomeDetails, migrateErr error) (int64, audit.OutcomeDetails, error) {
	if migrateErr != nil {
		slog.ErrorContext(ctx, "schemastatus.apply_failed",
			slog.Int64("target_version", target), slog.String("err", migrateErr.Error()))
		return 0, details, fmt.Errorf("apply migrations through version %d: %w", target, migrateErr)
	}
	if applied != target {
		return 0, details, refused(ctx, fmt.Errorf(
			"the database is at migration version %d after the run, not the target version %d", applied, target))
	}
	slog.InfoContext(ctx, "schemastatus.apply_finished", slog.Int64("applied_version", applied))
	return applied, details, nil
}

func completedOutcome() audit.OutcomeDetails {
	return audit.OutcomeDetails{Outcome: audit.OutcomeCompleted, FailureCode: "", FailureMessage: "", CommittedSteps: nil}
}

func failedOutcome(outcome audit.Outcome, code string, cause error) audit.OutcomeDetails {
	return audit.OutcomeDetails{Outcome: outcome, FailureCode: code, FailureMessage: cause.Error(), CommittedSteps: nil}
}
