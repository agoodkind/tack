package audit

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

// pg_partman parses every child name of audit.events as a date. One child
// named outside events_pYYYY_MM_DD fails every maintenance run (TACK-551).
const strayChildrenQuery = `
	SELECT c.relname FROM pg_inherits i
	JOIN pg_class c ON c.oid = i.inhrelid
	JOIN pg_class p ON p.oid = i.inhparent
	JOIN pg_namespace n ON n.oid = p.relnamespace
	WHERE n.nspname = 'audit' AND p.relname = 'events'
	AND c.relname !~ '^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$'
	ORDER BY c.relname`

func (s *pgPartitionStore) StrayChildren(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, strayChildrenQuery)
	if err != nil {
		slog.ErrorContext(ctx, "audit.partition.stray_query_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("stray child query: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			slog.ErrorContext(ctx, "audit.partition.stray_query_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("stray child scan: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.partition.stray_query_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("stray child rows: %w", err)
	}
	return names, nil
}

func (m *PartitionManager) reportStrayChildren(ctx context.Context) {
	names, err := m.store.StrayChildren(ctx)
	if err != nil {
		telemetry.L(ctx).Error("audit.partition.stray_check_failed", slog.String("err", err.Error()))
		return
	}
	telemetry.SetAuditPartitionStrayChildren(int64(len(names)))
	if len(names) > 0 {
		telemetry.L(ctx).Error("audit.partition.stray_child",
			slog.Int("stray_children", len(names)),
			slog.Any("names", names),
			slog.String("err", "audit.events has children named outside events_pYYYY_MM_DD"),
		)
	}
}
