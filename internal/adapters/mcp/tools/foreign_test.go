package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

type fakeRelationships struct {
	edges []*node.Relationship
}

func (*fakeRelationships) Add(context.Context, *node.Relationship) error { panic("Add called") }

func (*fakeRelationships) Remove(context.Context, uuid.UUID, uuid.UUID, string, uuid.UUID) error {
	panic("Remove called")
}

func (f *fakeRelationships) ListBySource(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	string,
) ([]*node.Relationship, error) {
	return f.edges, nil
}

func (f *fakeRelationships) ListByTarget(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	string,
) ([]*node.Relationship, error) {
	return nil, nil
}

// TestUUIDPathRefusesForeignOrg checks that project, property, and
// relationship tools return a not found error for a foreign project UUID.
func TestUUIDPathRefusesForeignOrg(t *testing.T) {
	f := newAuthzFixture(t)

	cases := []struct {
		tool string
		args func(id uuid.UUID) map[string]string
	}{
		{
			tool: "tack_get_project",
			args: func(id uuid.UUID) map[string]string {
				return map[string]string{"node_id": id.String(), "workspace_reference": "main"}
			},
		},
		{
			tool: "tack_get_properties",
			args: func(id uuid.UUID) map[string]string {
				return map[string]string{"node_id": id.String()}
			},
		},
		{
			tool: "tack_list_relationships",
			args: func(id uuid.UUID) map[string]string {
				return map[string]string{"node_id": id.String()}
			},
		},
		{
			tool: "tack_add_relationship",
			args: func(id uuid.UUID) map[string]string {
				return map[string]string{
					"source_id":     id.String(),
					"relation_type": "watches",
					"target_id":     uuid.New().String(),
				}
			},
		},
		{
			tool: "tack_remove_relationship",
			args: func(id uuid.UUID) map[string]string {
				return map[string]string{
					"source_id":     id.String(),
					"relation_type": "watches",
					"target_id":     uuid.New().String(),
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(
			testCase.tool,
			func(t *testing.T) {
				isError, text := f.callTool(t, testCase.tool, testCase.args(f.projectB))
				if !isError || !strings.Contains(text, "not found") {
					t.Fatalf(
						"%s with a foreign UUID returned isError=%v:\n%s",
						testCase.tool,
						isError,
						text,
					)
				}
			},
		)
	}
}

// TestRelationshipListHidesForeignEndpointIdentity pins that a cross-org edge
// renders its foreign endpoint as a bare UUID for a caller who is not a
// member of the endpoint's org.
func TestRelationshipListHidesForeignEndpointIdentity(t *testing.T) {
	f := newAuthzFixture(t)
	isError, text := f.callTool(
		t,
		"tack_list_relationships",
		map[string]string{
			"node_id":   f.projectA.String(),
			"direction": "out",
		},
	)
	if isError {
		t.Fatalf("list relationships failed:\n%s", text)
	}

	if !strings.Contains(text, f.projectB.String()) {
		t.Fatalf("the edge's endpoint UUID is missing:\n%s", text)
	}

	if strings.Contains(text, "Theirs") || strings.Contains(text, "THEIRS") {
		t.Fatalf("the foreign endpoint's identity leaked:\n%s", text)
	}
}
