package mcp

import (
	"context"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// collectMetadata gathers the node types and property definitions of every
// org the user belongs to, deduplicated by slug and name, for the per-user
// server build.
func (h *Handler) collectMetadata(ctx context.Context, span trace.Span, orgIDs []uuid.UUID) ([]*node.NodeType, []*node.PropertyDef) {
	log := telemetry.L(ctx)
	var nodeTypes []*node.NodeType
	seen := make(map[string]struct{})
	var propertyDefs []*node.PropertyDef
	seenPropertyDefs := make(map[string]struct{})
	for _, orgID := range orgIDs {
		nts, err := h.nodeTypes.List(ctx, orgID)
		if err != nil {
			span.RecordError(err)
			log.ErrorContext(ctx, "mcp: node type list", "org_id", orgID, "err", err)
			continue
		}
		for _, nt := range nts {
			if _, dup := seen[nt.Slug]; !dup {
				seen[nt.Slug] = struct{}{}
				nodeTypes = append(nodeTypes, nt)
			}
		}
		defs, err := h.propertyDefs.List(ctx, orgID)
		if err != nil {
			span.RecordError(err)
			log.ErrorContext(ctx, "mcp: property def list", "org_id", orgID, "err", err)
			continue
		}
		for _, def := range defs {
			if _, dup := seenPropertyDefs[def.Name]; !dup {
				seenPropertyDefs[def.Name] = struct{}{}
				propertyDefs = append(propertyDefs, def)
			}
		}
	}
	return nodeTypes, propertyDefs
}

func (h *Handler) buildServer(nodeTypes []*node.NodeType, propertyDefs []*node.PropertyDef) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("tack", "0.2.0")

	resolver := tools.NewResolver(h.nodes, h.reader, h.members, nodeTypes)

	tools.RegisterWorkspace(s, h.reader, resolver, nodeTypes)
	tools.RegisterMembers(s, h.members, h.users, resolver)
	tools.RegisterProperty(s, h.propertyDefs, resolver)
	tools.RegisterSearch(s, resolver, h.search)
	tools.RegisterRelationship(s, h.nodeSvc, h.relationships, resolver)

	binding := tools.NodeTypeBinding{
		NodeSvc:      h.nodeSvc,
		Reader:       h.reader,
		PropertyDefs: h.propertyDefs,
		Resolver:     resolver,
		Users:        h.users,
	}
	for _, nt := range nodeTypes {
		tools.RegisterNodeTools(s, nt, binding)
	}
	tools.RegisterReferencePropertyTools(s, binding, nodeTypes, propertyDefs)

	tools.RegisterResources(s, h.reader, resolver, nodeTypes, propertyDefs)
	tools.RegisterPrompts(s, resolver, nodeTypes, propertyDefs)
	return s
}
