package tools

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// gettingStartedURI is the one resource this server exposes.
const gettingStartedURI = "tack://getting-started"

// RegisterResources registers MCP resources. Content is generated from the
// current metadata set so that custom types surface automatically without
// editing string literals.
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

// gettingStartedHandler builds the handler AddResource registers.
//
// It is a named function rather than a literal so a test can drive the same
// handler the server serves. A test that called the recording helper directly
// would pass even if the handler stopped calling it, which is the shape that
// let this verb go unemitted in the first place.
func gettingStartedHandler(
	resolver *Resolver,
	nodeTypes []*node.NodeType,
	propertyDefs []*node.PropertyDef,
) func(context.Context, mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
	return func(ctx context.Context, _ mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
		// Reading a resource is a product action by an identified caller, so it
		// is recorded like any other. This handler took the context and ignored
		// it, which is why mcp.resource_read was declared and never emitted
		// (TACK-340).
		recordResourceRead(ctx, gettingStartedURI)
		return []mcpmcp.ResourceContents{
			mcpmcp.TextResourceContents{URI: gettingStartedURI, MIMEType: "text/markdown", Text: buildGettingStartedText(resolver, nodeTypes, propertyDefs)},
		}, nil
	}
}

// recordResourceRead writes the ledger row for one resource read.
//
// A failure to record is logged rather than returned, matching the tool
// wrapper: the read has already happened, and refusing the response afterwards
// would neither unmake it nor record it.
func recordResourceRead(ctx context.Context, uri string) {
	actor := audit.Actor{
		Type: audit.ActorUser, ID: uuid.Nil, Email: "", Name: "", SessionID: "",
		IP: "", UserAgent: "", RequestID: telemetry.RequestID(ctx), APITokenLabel: "",
	}
	if userID, found := auth.UserID(ctx); found {
		actor.ID = userID
	}
	scope := audit.ScopeFromContext(ctx)
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
	if err := currentAuditRecorder().Record(ctx, event); err != nil {
		telemetry.L(ctx).Warn("audit.record_failed",
			slog.String("resource", uri),
			slog.String("err", err.Error()))
	}
}
