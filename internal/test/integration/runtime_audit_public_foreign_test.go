package integration_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

// foreignProject bootstraps identities with a new seed, which creates another
// organization and workspace, and creates one project there as the first
// actor of that organization. It returns the project ID.
func (server *publicServer) foreignProject(t *testing.T) string {
	t.Helper()
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("datagen.ParseScale failed: %v", err)
	}
	ctx := integration.WithTestOperation(t, t.Context())
	identities, err := datagen.BootstrapIdentities(ctx, server.config, clock.Now().UnixNano(), scale)
	if err != nil {
		t.Fatalf("datagen.BootstrapIdentities failed for the foreign organization: %v", err)
	}
	foreign := identities.Workspaces[0]
	if foreign.OrgID == server.orgID {
		t.Fatalf("the foreign workspace %s uses the caller organization %s", foreign.Slug, server.orgID)
	}
	identifier := "audit-for-" + uuid.NewString()[:8]
	body := rpcBody(t, "tools/call", map[string]any{
		"name": "tack_create_project",
		"arguments": map[string]any{
			"workspace_reference": foreign.Slug, "name": identifier,
			"properties": map[string]any{"identifier": identifier},
		},
	})
	created := server.send(t, http.MethodPost, "/mcp", foreign.Actors[0].Token, newRequestID(t, "foreign"), body)
	return rawID(t, toolText(t, created))
}
