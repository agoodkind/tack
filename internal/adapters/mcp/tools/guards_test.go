package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
)

// TestGetRequiresWorkspaceReference pins part 3: the get schema requires the
// entry point reference and the handler refuses a call without one.
func TestGetRequiresWorkspaceReference(t *testing.T) {
	f := newAuthzFixture(t)
	tool := getTool(f.projectType, "project", "workspace_reference", f.resolver)
	if !strings.Contains(strings.Join(tool.InputSchema.Required, ","), "workspace_reference") {
		t.Fatalf("get schema required = %v, want workspace_reference", tool.InputSchema.Required)
	}

	updateSchema := updateTool(f.projectType, "project", "workspace_reference", f.resolver)
	if !strings.Contains(
		strings.Join(updateSchema.InputSchema.Required, ","),
		"workspace_reference",
	) {
		t.Fatalf(
			"update schema required = %v, want workspace_reference",
			updateSchema.InputSchema.Required,
		)
	}

	deleteSchema := deleteTool(f.projectType, "project", "workspace_reference", f.resolver)
	if !strings.Contains(
		strings.Join(deleteSchema.InputSchema.Required, ","),
		"workspace_reference",
	) {
		t.Fatalf(
			"delete schema required = %v, want workspace_reference",
			deleteSchema.InputSchema.Required,
		)
	}

	isError, text := f.callTool(
		t,
		"tack_get_project",
		map[string]string{"node_id": f.projectA.String()},
	)
	if !isError || !strings.Contains(text, "workspace_reference is required") {
		t.Fatalf("get without workspace_reference returned isError=%v:\n%s", isError, text)
	}
}

// TestMutationGuardRefusesWithoutAuthorization pins the pre-write guard's
// primitive: a context that never passed a membership check reads as
// unauthorized, including a context with no authorization slot at all, so a
// mutating handler refuses before its write instead of after it.
func TestMutationGuardRefusesWithoutAuthorization(t *testing.T) {
	if isAuthorized(context.Background()) {
		t.Fatal("a bare context read as authorized")
	}

	ctx := withAuthorization(context.Background())
	if isAuthorized(ctx) {
		t.Fatal("an unmarked context read as authorized")
	}

	markAuthorized(ctx)
	if !isAuthorized(ctx) {
		t.Fatal("a marked context read as unauthorized")
	}
}

// TestMembershipComesFromRequestContextWhenAttached pins that the resolvers
// read the per-request membership set: with the context naming only org B,
// the same caller is refused on an org A node even though the members
// repository would allow it.
func TestMembershipComesFromRequestContextWhenAttached(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := auth.WithOrgMembership(
		auth.WithUser(context.Background(), uuid.New()),
		[]uuid.UUID{f.orgB},
	)
	ctx = withAuthorization(ctx)
	_, err := f.resolver.ResolveNodeID(ctx, f.projectA.String())
	if err == nil {
		t.Fatal("ResolveNodeID served an org outside the request's membership set")
	}

	if isAuthorized(ctx) {
		t.Fatal("refused resolution still marked the call authorized")
	}
}
