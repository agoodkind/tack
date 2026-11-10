package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// RunMaintenance calls audit.run_partition_maintenance on one pooled
// connection and resets search_path on that connection before it returns to
// the pool. The function declares SET search_path = pg_catalog, partman,
// audit, and the session keeps that search path after the call returns. The
// consumer runs batch transactions on the same pool. On a connection with
// that search path, an unqualified reference to a table in the public schema
// fails with SQLSTATE 42P01.
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
		/* The pool destroys a closed connection on release, and a batch
		   transaction never receives the maintenance search path. */
		_ = connection.Conn().Close(context.WithoutCancel(ctx))
		return errors.Join(maintenanceErr, fmt.Errorf("reset search_path after partition maintenance: %w", err))
	}
	return maintenanceErr
}
