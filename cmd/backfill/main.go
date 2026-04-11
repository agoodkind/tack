//go:build fdb

// cmd/backfill migrates all entity data from YugabyteDB to FoundationDB.
// It reads issues, epics, cycles, and modules from SQL and writes them as
// NodeValue + PropertyValue records into FDB. Idempotent: running it twice
// is safe because EntityRepository.Set overwrites any existing record.
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
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/node"
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

	fdbStores, err := fdbadapter.NewStores(fdbCluster)
	if err != nil {
		slog.Error("foundationdb", "err", err)
		os.Exit(1)
	}

	seeder := service.NewWorkspaceSeeder(fdbStores.Properties)

	b := &backfiller{
		pool:     pool,
		entities: fdbStores.Entities,
		seeder:   seeder,
	}

	if err := b.run(ctx); err != nil {
		slog.Error("backfill failed", "err", err)
		os.Exit(1)
	}
	slog.Info("backfill complete")
}

type backfiller struct {
	pool     *pgxpool.Pool
	entities node.EntityRepository
	seeder   *service.WorkspaceSeeder
}

func (b *backfiller) run(ctx context.Context) error {
	wss, err := b.listWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	slog.Info("found workspaces", "count", len(wss))

	for _, ws := range wss {
		slog.Info("seeding workspace", "id", ws.id, "org_id", ws.orgID)
		b.seeder.SeedWorkspace(ctx, ws.orgID, ws.id)
	}

	// Build workspace -> orgID lookup.
	wsOrg := make(map[uuid.UUID]uuid.UUID, len(wss))
	for _, ws := range wss {
		wsOrg[ws.id] = ws.orgID
	}

	totals := struct{ epics, issues, cycles, modules int }{}

	// Epics first (issues reference them).
	epics, err := b.backfillEpics(ctx, wsOrg)
	if err != nil {
		return fmt.Errorf("backfill epics: %w", err)
	}
	totals.epics = epics

	issues, err := b.backfillIssues(ctx, wsOrg)
	if err != nil {
		return fmt.Errorf("backfill issues: %w", err)
	}
	totals.issues = issues

	cycles, err := b.backfillCycles(ctx, wsOrg)
	if err != nil {
		return fmt.Errorf("backfill cycles: %w", err)
	}
	totals.cycles = cycles

	modules, err := b.backfillModules(ctx, wsOrg)
	if err != nil {
		return fmt.Errorf("backfill modules: %w", err)
	}
	totals.modules = modules

	slog.Info("backfill summary",
		"epics", totals.epics,
		"issues", totals.issues,
		"cycles", totals.cycles,
		"modules", totals.modules,
	)
	return nil
}

// ---- workspace list ----

type wsRow struct {
	id    uuid.UUID
	orgID uuid.UUID
}

func (b *backfiller) listWorkspaces(ctx context.Context) ([]wsRow, error) {
	rows, err := b.pool.Query(ctx, `SELECT id, org_id FROM workspaces ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wsRow
	for rows.Next() {
		var r wsRow
		if err := rows.Scan(&r.id, &r.orgID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- epics ----

func (b *backfiller) backfillEpics(ctx context.Context, wsOrg map[uuid.UUID]uuid.UUID) (int, error) {
	const q = `
		SELECT id, workspace_id, project_id, parent_id, state_id,
		       name, description, priority, sequence_id, sort_order,
		       start_date, target_date, is_draft,
		       created_by, updated_by, created_at, updated_at
		FROM epics
		WHERE deleted_at IS NULL
		ORDER BY created_at`

	rows, err := b.pool.Query(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var (
			id, wsID, projID         uuid.UUID
			parentID, stateID        *uuid.UUID
			name, desc, priorityStr  string
			seqID                    int
			sortOrder                float64
			startDate, targetDate    *time.Time
			isDraft                  bool
			createdBy                uuid.UUID
			updatedBy                *uuid.UUID
			createdAt, updatedAt     time.Time
		)
		if err := rows.Scan(
			&id, &wsID, &projID, &parentID, &stateID,
			&name, &desc, &priorityStr, &seqID, &sortOrder,
			&startDate, &targetDate, &isDraft,
			&createdBy, &updatedBy, &createdAt, &updatedAt,
		); err != nil {
			return n, fmt.Errorf("epic scan: %w", err)
		}
		orgID, ok := wsOrg[wsID]
		if !ok {
			slog.Warn("epic: unknown workspace", "epic_id", id, "workspace_id", wsID)
			continue
		}

		updBy := createdBy
		if updatedBy != nil {
			updBy = *updatedBy
		}

		nv := &node.NodeValue{
			ID:          id,
			OrgID:       orgID,
			WorkspaceID: wsID,
			ProjectID:   projID,
			NodeType:    node.NodeTypeEpic,
			Name:        name,
			Description: desc,
			StateID:     stateID,
			ParentID:    parentID,
			SequenceID:  int32(seqID),
			SortOrder:   sortOrder,
			CreatedBy:   createdBy,
			UpdatedBy:   updBy,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		}

		props := epicProps(wsID, issue.Priority(priorityStr), startDate, targetDate, isDraft)

		if err := b.entities.Set(ctx, nv, props, nil); err != nil {
			return n, fmt.Errorf("epic %s set: %w", id, err)
		}
		n++
		if n%100 == 0 {
			slog.Info("epic progress", "count", n)
		}
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	slog.Info("epics backfilled", "count", n)
	return n, nil
}

// ---- issues ----

func (b *backfiller) backfillIssues(ctx context.Context, wsOrg map[uuid.UUID]uuid.UUID) (int, error) {
	const q = `
		SELECT id, workspace_id, project_id, epic_id, parent_id, state_id,
		       name, description, priority, sequence_id, sort_order,
		       start_date, target_date, is_draft,
		       created_by, updated_by, created_at, updated_at
		FROM issues
		WHERE deleted_at IS NULL
		ORDER BY created_at`

	rows, err := b.pool.Query(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var (
			id, wsID, projID                   uuid.UUID
			epicID, parentID, stateID          *uuid.UUID
			name, desc, priorityStr            string
			seqID                              int
			sortOrder                          float64
			startDate, targetDate              *time.Time
			isDraft                            bool
			createdBy                          uuid.UUID
			updatedBy                          *uuid.UUID
			createdAt, updatedAt               time.Time
		)
		if err := rows.Scan(
			&id, &wsID, &projID, &epicID, &parentID, &stateID,
			&name, &desc, &priorityStr, &seqID, &sortOrder,
			&startDate, &targetDate, &isDraft,
			&createdBy, &updatedBy, &createdAt, &updatedAt,
		); err != nil {
			return n, fmt.Errorf("issue scan: %w", err)
		}
		orgID, ok := wsOrg[wsID]
		if !ok {
			slog.Warn("issue: unknown workspace", "issue_id", id, "workspace_id", wsID)
			continue
		}

		updBy := createdBy
		if updatedBy != nil {
			updBy = *updatedBy
		}

		nv := &node.NodeValue{
			ID:          id,
			OrgID:       orgID,
			WorkspaceID: wsID,
			ProjectID:   projID,
			NodeType:    node.NodeTypeIssue,
			Name:        name,
			Description: desc,
			StateID:     stateID,
			ParentID:    parentID,
			EpicID:      epicID,
			SequenceID:  int32(seqID),
			SortOrder:   sortOrder,
			CreatedBy:   createdBy,
			UpdatedBy:   updBy,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		}

		props := issueProps(wsID, issue.Priority(priorityStr), startDate, targetDate, isDraft)

		if err := b.entities.Set(ctx, nv, props, nil); err != nil {
			return n, fmt.Errorf("issue %s set: %w", id, err)
		}
		n++
		if n%500 == 0 {
			slog.Info("issue progress", "count", n)
		}
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	slog.Info("issues backfilled", "count", n)
	return n, nil
}

// ---- cycles ----

func (b *backfiller) backfillCycles(ctx context.Context, wsOrg map[uuid.UUID]uuid.UUID) (int, error) {
	const q = `
		SELECT id, workspace_id, project_id,
		       name, description, start_date, end_date, sort_order,
		       created_by, updated_by, created_at, updated_at
		FROM cycles
		ORDER BY created_at`

	rows, err := b.pool.Query(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var (
			id, wsID, projID         uuid.UUID
			name, desc               string
			startDate, endDate       *time.Time
			sortOrder                float64
			createdBy                uuid.UUID
			updatedBy                *uuid.UUID
			createdAt, updatedAt     time.Time
		)
		if err := rows.Scan(
			&id, &wsID, &projID,
			&name, &desc, &startDate, &endDate, &sortOrder,
			&createdBy, &updatedBy, &createdAt, &updatedAt,
		); err != nil {
			return n, fmt.Errorf("cycle scan: %w", err)
		}
		orgID, ok := wsOrg[wsID]
		if !ok {
			slog.Warn("cycle: unknown workspace", "cycle_id", id, "workspace_id", wsID)
			continue
		}

		updBy := createdBy
		if updatedBy != nil {
			updBy = *updatedBy
		}

		nv := &node.NodeValue{
			ID:          id,
			OrgID:       orgID,
			WorkspaceID: wsID,
			ProjectID:   projID,
			NodeType:    node.NodeTypeCycle,
			Name:        name,
			Description: desc,
			SortOrder:   sortOrder,
			CreatedBy:   createdBy,
			UpdatedBy:   updBy,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		}

		props := cycleProps(wsID, startDate, endDate)

		if err := b.entities.Set(ctx, nv, props, nil); err != nil {
			return n, fmt.Errorf("cycle %s set: %w", id, err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	slog.Info("cycles backfilled", "count", n)
	return n, nil
}

// ---- modules ----

func (b *backfiller) backfillModules(ctx context.Context, wsOrg map[uuid.UUID]uuid.UUID) (int, error) {
	const q = `
		SELECT id, workspace_id, project_id,
		       name, description, status, start_date, target_date, sort_order,
		       created_by, updated_by, created_at, updated_at
		FROM modules
		ORDER BY created_at`

	rows, err := b.pool.Query(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var (
			id, wsID, projID         uuid.UUID
			name, desc, status       string
			startDate, targetDate    *time.Time
			sortOrder                float64
			createdBy                uuid.UUID
			updatedBy                *uuid.UUID
			createdAt, updatedAt     time.Time
		)
		if err := rows.Scan(
			&id, &wsID, &projID,
			&name, &desc, &status, &startDate, &targetDate, &sortOrder,
			&createdBy, &updatedBy, &createdAt, &updatedAt,
		); err != nil {
			return n, fmt.Errorf("module scan: %w", err)
		}
		orgID, ok := wsOrg[wsID]
		if !ok {
			slog.Warn("module: unknown workspace", "module_id", id, "workspace_id", wsID)
			continue
		}

		updBy := createdBy
		if updatedBy != nil {
			updBy = *updatedBy
		}

		nv := &node.NodeValue{
			ID:          id,
			OrgID:       orgID,
			WorkspaceID: wsID,
			ProjectID:   projID,
			NodeType:    node.NodeTypeModule,
			Name:        name,
			Description: desc,
			SortOrder:   sortOrder,
			CreatedBy:   createdBy,
			UpdatedBy:   updBy,
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		}

		props := moduleProps(wsID, status, startDate, targetDate)

		if err := b.entities.Set(ctx, nv, props, nil); err != nil {
			return n, fmt.Errorf("module %s set: %w", id, err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	slog.Info("modules backfilled", "count", n)
	return n, nil
}

// ---- property helpers (mirror service/property_key.go logic) ----

var tackPropNamespace = uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")

const (
	propNamePriority  = "priority"
	propNameDueDate   = "due_date"
	propNameStartDate = "start_date"
	propNameIsDraft   = "is_draft"
	propNameStatus    = "status"
	propNameEndDate   = "end_date"
)

func systemPropID(workspaceID uuid.UUID, name string) uuid.UUID {
	return uuid.NewSHA1(tackPropNamespace, []byte(workspaceID.String()+":"+name))
}

func priorityRank(p issue.Priority) *int32 {
	ranks := map[issue.Priority]int32{
		issue.PriorityUrgent: 0,
		issue.PriorityHigh:   1,
		issue.PriorityMedium: 2,
		issue.PriorityLow:    3,
		issue.PriorityNone:   4,
	}
	r, ok := ranks[p]
	if !ok {
		r = 4
	}
	return &r
}

func tsProp(t *time.Time) *node.PropertyValue {
	if t == nil {
		return nil
	}
	cp := *t
	return &node.PropertyValue{Kind: node.PropertyValueTimestamp, Timestamp: &cp}
}

func epicProps(wsID uuid.UUID, p issue.Priority, startDate, targetDate *time.Time, isDraft bool) map[uuid.UUID]*node.PropertyValue {
	props := make(map[uuid.UUID]*node.PropertyValue)
	if p != "" {
		key := string(p)
		props[systemPropID(wsID, propNamePriority)] = &node.PropertyValue{
			Kind:     node.PropertyValueEnum,
			Enum:     &key,
			EnumRank: priorityRank(p),
		}
	}
	if v := tsProp(startDate); v != nil {
		props[systemPropID(wsID, propNameStartDate)] = v
	}
	if v := tsProp(targetDate); v != nil {
		props[systemPropID(wsID, propNameDueDate)] = v
	}
	props[systemPropID(wsID, propNameIsDraft)] = &node.PropertyValue{
		Kind: node.PropertyValueBool,
		Bool: &isDraft,
	}
	return props
}

func issueProps(wsID uuid.UUID, p issue.Priority, startDate, targetDate *time.Time, isDraft bool) map[uuid.UUID]*node.PropertyValue {
	// Same fields as epics for issues.
	return epicProps(wsID, p, startDate, targetDate, isDraft)
}

func cycleProps(wsID uuid.UUID, startDate, endDate *time.Time) map[uuid.UUID]*node.PropertyValue {
	props := make(map[uuid.UUID]*node.PropertyValue)
	if v := tsProp(startDate); v != nil {
		props[systemPropID(wsID, propNameStartDate)] = v
	}
	if v := tsProp(endDate); v != nil {
		props[systemPropID(wsID, propNameEndDate)] = v
	}
	return props
}

func moduleProps(wsID uuid.UUID, status string, startDate, targetDate *time.Time) map[uuid.UUID]*node.PropertyValue {
	props := make(map[uuid.UUID]*node.PropertyValue)
	if status != "" {
		props[systemPropID(wsID, propNameStatus)] = &node.PropertyValue{
			Kind: node.PropertyValueText,
			Text: &status,
		}
	}
	if v := tsProp(startDate); v != nil {
		props[systemPropID(wsID, propNameStartDate)] = v
	}
	if v := tsProp(targetDate); v != nil {
		props[systemPropID(wsID, propNameDueDate)] = v
	}
	return props
}
