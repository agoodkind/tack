package tools

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// gettingStartedURI is the one resource this server exposes.
const gettingStartedURI = "tack://getting-started"

// RegisterResources registers MCP resources and generates their content from
// current metadata, including custom types.
func RegisterResources(
	s *mcpserver.MCPServer,
	reader node.NodeReader,
	resolver *Resolver,
	nodeTypes []*node.NodeType,
	propertyDefs []*node.PropertyDef,
) {
	s.AddResource(
		mcpmcp.Resource{
			URI:         gettingStartedURI,
			Name:        "tack-getting-started",
			Description: "Orientation guide. Read once per session before calling any other Tack tool.",
			MIMEType:    "text/markdown",
		},
		gettingStartedHandler(resolver, nodeTypes, propertyDefs),
	)
}

// gettingStartedHandler builds the handler that AddResource registers.
// Tests invoke this handler to verify that resource reads record audit events.
func gettingStartedHandler(
	resolver *Resolver,
	nodeTypes []*node.NodeType,
	propertyDefs []*node.PropertyDef,
) func(context.Context, mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
	return func(ctx context.Context, _ mcpmcp.ReadResourceRequest) (
		[]mcpmcp.ResourceContents,
		error,
	) {
		// Record the caller's resource read before returning content (TACK-340).
		if err := recordResourceRead(ctx, gettingStartedURI, resolver); err != nil {
			return nil, err
		}

		return []mcpmcp.ResourceContents{
			mcpmcp.TextResourceContents{
				URI:      gettingStartedURI,
				MIMEType: "text/markdown",
				Text:     buildGettingStartedText(resolver, nodeTypes, propertyDefs),
			},
		}, nil
	}
}

// recordResourceRead defers evidence to an existing read collector, or writes
// an event directly. A missing recorder returns an error. Recorder failures
// produce warnings without returning errors.
// Resource reads do not resolve a workspace. Without an existing audit scope,
// the event uses the caller's sole org when membership identifies exactly one
// (TACK-477, TACK-461). Otherwise the org remains nil.
func recordResourceRead(ctx context.Context, uri string, resolver *Resolver) error {
	if audit.HasReadCollector(ctx) {
		return nil
	}

	recorder, err := requireAuditRecorder(ctx)
	if err != nil {
		logRecorderMissing(ctx, "resource:"+uri)
		return err
	}

	actor := audit.Actor{
		Type:          audit.ActorUser,
		ID:            uuid.Nil,
		Email:         "",
		Name:          "",
		SessionID:     "",
		IP:            "",
		UserAgent:     "",
		RequestID:     telemetry.RequestID(ctx),
		APITokenLabel: "",
	}
	if userID, found := auth.UserID(ctx); found {
		actor.ID = userID
	}

	scope := audit.ScopeFromContext(ctx)
	if scope.OrgID == uuid.Nil {
		scope.OrgID = readerSoleOrg(ctx, resolver)
	}

	event := audit.Event{
		EventID: uuid.Nil,
		Verb:    string(audit.VerbMCPResourceRead),
		Actor:   actor,
		Entity: audit.Entity{
			Type:       "mcp_resource",
			NodeType:   "",
			ID:         uuid.Nil,
			Identifier: "",
			Name:       uri,
		},
		Context: audit.EventContext{
			Source:      audit.SourceMCP,
			Tool:        "",
			RPC:         "",
			Reason:      "",
			OrgID:       scope.OrgID,
			WorkspaceID: scope.WorkspaceID,
			ScopeID:     scope.ScopeID,
			ParentID:    scope.ParentID,
			RequestID:   telemetry.RequestID(ctx),
			TraceID:     telemetry.TraceID(ctx),
		},
		Delta:          nil,
		Outcome:        audit.OutcomeOK,
		Error:          nil,
		IdempotencyKey: "",
		OccurredAt:     time.Time{},
		Extra:          nil,
	}
	if err := recorder.Record(ctx, event); err != nil {
		telemetry.L(ctx).Warn(
			"audit.record_failed",
			slog.String("resource", uri),
			slog.String("err", err.Error()),
		)
	}

	return nil
}

// readerSoleOrg returns the caller's org when membership identifies exactly
// one. Missing dependencies, lookup errors, and other membership counts return nil.
func readerSoleOrg(ctx context.Context, resolver *Resolver) uuid.UUID {
	if resolver == nil || resolver.members == nil {
		return uuid.Nil
	}

	orgIDs, err := resolver.callerOrgIDs(ctx)
	if err != nil {
		telemetry.L(ctx).Warn("audit.resource_org_stamp_failed", slog.String("err", err.Error()))
		return uuid.Nil
	}

	if len(orgIDs) == 1 {
		return orgIDs[0]
	}

	return uuid.Nil
}
