package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
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

		// Walk the scope route to the deepest container, then narrow via an
		// indexed property instead of a type-wide view scan.
		target := routeTarget{Scope: ws, Parent: ws, ParentParam: ""}
		if route.hasLevels() {
			resolved, early := resolveRouteScope(ctx, b, ws, route, args)
			if early != nil {
				return early, nil
			}

			target = resolved
		}

		matchProperty, matchID := listMatch(nt, route, target)
		matchIDRaw, _ := json.Marshal(matchID.String())
		q.ByProperty = &node.PropertyMatch{
			PropName: matchProperty,
			Value:    matchIDRaw,
		}
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

func createHandler(
	nt *node.NodeType,
	route scopeRoute,
	b NodeTypeBinding,
) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}

		entryPointReference, ok := b.Resolver.entryPointReference(args)
		if !ok {
			return recoverableError(b.Resolver.entryPointRequiredMessage()), nil
		}

		name, ok := requireString(args, "name")
		if !ok {
			return recoverableError("name is required"), nil
		}

		userID, err := mustUser(ctx)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}

		ws, err := b.Resolver.Workspace(ctx, entryPointReference)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		target, early := resolveCreateTarget(ctx, b, ws, route, args)
		if early != nil {
			return early, nil
		}

		rawProps, propsResult := parseToolProps(args["properties"])
		if propsResult != nil {
			return propsResult, nil
		}

		if refusal := target.refuseParentProperty(rawProps); refusal != nil {
			return refusal, nil
		}

		rawProps, parentID, defs, err := normalizeCreateProps(
			ctx,
			b,
			nt,
			ws.OrgID,
			target.Scope.ID,
			rawProps,
		)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		idempotencyKey, idempotencyFingerprint, idempotencySource, err := createIdempotency(
			ctx,
			req,
			createIdempotencyInput{
				ToolName:       fmt.Sprintf("tack_create_%s", strings.ToLower(nt.Slug)),
				NodeTypeKey:    nt.TypeKey,
				EntryPointSlug: entryPointReference,
				Name:           name,
				ParentID:       target.createParentID(parentID),
				ScopeID:        target.Scope.ID,
				UserID:         userID,
				Props:          rawProps,
				Args:           args,
			},
		)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}

		if !isAuthorized(ctx) {
			return permissionDenied(ctx), nil
		}

		result, err := b.NodeSvc.Create(
			ctx,
			service.CreateInput{
				ParentID:               target.createParentID(parentID),
				ScopeID:                target.Scope.ID,
				NodeTypeKey:            nt.TypeKey,
				Name:                   name,
				Props:                  rawProps,
				ActorID:                userID,
				IdempotencyKey:         idempotencyKey,
				IdempotencyFingerprint: idempotencyFingerprint,
				IdempotencySource:      idempotencySource,
			},
		)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		return renderCreatedNode(ctx, b, result, name, rawProps, defs), nil
	}
}

func renderCreatedNode(
	ctx context.Context,
	b NodeTypeBinding,
	result *service.CreateResult,
	name string,
	rawProps map[string]json.RawMessage,
	defs []*node.PropertyDef,
) *mcpmcp.CallToolResult {
	instr := ""
	if result.Existed {
		instr = "This create matched an existing operation. No new write was performed."
	}

	rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
	audit.SetAuditEntity(
		ctx,
		audit.Entity{
			Type:       "node",
			NodeType:   result.View.NodeType,
			ID:         result.View.ID,
			Identifier: identifierFor(result.View, rc),
			Name:       result.View.Name,
		},
	)
	changes := fieldChanges{Shown: nil, Total: 0}
	if !result.Existed {
		changes = describeFieldChanges(
			rc,
			writeChanges{
				Before:        nil,
				After:         result.View,
				Name:          &name,
				Props:         rawProps,
				ReferenceKeys: referenceKeysOf(defs),
			},
		)
	}

	return successText(renderWriteConfirmation(rc, "Created", result.View, changes), instr)
}
