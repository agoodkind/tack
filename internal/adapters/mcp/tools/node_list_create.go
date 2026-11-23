package tools

import (
	"context"
	"encoding/json"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain/node"
)

func listHandler(nt *node.NodeType, route scopeRoute, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}

		entryPointReference, ok := b.Resolver.entryPointReference(args)
		if !ok {
			return recoverableError(b.Resolver.entryPointRequiredMessage()), nil
		}

		ws, err := b.Resolver.Workspace(ctx, entryPointReference)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		q := node.NodeListQuery{OrgID: ws.OrgID, NodeType: nt.TypeKey}

		target := routeTarget{Scope: ws, Parent: ws, ParentParam: ""}
		if route.hasLevels() {
			resolved, early := resolveRouteScope(ctx, b, ws, route, args)
			if early != nil {
				return early, nil
			}

			target = resolved
		}

		matchProperty, matchID := listMatch(nt, route, target)
		applyListScope(&q, matchProperty, matchID)
		propFilters, err := normalizeListFilters(
			ctx,
			b,
			nt,
			ws.OrgID,
			target.Scope.ID,
			args["filters"],
		)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		q.PropFilters = append(q.PropFilters, propFilters...)
		limit, cursor, err := pageArgs(args)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		q.Limit = limit
		q.Cursor = cursor

		page, err := b.Reader.ListPage(ctx, q)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		plural := nt.PluralSlug
		if plural == "" {
			plural = nt.Slug + "s"
		}

		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		if err := rc.loadAssignees(b.Relationships, page.Views...); err != nil {
			return classifyError(ctx, err), nil
		}

		return successText(renderListPage(rc, plural, page), ""), nil
	}
}

// parseToolProps converts the properties argument to raw JSON, tolerating
// payloads sent as a stringified JSON blob (see TACK-161). A parse failure
// maps to the client-facing error result so the handler returns a nil Go
// error, per the MCP handler contract.
func parseToolProps(raw json.RawMessage) (map[string]json.RawMessage, *mcpmcp.CallToolResult) {
	rawProps, err := parseProps(raw)
	if err != nil {
		return nil, recoverableError("invalid properties payload: " + err.Error())
	}

	return rawProps, nil
}
