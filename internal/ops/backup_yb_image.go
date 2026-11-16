// Ledger container commands require an explicitly configured engine image
// before creating resources. The binary does not define a default image tag
// that could disagree with the Compose image.

package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// requireBackupYBImage rejects an empty TACK_BACKUP_YB_IMAGE and logs the
// command-prefixed error under failEvent. Entry commands validate the image
// before opening a Docker client or creating containers.
func requireBackupYBImage(
	ctx context.Context,
	cfg *config.Config,
	command string,
	failEvent string,
) error {
	if cfg.BackupYBImage != "" {
		return nil
	}

	err := fmt.Errorf(
		"%s: TACK_BACKUP_YB_IMAGE is required; docker-compose.yml renders it for tack-ops from the yugabyte service's image",
		command,
	)
	telemetry.L(ctx).ErrorContext(ctx, failEvent, slog.String("err", err.Error()))

	return err
}
