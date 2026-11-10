package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// resolveRelationshipTarget resolves a relationship target. A node target
// must belong to one of the caller's organizations. The resolver accepts a
// UUID that does not identify a node as an opaque target, such as a user ID
// for assigned_to or watches. Users are not nodes. This resolution does not
// read user data.
func (r *Resolver) resolveRelationshipTarget(ctx context.Context, input string) (uuid.UUID, error) {
	id, err := uuid.Parse(input)
	if err != nil {
		return r.ResolveNodeID(ctx, input)
	}

	resolve, err := r.reader.Resolve(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && resolve == nil) {
		return id, nil
	}

	if err != nil {
		slog.ErrorContext(
			ctx,
			"resolver.relationship_target_failed",
			slog.String("input", input),
			slog.String("err", err.Error()),
		)

		return uuid.Nil, fmt.Errorf("resolve relationship target %q: %w", input, err)
	}

	if err := r.requireMembership(ctx, resolve.OrgID, "node", input); err != nil {
		return uuid.Nil, err
	}

	return id, nil
}

func listRelationshipsHandler(
	rels node.RelationshipRepository,
	resolver *Resolver,
) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
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

		stampAuditNodeResolve(ctx, resolve)
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
	}
}
