// Backup commands require an explicit ledger engine image before creating
// resources. The yb-admin containers, SQL dump containers, and temporary
// restore-drill yugabyted use this check. The binary does not provide a
// default image tag that could differ from the Compose image selection.

package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// requireBackupYBImage rejects an empty TACK_BACKUP_YB_IMAGE. The error
// identifies the variable and command. The helper logs the error with
// failEvent. Run the check before opening a Docker client or creating a
// container.
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
