package integration_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const backfillTransactionTimeout = 5 * time.Second

// TestDefaultChildrenBackfillRefusesUndeclaredProperties stores a node type
// with one default child. The default child sets two properties that no
// definition declares. The test also stores one node of that type without the
// child. The dry run and the executed backfill must each return an error that
// contains the node type, the default child, and both property names in
// sorted order. Neither run may write the child.
func TestDefaultChildrenBackfillRefusesUndeclaredProperties(t *testing.T) {
	env := integration.SetupTestEnv(t)
	userID := uuid.New()
	if _, err := env.Ops.Pool.Exec(env.Ctx, "INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)",
		userID, "ops-test-"+userID.String()+"@example.test", "Ops test"); err != nil {
		t.Fatalf("insert ops test user %s: %v", userID, err)
	}
	if _, err := env.Ops.Pool.Exec(env.Ctx, "INSERT INTO org_members (org_id, user_id) VALUES ($1, $2)",
		env.OrgID, userID); err != nil {
		t.Fatalf("insert ops test member for org %s: %v", env.OrgID, err)
	}

	laterName := opaqueName("b")
	earlierName := opaqueName("a")
	typeKey := opaqueName("n")
	childName := "Refused state"
	parentType := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, Name: typeKey, TypeKey: typeKey,
		Slug: typeKey, PluralSlug: typeKey + "s",
		DefaultChildren: []node.DefaultChild{{
			TypeKey: "state",
			Name:    childName,
			Props: map[string]json.RawMessage{
				laterName:   json.RawMessage(`"undeclared later"`),
				earlierName: json.RawMessage(`"undeclared earlier"`),
			},
		}},
	}
	if err := env.Stores.NodeTypes.Set(env.Ctx, parentType); err != nil {
		t.Fatalf("store node type %s: %v", typeKey, err)
	}
	now := clock.Now().UTC()
	parent := &node.Node{ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, NodeType: typeKey, Name: "Parent", CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: parent.ID, OrgID: env.OrgID, NodeType: typeKey, Name: "Parent", CreatedAt: now, UpdatedAt: now}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, parent, view, nil, nil, nil, nil); err != nil {
		t.Fatalf("store parent node %s: %v", parent.ID, err)
	}

	cfg := &config.Config{
		DatabaseURL: testenv.Ledger(t), FDBClusterFile: testenv.FoundationDB(t),
		FDBTransactionTimeout: backfillTransactionTimeout,
	}
	runs := []struct {
		mode string
		work func(context.Context, *config.Config, string) error
	}{
		{mode: "dry run", work: ops.RunDryRun},
		{mode: "execute", work: ops.Run},
	}
	wantNames := strconv.Quote(earlierName) + ", " + strconv.Quote(laterName)
	for _, run := range runs {
		err := run.work(env.Ctx, cfg, "backfill.default_children")
		if err == nil {
			t.Fatalf("default children backfill %s: got nil, want an undeclared property error", run.mode)
		}
		for _, want := range []string{typeKey, childName, wantNames} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("default children backfill %s: got error %q, want an error containing %q", run.mode, err, want)
			}
		}
	}

	parentIDRaw := json.RawMessage(strconv.Quote(parent.ID.String()))
	children, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "state", "parent_id", parentIDRaw)
	if err != nil {
		t.Fatalf("list the children of node %s: %v", parent.ID, err)
	}
	if len(children) != 0 {
		t.Fatalf("node %s has %d children after the refused backfill, want 0", parent.ID, len(children))
	}
}
