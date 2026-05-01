package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestIdentifierForUsesAncestorProjectWhenScopeIDMissing(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	epicID := uuid.New()
	issueID := uuid.New()
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Name: "Tack", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "TACK")}}
	epic := &node.NodeView{ID: epicID, OrgID: orgID, NodeType: "epic", Name: "Resolver work", Props: map[string]json.RawMessage{"sequence": mustRaw(t, 12), "parent_id": mustRaw(t, projectID.String())}}
	issue := &node.NodeView{ID: issueID, OrgID: orgID, NodeType: "issue", Name: "Fix round-trip", Props: map[string]json.RawMessage{"sequence": mustRaw(t, 64), "parent_id": mustRaw(t, epicID.String())}}
	reader := &fakeReader{views: map[uuid.UUID]*node.NodeView{projectID: project, epicID: epic, issueID: issue}}
	rc := newRenderCtxWithTypes(context.Background(), reader, nil, map[string]*node.NodeType{
		"project": {TypeKey: "project", Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"}},
		"issue":   {TypeKey: "issue", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}},
	})
	ident := identifierFor(issue, rc)
	if ident != "TACK-64" {
		t.Fatalf("identifierFor(issue): got %q want %q", ident, "TACK-64")
	}
}

func TestIdentifierForRendersScopedStateReference(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	stateID := uuid.New()
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Name: "Clyde", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "CLYDE")}}
	state := &node.NodeView{ID: stateID, OrgID: orgID, NodeType: "state", Name: "In Progress", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, projectID.String())}}
	reader := &fakeReader{views: map[uuid.UUID]*node.NodeView{projectID: project, stateID: state}}
	rc := newRenderCtxWithTypes(context.Background(), reader, nil, map[string]*node.NodeType{
		"project": {TypeKey: "project", Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"}},
		"state":   {TypeKey: "state", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"}},
	})
	ident := identifierFor(state, rc)
	if ident != "CLYDE::In Progress" {
		t.Fatalf("identifierFor(state): got %q want %q", ident, "CLYDE::In Progress")
	}
	body := renderNode(rc, state)
	if !strings.Contains(body, "CLYDE::In Progress") {
		t.Fatalf("renderNode(state) should contain scoped identifier, got:\n%s", body)
	}
}
