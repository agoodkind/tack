package datagen

import (
	"context"
	"fmt"
	"log/slog"
)

func loggedError(ctx context.Context, message string, err error) error {
	slog.ErrorContext(ctx, "qa.datagen.failed", slog.String("err", err.Error()))
	return fmt.Errorf("%s: %w", message, err)
}
