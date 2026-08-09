package tools

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestReferencePropertySetterRecordsMutatedNode pins the audit carrier for a
// setter because an unstamped setter records a mutation against nobody.
func TestReferencePropertySetterRecordsMutatedNode(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	previousRecorder := currentAuditRecorder()
	SetAuditRecorder(recorder)
	t.Cleanup(func() {
		SetAuditRecorder(previousRecorder)
	})

	orgID := uuid.New()
	workspaceID := uuid.New()
	issueID := uuid.New()
	stateID := uuid.New()
	actorID := uuid.New()
	workspaceType := &node.NodeType{
		TypeKey: "workspace",
		Slug:    "workspace",
		Features: node.Features{
			node.FeatureIsEntryPoint,
			node.FeatureIsScope,
		},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "slug",
		},
	}
	issueType := &node.NodeType{
		TypeKey:      "issue",
		Slug:         "issue",
		CanLiveUnder: []string{"workspace"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "identifier",
		},
	}
	stateType := &node.NodeType{
		TypeKey: "state",
		Slug:    "state",
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "name",
		},
	}
	workspace := &node.NodeView{
		ID: workspaceID, OrgID: orgID, NodeType: "workspace", Name: "Main",
		Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")},
	}
	issue := &node.NodeView{
		ID: issueID, OrgID: orgID, NodeType: "issue", Name: "Audit carrier",
		Props: map[string]json.RawMessage{
			"identifier": mustRaw(t, "TACK-427"),
			"parent_id":  mustRaw(t, workspaceID.String()),
			"scope_id":   mustRaw(t, workspaceID.String()),
		},
	}
	state := &node.NodeView{
		ID: stateID, OrgID: orgID, NodeType: "state", Name: "In Progress",
		Props: map[string]json.RawMessage{"name": mustRaw(t, "In Progress")},
	}
	reader := &resolverReader{
		views: map[uuid.UUID]*node.NodeView{
			workspaceID: workspace,
			issueID:     issue,
			stateID:     state,
		},
		workspaces: []*node.NodeView{workspace},
	}
	repo := &referenceAuditNodeRepo{fakeNodeRepo: &fakeNodeRepo{
		scopeChildren: map[string][]*node.Node{
			"workspace:slug:\"main\"": {{ID: workspaceID, OrgID: orgID, NodeType: "workspace"}},
			"issue:identifier:\"TACK-427\"": {{
				ID: issueID, OrgID: orgID, NodeType: "issue",
				Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())},
			}},
		},
	}}
	nodeTypes := []*node.NodeType{workspaceType, issueType, stateType}
	resolver := NewResolver(repo, reader, &fakeMembers{orgIDs: []uuid.UUID{orgID}}, nodeTypes)
	propertyDefs := &fakePropertyDefs{defs: []*node.PropertyDef{
		{Name: "parent_id"},
		{Name: "scope_id"},
		{
			Name:                   "state_id",
			Type:                   node.PropertyTypeUUID,
			ReferenceTargetTypeKey: "state",
		},
	}}
	nodeService := service.NewNodeService(
		repo,
		reader,
		&createAuditTypes{types: nodeTypes},
		propertyDefs,
		nil,
		nil,
		createAuditSearcher{},
	)
	binding := NodeTypeBinding{
		NodeSvc:      nodeService,
		Reader:       reader,
		PropertyDefs: propertyDefs,
		Resolver:     resolver,
	}
	handler := wrapToolHandler(
		"tack_set_issue_state",
		referencePropertyHandler(issueType, propertyDefs.defs[2], "state", binding),
	)
	result, err := handler(auth.WithUser(t.Context(), actorID), callToolReq("tack_set_issue_state", map[string]any{
		"workspace_reference": "main",
		"issue_reference":     "TACK-427",
		"state":               stateID.String(),
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("setter result: %s", resultText(t, result))
	}
	if repo.updated == nil {
		t.Fatal("reference property setter did not update a node")
	}
	assertNodeAuditEvent(
		t,
		recorder.Events(),
		string(audit.VerbNodeUpdate),
		issueID,
		"issue",
		"TACK-427",
		"Audit carrier",
	)
}
