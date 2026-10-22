package schemastatus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

func refused(ctx context.Context, err error) error {
	slog.ErrorContext(ctx, "schemastatus.refused", slog.String("err", err.Error()))
	return err
}
