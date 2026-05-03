package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/audit"
)

// RegisterAudit registers audit query, get, and redaction tools.
func RegisterAudit(s *mcpserver.MCPServer, reader *audit.Reader, redactor *audit.Redactor, resolver *Resolver) {
	if reader == nil {
		return
	}
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_audit_query",
			Description: "Query the audit ledger for an org over a bounded RFC3339 input range. Results render as Markdown with human-friendly timestamps.",
			InputSchema: schema{
				Fields: []schemaField{
					{Name: resolver.EntryPointParamName(), Type: schemaString},
					{Name: "oldest", Type: schemaString},
					{Name: "latest", Type: schemaString},
					{Name: "action", Type: schemaString},
					{Name: "actor_id", Type: schemaString},
					{Name: "entity_id", Type: schemaString},
					{Name: "request_id", Type: schemaString},
					{Name: "trace_id", Type: schemaString},
					{Name: "limit", Type: schemaInteger},
				},
				Required: []string{resolver.EntryPointParamName(), "oldest", "latest"},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			slug, ok := requireString(args, resolver.EntryPointParamName())
			if !ok {
				return recoverableError(resolver.EntryPointParamName() + " is required"), nil
			}
			oldestStr, ok := requireString(args, "oldest")
			if !ok {
				return recoverableError("oldest is required as an RFC3339 timestamp"), nil
			}
			latestStr, ok := requireString(args, "latest")
			if !ok {
				return recoverableError("latest is required as an RFC3339 timestamp"), nil
			}
			oldest, err := time.Parse(time.RFC3339, oldestStr)
			if err != nil {
				return recoverableError("oldest must be RFC3339"), nil
			}
			latest, err := time.Parse(time.RFC3339, latestStr)
			if err != nil {
				return recoverableError("latest must be RFC3339"), nil
			}
			if !latest.After(oldest) {
				return recoverableError("latest must be after oldest"), nil
			}
			ws, err := resolver.Workspace(ctx, slug)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			filter := audit.QueryFilter{
				OrgID:     ws.OrgID,
				Oldest:    oldest,
				Latest:    latest,
				Action:    optionalString(args, "action"),
				RequestID: optionalString(args, "request_id"),
				TraceID:   optionalString(args, "trace_id"),
				Limit:     100,
			}
			if v := optionalString(args, "limit"); v != "" {
				var n int
				if _, perr := fmt.Sscanf(v, "%d", &n); perr == nil {
					if n > 1000 {
						n = 1000
					}
					if n > 0 {
						filter.Limit = n
					}
				}
			}
			if v := optionalString(args, "actor_id"); v != "" {
				if id, perr := uuid.Parse(v); perr == nil {
					filter.ActorID = id
				}
			}
			if v := optionalString(args, "entity_id"); v != "" {
				if id, perr := uuid.Parse(v); perr == nil {
					filter.EntityID = id
				}
			}
			rows, err := reader.Query(ctx, filter)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			return successText(renderAuditRows(rows), ""), nil
		},
	)
	registerAuditGet(s, reader)
	if redactor != nil {
		registerAuditRedactor(s, redactor)
	}
}
