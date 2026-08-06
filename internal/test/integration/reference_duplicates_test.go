package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/migrations"
)

func TestReferenceDuplicatesAreRegistered(t *testing.T) {
	operations := ops.List()
	if !hasOperation(operations, "reference.duplicates") {
		t.Fatal("reference.duplicates is not registered")
	}
	if !hasOperation(operations, "repair.sequence_scope_ids") {
		t.Fatal("repair.sequence_scope_ids is not registered")
	}
}

func TestFindDuplicateReferencesReportsLegacyCollision(t *testing.T) {
	env := SetupTestEnv(t)
	registerOpsOrg(t, env)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)

	epic := mustCreateLegacyReference(t, env, "epic", "Epic one", project.ID)
	issue := mustCreateLegacyReference(t, env, "issue", "Issue one", project.ID)

	duplicates, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences: %v", err)
	}
	if len(duplicates) != 1 {
		t.Fatalf("duplicate groups = %d, want 1; groups = %+v", len(duplicates), duplicates)
	}
	group := duplicates[0]
	if group.TemplateName != "reference" || group.Encoded != "FAN-1" {
		t.Fatalf("group = %+v, want reference FAN-1", group)
	}
	if len(group.NodeIDs) != 2 {
		t.Fatalf("group nodes = %d, want 2", len(group.NodeIDs))
	}
	if !containsUUID(group.NodeIDs, epic.ID) || !containsUUID(group.NodeIDs, issue.ID) {
		t.Fatalf("group nodes = %v, want %s and %s", group.NodeIDs, epic.ID, issue.ID)
	}
}

func TestFindDuplicateReferencesReturnsNoGroupsWithoutCollisions(t *testing.T) {
	env := SetupTestEnv(t)
	registerOpsOrg(t, env)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	_ = mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)

	duplicates, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences: %v", err)
	}
	if len(duplicates) != 0 {
		t.Fatalf("duplicate groups = %d, want 0; groups = %+v", len(duplicates), duplicates)
	}
}

func registerOpsOrg(t *testing.T, env *TestEnv) {
	t.Helper()
	migrateAuthSchema(t, env)
	userID := uuid.New()
	if _, err := env.Ops.Pool.Exec(
		env.Ctx,
		"INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)",
		userID,
		"ops-test-"+userID.String()+"@example.test",
		"Ops test",
	); err != nil {
		t.Fatalf("insert ops test user %s: %v", userID, err)
	}
	if _, err := env.Ops.Pool.Exec(
		env.Ctx,
		"INSERT INTO org_members (org_id, user_id) VALUES ($1, $2)",
		env.OrgID,
		userID,
	); err != nil {
		t.Fatalf("insert ops test member for org %s: %v", env.OrgID, err)
	}
}

// authSchemaVersion is the goose version holding users, api_tokens, and
// org_members. Later migrations carry the audit schema, whose role grants the
// test database does not provision, so the test migrates up to auth only.
const authSchemaVersion = 1

var authSchemaOnce sync.Once

// migrateAuthSchema applies the auth migration through the same goose path the
// migrate subcommand uses, so the ops org listing reads the real tables.
func migrateAuthSchema(t *testing.T, env *TestEnv) {
	t.Helper()
	var migrateErr error
	authSchemaOnce.Do(func() {
		goose.SetBaseFS(migrations.FS)
		if err := goose.SetDialect("postgres"); err != nil {
			migrateErr = fmt.Errorf("set goose dialect: %w", err)
			return
		}
		database, err := goose.OpenDBWithDriver("pgx", os.Getenv("DATABASE_URL"))
		if err != nil {
			migrateErr = fmt.Errorf("open migration database: %w", err)
			return
		}
		upErr := goose.UpToContext(env.Ctx, database, ".", authSchemaVersion)
		_ = database.Close()
		if upErr != nil {
			migrateErr = fmt.Errorf("goose up to %d: %w", authSchemaVersion, upErr)
		}
	})
	if migrateErr != nil {
		t.Fatalf("migrate auth schema: %v", migrateErr)
	}
}

func mustCreateLegacyReference(t *testing.T, env *TestEnv, typeKey, name string, projectID uuid.UUID) *node.NodeView {
	t.Helper()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"parent_id": jsonStr(projectID.String()),
		"scope_id":  jsonStr(projectID.String()),
		"sequence":  jsonNumber(1),
	}
	current := &node.Node{
		ID:        uuid.New(),
		OrgID:     env.OrgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        current.ID,
		OrgID:     current.OrgID,
		NodeType:  current.NodeType,
		Name:      current.Name,
		Props:     props,
		CreatedAt: current.CreatedAt,
		UpdatedAt: current.UpdatedAt,
	}
	if err := env.Stores.Nodes.CreateAtomic(
		context.Background(), current, view, nil,
		[]string{"parent_id", "scope_id", "sequence"}, nil, nil,
	); err != nil {
		t.Fatalf("create legacy %s: %v", typeKey, err)
	}
	return view
}

func hasOperation(operations []ops.Operation, name string) bool {
	for _, operation := range operations {
		if operation.Name == name {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsUUID(values []uuid.UUID, want uuid.UUID) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func jsonNumber(value int) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
