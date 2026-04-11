//go:build fdb

// cmd/backfill migrates structural entities from YugabyteDB to FoundationDB.
// Migrates orgs, workspaces, projects, states, and labels in order.
// Issues, epics, cycles, and modules are already in FDB and are not touched.
// Idempotent: safe to run multiple times.
//
// Usage:
//
//	CGO_ENABLED=1 go build -tags fdb -o bin/backfill ./cmd/backfill
//	DATABASE_URL=... FDB_CLUSTER_FILE=... ./bin/backfill
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/label"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/state"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()

	telemetry.Setup(telemetry.LogConfig{Level: "info"})

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL env var required")
		os.Exit(1)
	}
	fdbCluster := os.Getenv("FDB_CLUSTER_FILE")
	if fdbCluster == "" {
		fdbCluster = "/etc/foundationdb/fdb.cluster"
	}

	pool, err := postgres.NewPool(ctx, dbURL, nil)
	if err != nil {
		slog.Error("postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(fdbCluster, pool)
	if err != nil {
		slog.Error("foundationdb", "err", err)
		os.Exit(1)
	}

	seeder := service.NewWorkspaceSeeder(fdbStores.Properties, fdbStores.NodeTypes)

	b := &backfiller{
		pool:   pool,
		seeder: seeder,
		stores: fdbStores,
	}

	if err := b.run(ctx); err != nil {
		slog.Error("backfill failed", "err", err)
		os.Exit(1)
	}
	slog.Info("backfill complete")
}

type backfiller struct {
	pool   *pgxpool.Pool
	seeder *service.WorkspaceSeeder
	stores *fdbadapter.Stores
}

func (b *backfiller) run(ctx context.Context) error {
	orgs, err := b.migrateOrgs(ctx)
	if err != nil {
		return fmt.Errorf("orgs: %w", err)
	}

	if err := b.migrateWorkspaces(ctx, orgs); err != nil {
		return fmt.Errorf("workspaces: %w", err)
	}

	if err := b.migrateProjects(ctx); err != nil {
		return fmt.Errorf("projects: %w", err)
	}

	if err := b.migrateStates(ctx); err != nil {
		return fmt.Errorf("states: %w", err)
	}

	if err := b.migrateLabels(ctx); err != nil {
		return fmt.Errorf("labels: %w", err)
	}

	slog.Info("migration complete")
	return nil
}

func (b *backfiller) migrateOrgs(ctx context.Context) (map[uuid.UUID]*org.Org, error) {
	rows, err := b.pool.Query(ctx, `SELECT id, name, slug, created_at, updated_at FROM orgs ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID]*org.Org)
	for rows.Next() {
		o := &org.Org{}
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		if _, err := b.stores.Org.Create(ctx, o); err != nil {
			slog.Warn("org create", "id", o.ID, "err", err)
		}
		b.seeder.SeedOrg(ctx, o.ID)
		result[o.ID] = o
		slog.Info("org migrated", "id", o.ID, "slug", o.Slug)
	}
	return result, rows.Err()
}

func (b *backfiller) migrateWorkspaces(ctx context.Context, orgs map[uuid.UUID]*org.Org) error {
	rows, err := b.pool.Query(ctx, `SELECT id, org_id, name, slug, created_at, updated_at FROM workspaces ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		ws := &workspace.Workspace{}
		if err := rows.Scan(&ws.ID, &ws.OrgID, &ws.Name, &ws.Slug, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return err
		}
		_ = orgs
		if _, err := b.stores.Workspace.Create(ctx, ws); err != nil {
			slog.Warn("workspace create", "id", ws.ID, "err", err)
		}
		b.seeder.SeedWorkspace(ctx, ws.OrgID, ws.ID)
		n++
		slog.Info("workspace migrated", "id", ws.ID, "slug", ws.Slug)
	}
	slog.Info("workspaces migrated", "count", n)
	return rows.Err()
}

func (b *backfiller) migrateProjects(ctx context.Context) error {
	rows, err := b.pool.Query(ctx, `
		SELECT p.id, p.workspace_id, p.name, p.identifier,
		       COALESCE(p.description,''), p.network, p.default_state_id,
		       p.created_by, p.created_at, p.updated_at
		FROM projects p
		ORDER BY p.created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		p := &project.Project{}
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Identifier,
			&p.Description, &p.Network, &p.DefaultStateID,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return err
		}
		if _, err := b.stores.Project.Create(ctx, p); err != nil {
			slog.Warn("project create", "id", p.ID, "err", err)
		}
		n++
	}
	slog.Info("projects migrated", "count", n)
	return rows.Err()
}

func (b *backfiller) migrateStates(ctx context.Context) error {
	rows, err := b.pool.Query(ctx, `
		SELECT id, project_id, name, group_name, color, sort_order, created_at, updated_at
		FROM states
		ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		s := &state.State{}
		var groupName string
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &groupName, &s.Color, &s.SortOrder, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return err
		}
		s.GroupName = state.GroupName(groupName)
		if _, err := b.stores.State.Create(ctx, s); err != nil {
			slog.Warn("state create", "id", s.ID, "err", err)
		}
		n++
	}
	slog.Info("states migrated", "count", n)
	return rows.Err()
}

func (b *backfiller) migrateLabels(ctx context.Context) error {
	rows, err := b.pool.Query(ctx, `
		SELECT id, workspace_id, project_id, name, color, sort_order, created_at, updated_at
		FROM labels
		ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		l := &label.Label{}
		if err := rows.Scan(&l.ID, &l.WorkspaceID, &l.ProjectID, &l.Name, &l.Color, &l.SortOrder, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return err
		}
		if _, err := b.stores.Label.Create(ctx, l); err != nil {
			slog.Warn("label create", "id", l.ID, "err", err)
		}
		n++
	}
	slog.Info("labels migrated", "count", n)
	return rows.Err()
}
