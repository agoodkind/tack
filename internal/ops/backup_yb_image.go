// backup_yb_image.go is the one gate on the ledger's engine image for every
// command that runs a container from it: the yb-admin one-shots, the SQL
// dumps, and the restore drill's throwaway yugabyted. The image has no
// compiled default, because a default in the binary is a second home for the
// tag that drifts from the compose pin, so each entry command asks here before
// it creates anything.

package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// requireBackupYBImage refuses an empty TACK_BACKUP_YB_IMAGE with an error
// naming the variable, logged under failEvent and prefixed with command the way
// the other required-value checks in this family are. Call it before any Docker
// client is opened or container created.
func requireBackupYBImage(ctx context.Context, cfg *config.Config, command, failEvent string) error {
	if cfg.BackupYBPITRImage != "" {
		return nil
	}
	err := fmt.Errorf("%s: TACK_BACKUP_YB_IMAGE is required; docker-compose.yml renders it for tack-ops from the yugabyte service's image", command)
	telemetry.L(ctx).ErrorContext(ctx, failEvent, slog.String("err", err.Error()))
	return err
}
