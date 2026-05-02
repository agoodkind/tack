package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func listHandler(nt *node.NodeType, chain []ScopeLevel, epParam string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}
		epSlug, ok := requireString(args, epParam)
		if !ok {
			return recoverableError(epParam + " is required"), nil
		}
		ws, err := b.Resolver.Workspace(ctx, epSlug)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		q := node.NodeListQuery{OrgID: ws.OrgID, NodeType: nt.TypeKey}

		// Walk the scope chain to the deepest container, then narrow via an
		// indexed property instead of a type-wide view scan.
		var parentID uuid.UUID
		if len(chain) > 0 {
			parent := ws
			for _, level := range chain {
				ident, ok := requireString(args, level.ParamName)
				if !ok {
					return recoverableError(level.ParamName + " is required"), nil
				}
				parent, err = b.Resolver.ResolveScope(ctx, parent, level, ident)
				if err != nil {
					return classifyError(ctx, err), nil
				}
			}
			parentID = parent.ID
		} else {
			parentID = ws.ID
		}
		parentIDRaw, _ := json.Marshal(parentID.String())
		q.ByProperty = &node.PropertyMatch{
			PropName: listScopeProperty(nt, chain),
			Value:    parentIDRaw,
		}
		propFilters, err := normalizeListFilters(ctx, b, nt, ws.OrgID, parentID, args["filters"])
		if err != nil {
			return classifyError(ctx, err), nil
		}
		q.PropFilters = append(q.PropFilters, propFilters...)

		views, err := b.Reader.List(ctx, q)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		plural := nt.PluralSlug
		if plural == "" {
			plural = nt.Slug + "s"
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		return successText(renderList(rc, plural, views), ""), nil
	}
}

func createHandler(nt *node.NodeType, chain []ScopeLevel, epParam string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}
		epSlug, ok := requireString(args, epParam)
		if !ok {
			return recoverableError(epParam + " is required"), nil
		}
		name, ok := requireString(args, "name")
		if !ok {
			return recoverableError("name is required"), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}
		ws, err := b.Resolver.Workspace(ctx, epSlug)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		scopeID := ws.ID
		if len(chain) > 0 {
			parent := ws
			for _, level := range chain {
				ident, ok := requireString(args, level.ParamName)
				if !ok {
					return recoverableError(level.ParamName + " is required"), nil
				}
				parent, err = b.Resolver.ResolveScope(ctx, parent, level, ident)
				if err != nil {
					return classifyError(ctx, err), nil
				}
			}
			scopeID = parent.ID
		}

		// Convert property values to raw JSON. Tolerates payloads sent as a
		// stringified JSON blob; see TACK-161.
		rawProps, err := parseProps(args["properties"])
		if err != nil {
			return recoverableError("invalid properties payload: " + err.Error()), nil
		}

		rawProps, parentID, err := normalizeCreateProps(ctx, b, nt, ws.OrgID, scopeID, rawProps)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		toolName := fmt.Sprintf("tack_create_%s", strings.ToLower(nt.Slug))
		idempotencyKey, idempotencyFingerprint, idempotencySource, err := createIdempotency(ctx, req, createIdempotencyInput{
			ToolName:       toolName,
			NodeTypeKey:    nt.TypeKey,
			EntryPointSlug: epSlug,
			Name:           name,
			ParentID:       parentID,
			ScopeID:        scopeID,
			UserID:         userID,
			Props:          rawProps,
			Args:           args,
		})
		if err != nil {
			return unexpectedError(ctx, err), nil
		}
		result, err := b.NodeSvc.Create(ctx, service.CreateInput{
			ParentID:               parentID,
			ScopeID:                scopeID,
			NodeTypeKey:            nt.TypeKey,
			Name:                   name,
			Props:                  rawProps,
			ActorID:                userID,
			IdempotencyKey:         idempotencyKey,
			IdempotencyFingerprint: idempotencyFingerprint,
			IdempotencySource:      idempotencySource,
		})
		if err != nil {
			return classifyError(ctx, err), nil
		}
		instr := ""
		if result.Existed {
			instr = "This create matched an existing operation. No new write was performed."
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		return successText(renderNode(rc, result.View), instr), nil
	}
}
