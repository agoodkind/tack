package tools

import (
	"context"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/service"

	"goodkind.io/tack/internal/domain/node"
)

// RegisterRelationship registers tack_add_relationship / tack_remove_relationship /
// tack_list_relationships. RelationType is arbitrary; seeds define the
// conventional strings (assigned_to, labeled_with, child_of, etc.).
func RegisterRelationship(s *mcpserver.MCPServer, svc *service.NodeService, rels node.RelationshipRepository, resolver *Resolver) {
	addRemove := schema{
		Fields: []schemaField{
			{Name: "source_id", Type: schemaString},
			{Name: "relation_type", Type: schemaString},
			{Name: "target_id", Type: schemaString},
		},
		Required: []string{"source_id", "relation_type", "target_id"},
	}.toMCP()

	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_add_relationship",
			Description: "Adds a directed relationship between two nodes. relation_type is free-form (e.g. assigned_to, labeled_with, child_of, watches).",
			InputSchema: addRemove,
		},
		addRelationshipHandler(svc, resolver),
	)

	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_remove_relationship",
			Description: "Removes a directed relationship between two nodes.",
			InputSchema: addRemove,
		},
		removeRelationshipHandler(svc, resolver),
	)

	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_list_relationships",
			Description: "Lists relationships where the given node is source (direction=out) or target (direction=in). Optional relation_type filter.",
			InputSchema: schema{
				Fields: []schemaField{
					{Name: "node_id", Type: schemaString},
					{Name: "direction", Type: schemaString, Enum: []string{"out", "in"}},
					{Name: "relation_type", Type: schemaString},
				},
				Required: []string{"node_id"},
			}.toMCP(),
		},
		listRelationshipsHandler(rels, resolver),
	)
}

// relationshipEndpoints is one resolved add or remove request: the source
// node, its org, the relation type, and the target.
type relationshipEndpoints struct {
	orgID    uuid.UUID
	sourceID uuid.UUID
	targetID uuid.UUID
	relType  string
}

// resolveRelationshipEndpoints binds and resolves the shared add/remove
// arguments. A non-nil result is an early return the handler passes back to
// the client unchanged. The source must be a node in one of the caller's
// orgs; the guard before the write reads the mark this resolution set.
func resolveRelationshipEndpoints(ctx context.Context, resolver *Resolver, req mcpmcp.CallToolRequest) (relationshipEndpoints, *mcpmcp.CallToolResult) {
	none := relationshipEndpoints{orgID: uuid.Nil, sourceID: uuid.Nil, targetID: uuid.Nil, relType: ""}
	args, err := bindArgs(req)
	if err != nil {
		return none, recoverableError(err.Error())
	}
	sourceIn, ok := requireString(args, "source_id")
	if !ok {
		return none, recoverableError("source_id is required")
	}
	relType, ok := requireString(args, "relation_type")
	if !ok {
		return none, recoverableError("relation_type is required")
	}
	targetIn, ok := requireString(args, "target_id")
	if !ok {
		return none, recoverableError("target_id is required")
	}
	sourceID, err := resolver.ResolveNodeID(ctx, sourceIn)
	if err != nil {
		return none, classifyError(ctx, err)
	}
	targetID, err := resolver.resolveRelationshipTarget(ctx, targetIn)
	if err != nil {
		return none, classifyError(ctx, err)
	}
	resolve, err := resolver.reader.Resolve(ctx, sourceID)
	if err != nil {
		return none, classifyError(ctx, err)
	}
	stampAuditNodeResolve(ctx, resolve)
	// Refuse before the write, not only in the response.
	if !isAuthorized(ctx) {
		return none, permissionDenied()
	}
	return relationshipEndpoints{orgID: resolve.OrgID, sourceID: sourceID, targetID: targetID, relType: relType}, nil
}

func addRelationshipHandler(svc *service.NodeService, resolver *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}
		endpoints, early := resolveRelationshipEndpoints(ctx, resolver, req)
		if early != nil {
			return early, nil
		}
		if err := svc.AddRelationship(ctx, &node.Relationship{
			OrgID:        endpoints.orgID,
			SourceID:     endpoints.sourceID,
			RelationType: endpoints.relType,
			TargetID:     endpoints.targetID,
			CreatedBy:    userID,
			CreatedAt:    clock.Now().UTC(),
		}); err != nil {
			return classifyError(ctx, err), nil
		}
		rc := newRenderCtxWithTypes(ctx, resolver.reader, nil, resolver.typeIndex)
		return successText(renderRelationshipMutation(rc, "Added relationship", endpoints.sourceID, endpoints.relType, endpoints.targetID), ""), nil
	}
}

func removeRelationshipHandler(svc *service.NodeService, resolver *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		endpoints, early := resolveRelationshipEndpoints(ctx, resolver, req)
		if early != nil {
			return early, nil
		}
		if err := svc.RemoveRelationship(ctx, endpoints.orgID, endpoints.sourceID, endpoints.relType, endpoints.targetID); err != nil {
			return classifyError(ctx, err), nil
		}
		rc := newRenderCtxWithTypes(ctx, resolver.reader, nil, resolver.typeIndex)
		return successText(renderRelationshipMutation(rc, "Removed relationship", endpoints.sourceID, endpoints.relType, endpoints.targetID), ""), nil
	}
}
