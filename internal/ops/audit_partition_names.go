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

// auditWeekNamePattern is the child name pg_partman parses as the start of a
// week. Migration 017 refuses every other name.
var auditWeekNamePattern = regexp.MustCompile(`^events_p[0-9]{4}_[0-9]{2}_[0-9]{2}$`)

// auditPartitionBoundPattern matches the range bound of an audit.events child
// in a UTC session.
var auditPartitionBoundPattern = regexp.MustCompile(`^FOR VALUES FROM \('([^']+)'\) TO \('([^']+)'\)$`)

const (
	// auditPartitionBoundLayout is the timestamp form of a bound in a UTC session.
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

// RunAuditPartitionNamesBackfill finds each child of audit.events with a name
// outside events_pYYYY_MM_DD. A child that covers exactly one week starting
// Monday 00:00 UTC is renamed to the name of that Monday, with its primary
// key. Any other such child, or a target name that exists, fails the run
// before any rename. A dry run lists the renames and changes nothing.
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

// plannedAuditPartitionRenames reads every child of audit.events and returns
// the renames for the children outside the week form.
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
		rename, err := weekRename(ctx, name, bound)
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

// weekRename returns the rename of one child from its range bound, or an
// error when the range is not one week starting Monday 00:00 UTC.
func weekRename(ctx context.Context, name, bound string) (AuditPartitionRename, error) {
	match := auditPartitionBoundPattern.FindStringSubmatch(bound)
	if match == nil {
		return AuditPartitionRename{}, fmt.Errorf("child %s: bound %q is not a time range", name, bound)
	}
	lower, lowerErr := time.Parse(auditPartitionBoundLayout, match[1])
	upper, upperErr := time.Parse(auditPartitionBoundLayout, match[2])
	if err := errors.Join(lowerErr, upperErr); err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.bound_parse_failed", slog.String("err", err.Error()), slog.String("child", name))
		return AuditPartitionRename{}, fmt.Errorf("child %s: parse bound %q: %w", name, bound, err)
	}
	lower, upper = lower.UTC(), upper.UTC()
	weekStart := lower.Weekday() == time.Monday && lower.Equal(lower.Truncate(24*time.Hour))
	if !weekStart || !upper.Equal(lower.Add(auditWeek)) {
		return AuditPartitionRename{}, fmt.Errorf("child %s: range %s to %s is not one week from Monday 00:00 UTC", name, lower, upper)
	}
	return AuditPartitionRename{From: name, To: "events_p" + lower.Format("2006_01_02"), LowerBound: lower, UpperBound: upper}, nil
}

// renameAuditPartition renames the primary key and then the table. A rerun
// after a failure between the two statements finds the key renamed.
func renameAuditPartition(ctx context.Context, conn *pgx.Conn, rename AuditPartitionRename) error {
	table := pgx.Identifier{"audit", rename.From}.Sanitize()
	var keyCount int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		 WHERE conrelid = $1::regclass AND conname = $2`, table, rename.From+"_pkey").Scan(&keyCount); err != nil {
		slog.ErrorContext(ctx, "audit_partition_names.key_read_failed", slog.String("err", err.Error()))
		return fmt.Errorf("read the primary key of %s: %w", rename.From, err)
	}
	statements := []string{}
	if keyCount == 1 {
		statements = append(statements, fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO %s", table,
			pgx.Identifier{rename.From + "_pkey"}.Sanitize(), pgx.Identifier{rename.To + "_pkey"}.Sanitize()))
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
