// Package schemastatus reads goose migration state in one read-only
// transaction and applies migrations after checking version requirements.
// Reads do not create the goose version table.
package schemastatus

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"goodkind.io/tack/internal/version"
)

// migrationGlob matches the embedded goose SQL files.
const migrationGlob = "*.sql"

// Migration represents one embedded goose migration file.
type Migration struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
}

// Status reports the database migration state relative to the embedded
// migrations. AppliedVersion is 0 when the goose version table does not exist.
type Status struct {
	MigrationTablePresent bool        `json:"migration_table_present"`
	AppliedVersion        int64       `json:"applied_version"`
	Available             []Migration `json:"available"`
	Pending               []Migration `json:"pending"`
	ImageCommit           string      `json:"image_commit"`
}

// Highest returns the highest embedded migration version.
func (s Status) Highest() int64 {
	if len(s.Available) == 0 {
		return 0
	}
	return s.Available[len(s.Available)-1].Version
}

// Summary returns the migration state as short text lines.
func (s Status) Summary() string {
	table := "absent"
	if s.MigrationTablePresent {
		table = "present"
	}
	pending := "none"
	if len(s.Pending) > 0 {
		names := make([]string, 0, len(s.Pending))
		for _, migration := range s.Pending {
			names = append(names, migration.Name)
		}
		pending = strings.Join(names, ", ")
	}
	return fmt.Sprintf(
		"migration table: %s\napplied version: %d\nhighest embedded version: %d\npending: %s\nimage commit: %s",
		table, s.AppliedVersion, s.Highest(), pending, s.ImageCommit)
}

// Available returns the embedded migrations in version order with the SHA-256
// hash of each file's contents.
func Available(ctx context.Context, migrationsFS fs.FS) ([]Migration, error) {
	names, err := fs.Glob(migrationsFS, migrationGlob)
	if err != nil {
		slog.ErrorContext(ctx, "schemastatus.list_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("list the embedded migrations %s: %w", migrationGlob, err)
	}
	available := make([]Migration, 0, len(names))
	for _, name := range names {
		migrationVersion, err := goose.NumericComponent(name)
		if err != nil {
			slog.ErrorContext(ctx, "schemastatus.version_parse_failed",
				slog.String("migration", name), slog.String("err", err.Error()))
			return nil, fmt.Errorf("read the version of migration %s: %w", name, err)
		}
		contents, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			slog.ErrorContext(ctx, "schemastatus.read_file_failed",
				slog.String("migration", name), slog.String("err", err.Error()))
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(contents)
		available = append(available, Migration{
			Version: migrationVersion, Name: name, SHA256: hex.EncodeToString(sum[:]),
		})
	}
	slices.SortFunc(available, func(left, right Migration) int {
		return cmp.Compare(left.Version, right.Version)
	})
	return available, nil
}

// Read reports the migration state of the database at dsn in one read-only
// transaction. It uses to_regclass to check whether the goose version table
// exists and considers only rows with is_applied when reading the highest
// applied version.
func Read(ctx context.Context, dsn string, migrationsFS fs.FS) (Status, error) {
	available, err := Available(ctx, migrationsFS)
	if err != nil {
		return Status{}, err
	}
	present, applied, err := readApplied(ctx, dsn)
	if err != nil {
		return Status{}, err
	}
	pending := make([]Migration, 0, len(available))
	for _, migration := range available {
		if migration.Version > applied {
			pending = append(pending, migration)
		}
	}
	slog.InfoContext(ctx, "schemastatus.read",
		slog.Bool("migration_table_present", present),
		slog.Int64("applied_version", applied),
		slog.Int("pending", len(pending)))
	return Status{
		MigrationTablePresent: present,
		AppliedVersion:        applied,
		Available:             available,
		Pending:               pending,
		ImageCommit:           version.Commit(),
	}, nil
}

func readApplied(ctx context.Context, dsn string) (bool, int64, error) {
	table := goose.DefaultTablename
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		slog.ErrorContext(ctx, "schemastatus.connect_failed",
			slog.String("table", table), slog.String("err", err.Error()))
		return false, 0, fmt.Errorf("connect to read the migration table %s: %w", table, err)
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	transaction, err := connection.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		slog.ErrorContext(ctx, "schemastatus.begin_failed",
			slog.String("table", table), slog.String("err", err.Error()))
		return false, 0, fmt.Errorf("begin the read-only read of the migration table %s: %w", table, err)
	}
	defer func() { _ = transaction.Rollback(context.WithoutCancel(ctx)) }()
	var present bool
	if err := transaction.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&present); err != nil {
		slog.ErrorContext(ctx, "schemastatus.table_check_failed",
			slog.String("table", table), slog.String("err", err.Error()))
		return false, 0, fmt.Errorf("check for the migration table %s: %w", table, err)
	}
	var applied int64
	if present {
		query := `SELECT COALESCE(max(version_id), 0) FROM ` + pgx.Identifier{table}.Sanitize() + ` WHERE is_applied`
		if err := transaction.QueryRow(ctx, query).Scan(&applied); err != nil {
			slog.ErrorContext(ctx, "schemastatus.version_read_failed",
				slog.String("table", table), slog.String("err", err.Error()))
			return false, 0, fmt.Errorf("read the applied version from %s: %w", table, err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "schemastatus.commit_failed",
			slog.String("table", table), slog.String("err", err.Error()))
		return false, 0, fmt.Errorf("end the read-only read of the migration table %s: %w", table, err)
	}
	return present, applied, nil
}
