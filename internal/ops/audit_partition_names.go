package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pg_partman reads the YYYY_MM_DD suffix of each audit.events child name as
// the Monday that starts the week of the child.
var auditWeekNamePattern = regexp.MustCompile(`^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$`)

var auditPartitionBoundPattern = regexp.MustCompile(`^FOR VALUES FROM \('([^']+)'\) TO \('([^']+)'\)$`)

const (
	// pg_get_expr prints each bound in the session time zone.
	// RunAuditPartitionNamesBackfill sets the session to UTC before the read.
	auditPartitionBoundLayout = "2006-01-02 15:04:05-07"
	auditWeek                 = 7 * 24 * time.Hour
)

// AuditPartitionRename is one child of audit.events renamed to the week form.
type AuditPartitionRename struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	LowerBound time.Time `json:"lower_bound"`
	UpperBound time.Time `json:"upper_bound"`
}

// AuditPartitionNamesResult lists the renames of one run.
type AuditPartitionNamesResult struct {
	Renames []AuditPartitionRename `json:"renames"`
}

// RunAuditPartitionNamesBackfill renames each audit.events child outside
// events_pYYYY_MM_DD to the Monday its one-week range starts on.
func RunAuditPartitionNamesBackfill(ctx context.Context, pool *pgxpool.Pool, dryRun bool) (AuditPartitionNamesResult, error) {
	result := AuditPartitionNamesResult{Renames: []AuditPartitionRename{}}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.acquire_failed", slog.String("err", err.Error()))
		return result, fmt.Errorf("acquire a ledger connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET TimeZone = 'UTC'`); err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.timezone_failed", slog.String("err", err.Error()))
		return result, fmt.Errorf("set the session time zone to UTC: %w", err)
	}
	renames, err := plannedAuditPartitionRenames(ctx, conn.Conn())
	if err != nil {
		return result, err
	}
	result.Renames = renames
	if dryRun {
		return result, nil
	}
	for _, rename := range renames {
		if err := renameAuditPartition(ctx, conn.Conn(), rename); err != nil {
			return result, err
		}
	}
	slog.InfoContext(ctx, "audit_partition_names.renamed", slog.Int("renamed", len(renames)))
	return result, nil
}

func plannedAuditPartitionRenames(ctx context.Context, conn *pgx.Conn) ([]AuditPartitionRename, error) {
	rows, err := conn.Query(ctx, `
		SELECT c.relname, pg_get_expr(c.relpartbound, c.oid)
		  FROM pg_inherits i
		  JOIN pg_class c ON c.oid = i.inhrelid
		  JOIN pg_class p ON p.oid = i.inhparent
		  JOIN pg_namespace n ON n.oid = p.relnamespace
		 WHERE n.nspname = 'audit' AND p.relname = 'events'
		 ORDER BY c.relname`)
	if err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.list_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("list the children of audit.events: %w", err)
	}
	names := map[string]bool{}
	bounds := map[string]string{}
	for rows.Next() {
		var name, bound string
		if err := rows.Scan(&name, &bound); err != nil {
			rows.Close()
			slog.ErrorContext(ctx, "audit_partition_names.scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("read a child of audit.events: %w", err)
		}
		names[name] = true
		bounds[name] = bound
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.list_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("list the children of audit.events: %w", err)
	}
	renames := []AuditPartitionRename{}
	var problems []error
	for name, bound := range bounds {
		if auditWeekNamePattern.MatchString(name) {
			continue
		}
		rename, err := weekRename(name, bound)
		if err == nil && names[rename.To] {
			err = fmt.Errorf("child %s: the target name %s exists", name, rename.To)
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		renames = append(renames, rename)
	}
	if err := errors.Join(problems...); err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.refused", slog.String("err", err.Error()))
		return nil, fmt.Errorf("refuse the audit partition renames: %w", err)
	}
	slices.SortFunc(renames, func(left, right AuditPartitionRename) int { return strings.Compare(left.From, right.From) })
	return renames, nil
}

func weekRename(name, bound string) (AuditPartitionRename, error) {
	match := auditPartitionBoundPattern.FindStringSubmatch(bound)
	if match == nil {
		return AuditPartitionRename{}, fmt.Errorf("child %s: bound %q is not a time range", name, bound)
	}
	lower, lowerErr := time.Parse(auditPartitionBoundLayout, match[1])
	upper, upperErr := time.Parse(auditPartitionBoundLayout, match[2])
	if lowerErr != nil || upperErr != nil {
		return AuditPartitionRename{}, fmt.Errorf("child %s: bound %q has a timestamp outside the form %s", name, bound, auditPartitionBoundLayout)
	}
	lower, upper = lower.UTC(), upper.UTC()
	weekStart := lower.Weekday() == time.Monday && lower.Equal(lower.Truncate(24*time.Hour))
	if !weekStart || !upper.Equal(lower.Add(auditWeek)) {
		return AuditPartitionRename{}, fmt.Errorf("child %s: range %s to %s is not one week from Monday 00:00 UTC", name, lower, upper)
	}
	return AuditPartitionRename{From: name, To: "events_p" + lower.Format("2006_01_02"), LowerBound: lower, UpperBound: upper}, nil
}

// The primary key rename runs before the table rename. A rerun after a failed
// table rename finds the key named To_pkey and renames only the table.
func renameAuditPartition(ctx context.Context, conn *pgx.Conn, rename AuditPartitionRename) error {
	table := pgx.Identifier{"audit", rename.From}.Sanitize()
	var key string
	if err := conn.QueryRow(ctx, `
		SELECT conname FROM pg_constraint
		 WHERE conrelid = $1::regclass AND contype = 'p'`, table).Scan(&key); err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.key_read_failed", slog.String("err", err.Error()))
		return fmt.Errorf("read the primary key of %s: %w", rename.From, err)
	}
	statements := []string{}
	if key != rename.To+"_pkey" {
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO %s", table,
			pgx.Identifier{key}.Sanitize(), pgx.Identifier{rename.To + "_pkey"}.Sanitize()))
	}
	statements = append(statements, fmt.Sprintf("ALTER TABLE %s RENAME TO %s", table, pgx.Identifier{rename.To}.Sanitize()))
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			slog.ErrorContext(ctx, "audit_partition_names.rename_failed", slog.String("err", err.Error()), slog.String("from", rename.From))
			return fmt.Errorf("rename %s to %s: %w", rename.From, rename.To, err)
		}
	}
	return nil
}
