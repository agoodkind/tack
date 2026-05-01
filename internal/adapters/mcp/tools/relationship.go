package tools

import (
	"context"
	"time"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
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
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			sourceIn, ok := requireString(args, "source_id")
			if !ok {
				return recoverableError("source_id is required"), nil
			}
			relType, ok := requireString(args, "relation_type")
			if !ok {
				return recoverableError("relation_type is required"), nil
			}
			targetIn, ok := requireString(args, "target_id")
			if !ok {
				return recoverableError("target_id is required"), nil
			}

			userID, err := mustUser(ctx)
			if err != nil {
				return unexpectedError(ctx, err), nil
			}
			sourceID, err := resolver.ResolveNodeID(ctx, sourceIn)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			targetID, err := resolver.ResolveNodeID(ctx, targetIn)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			resolve, err := resolver.reader.Resolve(ctx, sourceID)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			if err := svc.AddRelationship(ctx, &node.Relationship{
				OrgID:        resolve.OrgID,
				SourceID:     sourceID,
				RelationType: relType,
				TargetID:     targetID,
				CreatedBy:    userID,
				CreatedAt:    time.Now().UTC(),
			}); err != nil {
				return classifyError(ctx, err), nil
			}
			return successText("ok", ""), nil
		},
	)

	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_remove_relationship",
			Description: "Removes a directed relationship between two nodes.",
			InputSchema: addRemove,
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			sourceIn, ok := requireString(args, "source_id")
			if !ok {
				return recoverableError("source_id is required"), nil
			}
			relType, ok := requireString(args, "relation_type")
			if !ok {
				return recoverableError("relation_type is required"), nil
			}
			targetIn, ok := requireString(args, "target_id")
			if !ok {
				return recoverableError("target_id is required"), nil
			}

			sourceID, err := resolver.ResolveNodeID(ctx, sourceIn)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			targetID, err := resolver.ResolveNodeID(ctx, targetIn)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			resolve, err := resolver.reader.Resolve(ctx, sourceID)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			if err := svc.RemoveRelationship(ctx, resolve.OrgID, sourceID, relType, targetID); err != nil {
				return classifyError(ctx, err), nil
			}
			return successText("ok", ""), nil
		},
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
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			nodeIn, ok := requireString(args, "node_id")
			if !ok {
				return recoverableError("node_id is required"), nil
			}
			direction := optionalString(args, "direction")
			relType := optionalString(args, "relation_type")

			nodeID, err := resolver.ResolveNodeID(ctx, nodeIn)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			resolve, err := resolver.reader.Resolve(ctx, nodeID)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			var relsOut []*node.Relationship
			if direction == "in" {
				relsOut, err = rels.ListByTarget(ctx, resolve.OrgID, nodeID, relType)
			} else {
				relsOut, err = rels.ListBySource(ctx, resolve.OrgID, nodeID, relType)
			}
			if err != nil {
				return classifyError(ctx, err), nil
			}
			rc := newRenderCtxWithTypes(ctx, resolver.reader, nil, resolver.typeIndex)
			label := "outgoing"
			otherIsTarget := true
			if direction == "in" {
				label = "incoming"
				otherIsTarget = false
			}
			return successText(renderRelationships(rc, label, relsOut, otherIsTarget), ""), nil
		},
	)
}
