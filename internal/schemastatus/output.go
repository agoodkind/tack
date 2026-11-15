package schemastatus

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"

	"goodkind.io/tack/internal/audit"
)

// ReadOutput reads the applied and pending migrations.
//
// It returns JSON or the text summary for the migrate command.
//
//	output, err := ReadOutput(ctx, dsn, migrationsFS, true)
func ReadOutput(
	ctx context.Context,
	dsn string,
	migrationsFS fs.FS,
	jsonOutput bool,
) (audit.OperatorReadOutput, error) {
	status, err := Read(ctx, dsn, migrationsFS)
	if err != nil {
		return audit.OperatorReadOutput{}, err
	}

	if !jsonOutput {
		return audit.OperatorReadOutput{
			Payload: []byte(status.Summary()),
		}, nil
	}

	body, err := json.Marshal(status)
	if err != nil {
		slog.ErrorContext(ctx, "migrate.status_encode_failed", slog.String("err", err.Error()))

		return audit.OperatorReadOutput{}, fmt.Errorf(
			"encode the migration state at version %d: %w",
			status.AppliedVersion,
			err,
		)
	}

	return audit.OperatorReadOutput{Payload: body}, nil
}
