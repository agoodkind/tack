package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// collectMetadata reads the node types and property definitions of every
// org the user belongs to, deduplicated by slug and name, for the per-user
// server build. Any failed read returns an error. The handler then answers
// with an explicit error and publishes no server from partial metadata.
func (h *Handler) collectMetadata(ctx context.Context, orgIDs []uuid.UUID) ([]*node.NodeType, []*node.PropertyDef, error) {
	var nodeTypes []*node.NodeType
	seen := make(map[string]struct{})
	var propertyDefs []*node.PropertyDef
	seenPropertyDefs := make(map[string]struct{})
	for _, orgID := range orgIDs {
		nts, err := h.nodeTypes.List(ctx, orgID)
		if err != nil {
			return nil, nil, metadataReadFailure(ctx, "list node types of organization "+orgID.String(), err)
		}
		for _, nt := range nts {
			if _, dup := seen[nt.Slug]; !dup {
				seen[nt.Slug] = struct{}{}
				nodeTypes = append(nodeTypes, nt)
			}
		}
		defs, err := h.propertyDefs.List(ctx, orgID)
		if err != nil {
			return nil, nil, metadataReadFailure(ctx, "list property definitions of organization "+orgID.String(), err)
		}
		for _, def := range defs {
			if _, dup := seenPropertyDefs[def.Name]; !dup {
				seenPropertyDefs[def.Name] = struct{}{}
				propertyDefs = append(propertyDefs, def)
			}
		}
	}
	return nodeTypes, propertyDefs, nil
}

// metadataReadFailure logs one failed metadata read and returns it wrapped.
func metadataReadFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("mcp: %s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "mcp.metadata_read_failed", slog.String("err", wrapped.Error()))
	return wrapped
}

// metadataUnavailable answers a request with an explicit error instead of a
// tool server built from empty metadata. err was already logged.
func metadataUnavailable(w http.ResponseWriter, span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, "metadata_unavailable")
	http.Error(w, `{"error":"metadata unavailable"}`, http.StatusServiceUnavailable)
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
