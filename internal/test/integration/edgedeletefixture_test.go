package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

func createDeleteFixtureProject(
	t *testing.T,
	env *integration.TestEnv,
	actor uuid.UUID,
	identifier string,
) uuid.UUID {
	t.Helper()
	workspace := createDeleteFixtureNode(
		t,
		env,
		actor,
		env.OrgID,
		env.OrgID,
		"workspace",
		"Workspace "+identifier,
		map[string]json.RawMessage{
			"slug": parentEdgeJSON(t, "workspace-"+strings.ToLower(identifier)),
		},
	)

	return createDeleteFixtureNode(
		t,
		env,
		actor,
		workspace,
		workspace,
		"project",
		"Project "+identifier,
		map[string]json.RawMessage{"identifier": parentEdgeJSON(t, identifier)},
	)
}

func createDeleteFixtureNode(
	t *testing.T,
	env *integration.TestEnv,
	actor uuid.UUID,
	parentID uuid.UUID,
	scopeID uuid.UUID,
	typeKey string,
	name string,
	props map[string]json.RawMessage,
) uuid.UUID {
	t.Helper()
	created, err := env.NodeSvc.Create(
		env.Ctx,
		service.CreateInput{
			ParentID:    parentID,
			ScopeID:     scopeID,
			NodeTypeKey: typeKey,
			Name:        name,
			Props:       props,
			ActorID:     actor,
		},
	)
	if err != nil || created == nil || created.View == nil {
		t.Fatalf("create %s %q: %v", typeKey, name, err)
	}

	return created.View.ID
}
