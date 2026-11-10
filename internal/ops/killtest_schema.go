package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	killtestSchema              = "killtest"
	dropKilltestSchemaStatement = "DROP SCHEMA " + killtestSchema + " CASCADE"
)

// PostgreSQL permits DROP SCHEMA to a superuser or a member of the role
// that owns the schema.
const killtestSchemaStateQuery = `
	SELECT session_user::text,
	       n.oid IS NOT NULL,
	       COALESCE(pg_get_userbyid(n.nspowner), ''),
	       COALESCE(r.rolsuper OR pg_has_role(session_user, n.nspowner, 'USAGE'), false)
	  FROM pg_roles r
	  LEFT JOIN pg_namespace n ON n.nspname = $1
	 WHERE r.rolname = session_user`

const killtestSchemaTablesQuery = `
	SELECT c.relname
	  FROM pg_class c
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	 WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')
	 ORDER BY c.relname`

// KilltestSchemaTable is one table of the killtest schema.
type KilltestSchemaTable struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// KilltestSchemaResult reports the killtest schema that one run found and
// whether the run dropped the schema.
type KilltestSchemaResult struct {
	SessionRole string                `json:"session_role"`
	Exists      bool                  `json:"exists"`
	Owner       string                `json:"owner"`
	Tables      []KilltestSchemaTable `json:"tables"`
	Dropped     bool                  `json:"dropped"`
}

// RunDropKilltestSchema reports the killtest schema of the ledger database and,
// unless dryRun is set, drops the schema with its tables.
func RunDropKilltestSchema(ctx context.Context, pool *pgxpool.Pool, dryRun bool) (KilltestSchemaResult, error) {
	result := KilltestSchemaResult{SessionRole: "", Exists: false, Owner: "", Tables: []KilltestSchemaTable{}, Dropped: false}
	var canDrop bool
	if err := pool.QueryRow(ctx, killtestSchemaStateQuery, killtestSchema).Scan(
		&result.SessionRole, &result.Exists, &result.Owner, &canDrop); err != nil {
		slog.ErrorContext(ctx, "killtest_schema.state_read_failed", slog.String("err", err.Error()))
		return result, fmt.Errorf("read the login and the %s schema: %w", killtestSchema, err)
	}
	if !result.Exists {
		slog.InfoContext(ctx, "killtest_schema.absent", slog.String("session_role", result.SessionRole))
		return result, nil
	}
	if !canDrop {
		refused := fmt.Errorf("the login %s cannot drop the schema %s: the drop needs a superuser login or membership in the owner role %s",
			result.SessionRole, killtestSchema, result.Owner)
		slog.ErrorContext(ctx, "killtest_schema.login_refused", slog.String("err", refused.Error()))
		return result, refused
	}
	tables, err := killtestSchemaTables(ctx, pool)
	if err != nil {
		return result, err
	}
	result.Tables = tables
	if err := refuseKilltestSchemaBlockers(ctx, pool); err != nil {
		return result, err
	}
	if dryRun {
		return result, nil
	}
	if _, err := pool.Exec(ctx, dropKilltestSchemaStatement); err != nil {
		slog.ErrorContext(ctx, "killtest_schema.drop_failed", slog.String("err", err.Error()))
		return result, fmt.Errorf("run %q as %s: %w", dropKilltestSchemaStatement, result.SessionRole, err)
	}
	result.Dropped = true
	slog.InfoContext(ctx, "killtest_schema.dropped",
		slog.String("session_role", result.SessionRole), slog.String("owner", result.Owner), slog.Int("tables", len(tables)))
	return result, nil
}

func killtestSchemaTables(ctx context.Context, pool *pgxpool.Pool) ([]KilltestSchemaTable, error) {
	names, err := killtestSchemaStrings(ctx, pool, killtestSchemaTablesQuery)
	if err != nil {
		return nil, err
	}
	tables := make([]KilltestSchemaTable, 0, len(names))
	for _, name := range names {
		table := KilltestSchemaTable{Name: name, Rows: 0}
		count := "SELECT count(*) FROM " + pgx.Identifier{killtestSchema, name}.Sanitize()
		if err := pool.QueryRow(ctx, count).Scan(&table.Rows); err != nil {
			slog.ErrorContext(ctx, "killtest_schema.count_failed", slog.String("err", err.Error()), slog.String("table", name))
			return nil, fmt.Errorf("count the rows of %s.%s: %w", killtestSchema, name, err)
		}
		tables = append(tables, table)
	}
	return tables, nil
}

// killtestSchemaStrings runs a catalog query that takes the schema name and
// returns one text column.
func killtestSchemaStrings(ctx context.Context, pool *pgxpool.Pool, query string) ([]string, error) {
	rows, err := pool.Query(ctx, query, killtestSchema)
	if err != nil {
		slog.ErrorContext(ctx, "killtest_schema.catalog_read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the catalog for the %s schema: %w", killtestSchema, err)
	}
	values, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		slog.ErrorContext(ctx, "killtest_schema.catalog_read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the catalog for the %s schema: %w", killtestSchema, err)
	}
	return values, nil
}

func refuseKilltestSchemaBlockers(ctx context.Context, pool *pgxpool.Pool) error {
	var problems []error
	for _, check := range []struct{ query, refusal string }{
		{killtestSchemaOtherObjectsQuery, "the schema contains an object other than a table"},
		{killtestSchemaDependentsQuery, "an object in audit, partman, or public depends on the schema"},
	} {
		found, err := killtestSchemaStrings(ctx, pool, check.query)
		if err != nil {
			return err
		}
		for _, object := range found {
			problems = append(problems, fmt.Errorf("%s: %s", check.refusal, object))
		}
	}
	if err := errors.Join(problems...); err != nil {
		slog.ErrorContext(ctx, "killtest_schema.refused", slog.String("err", err.Error()))
		return fmt.Errorf("refuse to drop the schema %s: %w", killtestSchema, err)
	}
	return nil
}
