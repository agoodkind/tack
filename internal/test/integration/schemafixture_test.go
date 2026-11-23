package integration_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

func requireRefusedName(t *testing.T, err error, name string) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalidArgument) ||
		!strings.Contains(err.Error(), strconv.Quote(name)) {
		t.Fatalf("error = %v, want an invalid argument that contains %q", err, name)
	}
}

func createSchemaProject(t *testing.T, env *integration.TestEnv) uuid.UUID {
	t.Helper()
	workspace := createSchemaNode(
		t,
		env,
		env.OrgID,
		env.OrgID,
		"workspace",
		schemaProps("slug", quoted("main")),
	)
	project := createSchemaNode(
		t,
		env,
		workspace.ID,
		workspace.ID,
		"project",
		map[string]json.RawMessage{
			"slug":       quoted("schema"),
			"identifier": quoted("SCHEMA"),
		},
	)

	return project.ID
}

func createSchemaNode(
	t *testing.T,
	env *integration.TestEnv,
	parentID uuid.UUID,
	scopeID uuid.UUID,
	typeKey string,
	props map[string]json.RawMessage,
) *node.NodeView {
	t.Helper()
	created, err := env.NodeSvc.Create(
		env.Ctx,
		schemaCreateInput(parentID, scopeID, typeKey, props),
	)
	if err != nil {
		t.Fatalf("create %s: %v", typeKey, err)
	}

	return created.View
}

func schemaCreateInput(
	parentID uuid.UUID,
	scopeID uuid.UUID,
	typeKey string,
	props map[string]json.RawMessage,
) service.CreateInput {
	return service.CreateInput{
		ParentID:               parentID,
		ScopeID:                scopeID,
		NodeTypeKey:            typeKey,
		Name:                   "Schema " + typeKey,
		Props:                  props,
		Relationships:          nil,
		ActorID:                uuid.New(),
		IdempotencyKey:         "",
		IdempotencyFingerprint: "",
		IdempotencySource:      "",
	}
}

func listByScope(
	t *testing.T,
	env *integration.TestEnv,
	typeKey string,
	scopeID uuid.UUID,
) []*node.Node {
	t.Helper()
	nodes, err := env.Stores.Nodes.ListByProperty(
		env.Ctx,
		env.OrgID,
		typeKey,
		"scope_id",
		quoted(scopeID.String()),
	)
	if err != nil {
		t.Fatalf("list %s nodes by scope %s: %v", typeKey, scopeID, err)
	}

	return nodes
}

func stateByName(
	t *testing.T,
	env *integration.TestEnv,
	projectID uuid.UUID,
	name string,
) uuid.UUID {
	t.Helper()
	states, err := env.Stores.Views.List(
		env.Ctx,
		node.NodeListQuery{
			OrgID:            env.OrgID,
			NodeType:         "state",
			ByTargetRelation: &node.RelationScan{NodeID: projectID, RelationType: node.RelChildOf},
		},
	)
	if err != nil {
		t.Fatalf("list states of project %s: %v", projectID, err)
	}

	for _, state := range states {
		if state.Name == name {
			return state.ID
		}
	}

	t.Fatalf("project %s has no state %q", projectID, name)

	return uuid.Nil
}
