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
	"goodkind.io/tack/internal/domain/label"
	"goodkind.io/tack/internal/domain/node"
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
		pool:     pool,
		entities: fdbStores.Entities,
		seeder:   seeder,
		stores:   fdbStores,
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
	stores   *fdbadapter.Stores
}

func (b *backfiller) run(ctx context.Context) error {
	// Step 1: migrate structural entities (org → workspace → project → state → label).
	// These must come first so resolution records exist before entity backfill.
	if err := b.migrateStructural(ctx); err != nil {
		return fmt.Errorf("migrate structural: %w", err)
	}

	wss, err := b.listWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	slog.Info("found workspaces", "count", len(wss))

	// Build workspace -> orgID lookup (still reads from SQL until Step 11).
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

// ---- structural migration (org → workspace → project → state → label) ----

func (b *backfiller) migrateStructural(ctx context.Context) error {
	// Orgs
	orgs, err := b.migrateOrgs(ctx)
	if err != nil {
		return fmt.Errorf("orgs: %w", err)
	}
	slog.Info("orgs migrated", "count", len(orgs))

	// Workspaces
	if err := b.migrateWorkspaces(ctx, orgs); err != nil {
		return fmt.Errorf("workspaces: %w", err)
	}

	// Projects
	if err := b.migrateProjects(ctx); err != nil {
		return fmt.Errorf("projects: %w", err)
	}

	// States
	if err := b.migrateStates(ctx); err != nil {
		return fmt.Errorf("states: %w", err)
	}

	// Labels
	if err := b.migrateLabels(ctx); err != nil {
		return fmt.Errorf("labels: %w", err)
	}

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
		SELECT p.id, p.workspace_id, w.org_id, p.name, p.identifier,
		       COALESCE(p.description,''), p.network, p.default_state_id,
		       p.created_by, p.created_at, p.updated_at
		FROM projects p
		JOIN workspaces w ON w.id = p.workspace_id
		ORDER BY p.created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		p := &project.Project{}
		var orgID uuid.UUID
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &orgID, &p.Name, &p.Identifier,
			&p.Description, &p.Network, &p.DefaultStateID,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return err
		}
		_ = orgID // resolved internally via workspace resolution record
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
		SELECT s.id, s.project_id, s.name, s.group_name, s.color, s.sort_order, s.created_at, s.updated_at
		FROM states s
		ORDER BY s.created_at`)
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
