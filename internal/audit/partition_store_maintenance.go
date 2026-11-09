package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// RunMaintenance calls audit.run_partition_maintenance using one pooled
// connection and resets search_path before releasing the connection.
//
// The function declares SET search_path = pg_catalog, partman, audit. The
// consumer runs batch transactions using the same pool.
//
// The original implementation rationale assumes that the session retains
// this search path after the function returns. Under that assumed path, an
// unqualified reference to a public-schema table fails with SQLSTATE 42P01.
//
//   - A successful reset restores search_path before connection release.
//   - A reset failure closes the connection before release.
func (s *pgPartitionStore) RunMaintenance(ctx context.Context) error {
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.partition.acquire_failed", slog.String("err", err.Error()))
		return fmt.Errorf("acquire a ledger connection for partition maintenance: %w", err)
	}
	defer connection.Release()
	_, maintenanceErr := connection.Exec(ctx, `SELECT audit.run_partition_maintenance()`)
	if maintenanceErr != nil {
		slog.ErrorContext(ctx, "audit.partition.maintenance_query_failed",
			slog.String("err", maintenanceErr.Error()),
		)
		maintenanceErr = fmt.Errorf("run partition maintenance: %w", maintenanceErr)
	}
	if _, err := connection.Exec(ctx, `RESET search_path`); err != nil {
		slog.ErrorContext(ctx, "audit.partition.search_path_reset_failed", slog.String("err", err.Error()))
		// The pool destroys a closed connection on release. A later batch transaction
		// cannot use that connection's maintenance search path.
		_ = connection.Conn().Close(context.WithoutCancel(ctx))
		return errors.Join(maintenanceErr, fmt.Errorf("reset search_path after partition maintenance: %w", err))
	}
	return maintenanceErr
}
