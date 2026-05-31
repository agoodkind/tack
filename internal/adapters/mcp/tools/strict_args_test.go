package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// TestSchemaToMCPClosesAdditionalProperties pins the wire contract: every
// tool schema produced via schema.toMCP() declares AdditionalProperties:false
// so clients that validate locally see a closed schema.
func TestSchemaToMCPClosesAdditionalProperties(t *testing.T) {
	cases := []struct {
		name string
		s    schema
	}{
		{name: "empty", s: schema{}},
		{name: "populated", s: schema{
			Fields:   []schemaField{{Name: "a", Type: schemaString}, {Name: "b", Type: schemaObject}},
			Required: []string{"a"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.s.toMCP()
			if got.AdditionalProperties != false {
				t.Fatalf("AdditionalProperties = %v, want false", got.AdditionalProperties)
			}
		})
	}
}

// TestUpdateToolSchemaClosesAdditionalProperties confirms a representative
// generated tool inherits the closed-schema contract end-to-end.
func TestUpdateToolSchemaClosesAdditionalProperties(t *testing.T) {
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", Name: "Issue"}
	resolver := &Resolver{entryPointSlug: "workspace"}
	tool := updateTool(issueType, "issue", "workspace_reference", resolver)
	if tool.InputSchema.AdditionalProperties != false {
		t.Fatalf("update tool AdditionalProperties = %v, want false", tool.InputSchema.AdditionalProperties)
	}
}

// TestRejectUnknownArgsAcceptsDeclaredKeys returns nil for arguments whose
// keys are all in the allowlist.
func TestRejectUnknownArgsAcceptsDeclaredKeys(t *testing.T) {
	req := callToolReq("tack_update_issue", map[string]any{
		"node_id":    "OSS-71",
		"name":       "new name",
		"properties": map[string]any{"description": "x"},
	})
	allowed := map[string]struct{}{"node_id": {}, "name": {}, "properties": {}, "workspace_reference": {}}
	if err := rejectUnknownArgs(req, "tack_update_issue", allowed); err != nil {
		t.Fatalf("rejectUnknownArgs returned %v, want nil", err)
	}
}

// TestRejectUnknownArgsAcceptsEmptyArguments returns nil when no arguments
// are present at all.
func TestRejectUnknownArgsAcceptsEmptyArguments(t *testing.T) {
	req := callToolReq("tack_list_workspaces", nil)
	allowed := map[string]struct{}{}
	if err := rejectUnknownArgs(req, "tack_list_workspaces", allowed); err != nil {
		t.Fatalf("rejectUnknownArgs returned %v, want nil", err)
	}
}

// TestRejectUnknownArgsRejectsUndeclaredKey produces an error that names
// the offending key, the tool, and the allowed key list in sorted order.
// This is the regression test for the silent-drop bug: a top-level
// description on tack_update_issue must surface as an error, not pass.
func TestRejectUnknownArgsRejectsUndeclaredKey(t *testing.T) {
	req := callToolReq("tack_update_issue", map[string]any{
		"node_id":     "OSS-71",
		"description": "x",
	})
	allowed := map[string]struct{}{"node_id": {}, "name": {}, "properties": {}, "workspace_reference": {}}
	err := rejectUnknownArgs(req, "tack_update_issue", allowed)
	if err == nil {
		t.Fatal("rejectUnknownArgs returned nil for unknown key")
	}
	msg := err.Error()
	for _, want := range []string{
		`"description"`,
		"tack_update_issue",
		"name, node_id, properties, workspace_reference",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error message %q missing %q", msg, want)
		}
	}
}

// TestRejectUnknownArgsListsAllUnknownKeysSorted reports every unknown
// key in one error, sorted, so callers see the full diagnosis in one round
// trip rather than playing whack-a-mole.
func TestRejectUnknownArgsListsAllUnknownKeysSorted(t *testing.T) {
	req := callToolReq("tack_update_issue", map[string]any{
		"node_id": "OSS-71",
		"zeta":    "x",
		"alpha":   "y",
	})
	allowed := map[string]struct{}{"node_id": {}, "name": {}, "properties": {}}
	err := rejectUnknownArgs(req, "tack_update_issue", allowed)
	if err == nil {
		t.Fatal("rejectUnknownArgs returned nil")
	}
	msg := err.Error()
	want := `"alpha", "zeta"`
	if !strings.Contains(msg, want) {
		t.Fatalf("error message %q missing %q (unknown keys must be sorted)", msg, want)
	}
}

// TestNormalizeUpdatePropsRejectsUnknownProperty pins the nested-level
// check on the update path. The create path already covers this in
// TestNormalizeCreatePropsRejectsUnknownProperty; this is its mirror so
// a typo'd property name on update cannot silently land either.
func TestNormalizeUpdatePropsRejectsUnknownProperty(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue"}
	existing := &node.NodeView{
		ID: issueID, OrgID: orgID, NodeType: "issue",
		Props: map[string]json.RawMessage{"scope_id": mustRaw(t, projectID.String())},
	}
	resolver := &Resolver{typeIndex: map[string]*node.NodeType{"issue": issueType}}
	_, err := normalizeUpdateProps(context.Background(), NodeTypeBinding{
		PropertyDefs: &fakePropertyDefs{defs: []*node.PropertyDef{{Name: "priority", Type: node.PropertyTypeText}}},
		Resolver:     resolver,
	}, issueType, existing, map[string]json.RawMessage{"descrption": mustRaw(t, "typo")})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("normalizeUpdateProps unknown property err = %v, want ErrInvalidArgument", err)
	}
}

func callToolReq(name string, args map[string]any) mcpmcp.CallToolRequest {
	return mcpmcp.CallToolRequest{Params: mcpmcp.CallToolParams{Name: name, Arguments: args}}
}
