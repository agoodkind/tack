package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

// authzFixture is one caller in org A facing a workspace and project of org A
// and a foreign project of org B.
type authzFixture struct {
	orgA, orgB         uuid.UUID
	workspaceID        uuid.UUID
	projectA, projectB uuid.UUID
	resolver           *Resolver
	reader             *resolverReader
	server             *mcpserver.MCPServer
	relationships      *fakeRelationships
	projectType        *node.NodeType
	// recorder is the audit sink that ctx attaches to every tool call.
	recorder audit.Recorder
}

func newAuthzFixture(t *testing.T) *authzFixture {
	t.Helper()
	f := &authzFixture{
		orgA:          uuid.New(),
		orgB:          uuid.New(),
		workspaceID:   uuid.New(),
		projectA:      uuid.New(),
		projectB:      uuid.New(),
		resolver:      nil,
		reader:        nil,
		server:        nil,
		relationships: &fakeRelationships{},
		projectType:   nil,
		recorder:      audit.NewMemoryRecorder(),
	}
	workspace := &node.NodeView{
		ID:       f.workspaceID,
		OrgID:    f.orgA,
		NodeType: "workspace",
		Name:     "Main",
		Props:    map[string]json.RawMessage{"slug": mustRaw(t, "main")},
	}
	projectA := &node.NodeView{
		ID:       f.projectA,
		OrgID:    f.orgA,
		NodeType: "project",
		Name:     "Ours",
		Props: map[string]json.RawMessage{
			"identifier": mustRaw(t, "OURS"),
			"parent_id":  mustRaw(t, f.workspaceID.String()),
		},
	}
	projectB := &node.NodeView{
		ID:       f.projectB,
		OrgID:    f.orgB,
		NodeType: "project",
		Name:     "Theirs",
		Props:    map[string]json.RawMessage{"identifier": mustRaw(t, "THEIRS")},
	}
	f.reader = &resolverReader{
		views: map[uuid.UUID]*node.NodeView{
			f.workspaceID: workspace,
			f.projectA:    projectA,
			f.projectB:    projectB,
		},
		workspaces: []*node.NodeView{workspace},
	}
	f.projectType = &node.NodeType{
		TypeKey:      "project",
		Slug:         "project",
		Name:         "Project",
		CanLiveUnder: []string{"workspace"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "identifier",
		},
		AllowedOps: []node.Op{node.OpRead},
	}
	workspaceType := &node.NodeType{
		TypeKey:   "workspace",
		Slug:      "workspace",
		Features:  node.Features{node.FeatureIsEntryPoint},
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "slug"},
	}
	repo := &fakeNodeRepo{
		scopeChildren: map[string][]*node.Node{
			"workspace:slug:\"main\"": {
				{
					ID:       f.workspaceID,
					OrgID:    f.orgA,
					NodeType: "workspace",
					Props:    map[string]json.RawMessage{"slug": mustRaw(t, "main")},
				},
			},
		},
	}
	// The fake relationships store a cross-org edge that a dual-org member
	// could have created. A tool response may include the foreign node's UUID
	// but must not reveal its name or properties.
	f.relationships.edges = []*node.Relationship{
		{
			OrgID:        f.orgA,
			SourceID:     f.projectA,
			RelationType: "watches",
			TargetID:     f.projectB,
			CreatedBy:    uuid.New(),
			CreatedAt:    time.Time{},
		},
	}
	f.resolver = NewResolver(
		repo,
		f.reader,
		&fakeMembers{orgIDs: []uuid.UUID{f.orgA}},
		[]*node.NodeType{workspaceType, f.projectType},
	)
	f.server = mcpserver.NewMCPServer("tack", "0.2.0")
	RegisterProperty(f.server, &fakePropertyDefs{defs: nil}, f.resolver)
	RegisterRelationship(f.server, nil, f.relationships, f.resolver)
	nodes := NodeTypeBinding{
		NodeSvc:      nil,
		Reader:       f.reader,
		PropertyDefs: &fakePropertyDefs{defs: nil},
		Resolver:     f.resolver,
		Users:        nil,
	}
	RegisterSearch(f.server, nodes, SearchBinding{Runner: nil, Cursors: nil})
	RegisterNodeTools(f.server, f.projectType, nodes)

	return f
}
