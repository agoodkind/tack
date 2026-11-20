package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// The default children backfill reads its organizations from org_members.
func addCommandOrgMember(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) {
	t.Helper()
	principal := sqlAuditPrincipal()
	member := createSQLAuditUser(t, pool, principal)
	ctx, _ := sqlAuditContext(t, principal)
	err := postgres.NewOrgMemberRepo(pool).AddMember(
		ctx,
		&org.Member{
			ID:        uuid.Nil,
			OrgID:     orgID,
			UserID:    member.ID,
			Role:      commandMemberRole,
			CreatedAt: time.Time{},
		},
	)
	if err != nil {
		t.Fatalf("adding user %s to organization %s failed: %v", member.ID, orgID, err)
	}
}

func createCommandScope(
	t *testing.T,
	env *integration.TestEnv,
	typeKey string,
	parentID uuid.UUID,
	actor uuid.UUID,
) uuid.UUID {
	t.Helper()
	props := map[string]json.RawMessage{"slug": json.RawMessage(strconv.Quote("changes"))}
	if typeKey == "project" {
		props["identifier"] = json.RawMessage(strconv.Quote("CHG"))
	}

	return createRepairNode(
		t,
		env,
		service.CreateInput{
			ParentID:               parentID,
			ScopeID:                parentID,
			NodeTypeKey:            typeKey,
			Name:                   "Changes",
			Props:                  props,
			Relationships:          nil,
			ActorID:                actor,
			IdempotencyKey:         "",
			IdempotencyFingerprint: "",
			IdempotencySource:      "",
		},
	)
}

func projectStates(t *testing.T, env *integration.TestEnv, projectID uuid.UUID) []*node.Node {
	t.Helper()
	edges, err := env.Stores.Relationships.ListByTarget(
		env.Ctx,
		env.OrgID,
		projectID,
		node.RelChildOf,
	)
	if err != nil {
		t.Fatalf("list children of project %s: %v", projectID, err)
	}

	var states []*node.Node

	for _, edge := range edges {
		current, readErr := env.Stores.Nodes.Get(env.Ctx, env.OrgID, edge.SourceID)
		if readErr != nil || current == nil {
			t.Fatalf("read child %s of project %s: %v", edge.SourceID, projectID, readErr)
		}

		if current.NodeType == "state" {
			states = append(states, current)
		}
	}

	if err != nil || len(states) == 0 {
		t.Fatalf(
			"project %s has %d states (list error %v); the test requires at least one",
			projectID,
			len(states),
			err,
		)
	}

	return states
}
