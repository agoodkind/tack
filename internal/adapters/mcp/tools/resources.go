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

// RegisterResources registers the MCP orientation resource.
//
// The resource renders current node types and property definitions. Custom
// types appear from metadata without edits to the template's type list.
//
// The resource registration uses these values:
//
//   - The URI is tack://getting-started.
//   - The MIME type is text/markdown.
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
//
// Resource tests invoke the handler through the registered resource. Calling
// only the recording helper would not detect a handler that omits recording.
func gettingStartedHandler(
	resolver *Resolver,
	nodeTypes []*node.NodeType,
	propertyDefs []*node.PropertyDef,
) func(context.Context, mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
	return func(ctx context.Context, _ mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		// Record the identified caller's resource read before rendering content.
		// TACK-340 added this requirement after the handler ignored its context
		// and did not emit mcp.resource_read.
		if err := recordResourceRead(ctx, gettingStartedURI, resolver); err != nil {
			return nil, err
		}
		return []mcpmcp.ResourceContents{
			mcpmcp.TextResourceContents{URI: gettingStartedURI, MIMEType: "text/markdown", Text: buildGettingStartedText(resolver, nodeTypes, propertyDefs)},
		}, nil
	}
}

// recordResourceRead skips local recording when a read collector exists.
// Otherwise it submits mcp.resource_read to the context recorder before the
// handler renders content. A missing recorder refuses the read. A recorder
// failure only logs a warning; that branch does not satisfy the requirement
// to refuse reads when required audit recording fails.
//
// The helper preserves an existing scope. Without an organization in that
// scope, it uses the reader's sole organization or uuid.Nil for zero or
// several memberships. TACK-477 added that selection after resource reads
// omitted the organization, following the auth-event rule from TACK-461.
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
		Type: audit.ActorUser, ID: uuid.Nil, Email: "", Name: "", SessionID: "",
		IP: "", UserAgent: "", RequestID: telemetry.RequestID(ctx), APITokenLabel: "",
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
			Type: "mcp_resource", NodeType: "", ID: uuid.Nil, Identifier: "", Name: uri,
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
		telemetry.L(ctx).Warn("audit.record_failed",
			slog.String("resource", uri),
			slog.String("err", err.Error()))
	}
	return nil
}

// readerSoleOrg returns the reader's sole organization or uuid.Nil for any
// other membership count. It also returns uuid.Nil when the resolver or
// membership repository is missing, or when the lookup fails. A lookup
// failure logs a warning.
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
