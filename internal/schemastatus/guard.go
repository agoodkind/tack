package schemastatus

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"goodkind.io/tack/internal/adapters/postgres"
)

// Guard sets version requirements for one migration run. A zero field does not
// set a requirement.
type Guard struct {
	// ExpectFrom requires the database to be at this version before the run.
	ExpectFrom int64
	// To limits the run to migrations through this version.
	To int64
}

// Target returns the target migration version for guard and status. It
// returns an error when the database state or target version prevents the
// run.
func Target(ctx context.Context, status Status, guard Guard) (int64, error) {
	highest := status.Highest()
	if highest == 0 {
		return 0, refused(ctx, errors.New("the image embeds no migration"))
	}
	if guard.ExpectFrom != 0 && guard.ExpectFrom != status.AppliedVersion {
		return 0, refused(ctx, fmt.Errorf(
			"the database is at migration version %d, and --expect-from requires version %d",
			status.AppliedVersion, guard.ExpectFrom))
	}
	if guard.To == 0 {
		if status.AppliedVersion > highest {
			return 0, refused(ctx, fmt.Errorf(
				"the database is at migration version %d, above the highest embedded version %d",
				status.AppliedVersion, highest))
		}
		return highest, nil
	}
	if guard.To < status.AppliedVersion {
		return 0, refused(ctx, fmt.Errorf(
			"--to version %d is below the applied migration version %d", guard.To, status.AppliedVersion))
	}
	if guard.To > highest {
		return 0, refused(ctx, fmt.Errorf(
			"--to version %d is above the highest embedded migration version %d", guard.To, highest))
	}
	return guard.To, nil
}

// Apply checks guard against the migration state of the database at dsn,
// applies migrations through the target version, and reads the state again.
// It returns an error if the applied version differs from the target.
func Apply(ctx context.Context, dsn string, migrationsFS fs.FS, guard Guard) (int64, error) {
	before, err := Read(ctx, dsn, migrationsFS)
	if err != nil {
		return 0, err
	}
	target, err := Target(ctx, before, guard)
	if err != nil {
		return 0, err
	}
	slog.InfoContext(ctx, "schemastatus.apply_started",
		slog.Int64("applied_version", before.AppliedVersion), slog.Int64("target_version", target))
	if err := postgres.MigrateTo(ctx, dsn, migrationsFS, target); err != nil {
		slog.ErrorContext(ctx, "schemastatus.apply_failed",
			slog.Int64("target_version", target), slog.String("err", err.Error()))
		return 0, fmt.Errorf("apply migrations through version %d: %w", target, err)
	}
	after, err := Read(ctx, dsn, migrationsFS)
	if err != nil {
		return 0, err
	}
	if after.AppliedVersion != target {
		return 0, refused(ctx, fmt.Errorf(
			"the database is at migration version %d after the run, not the target version %d",
			after.AppliedVersion, target))
	}
	slog.InfoContext(ctx, "schemastatus.apply_finished", slog.Int64("applied_version", after.AppliedVersion))
	return after.AppliedVersion, nil
}

func refused(ctx context.Context, err error) error {
	slog.ErrorContext(ctx, "schemastatus.refused", slog.String("err", err.Error()))
	return err
}
