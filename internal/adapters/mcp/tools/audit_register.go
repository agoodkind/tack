package tools

import (
	"context"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/audit"
)

func registerAuditGet(s *mcpserver.MCPServer, reader *audit.Reader) {
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_audit_get",
			Description: "Fetch one audit ledger record by event_id as Markdown.",
			InputSchema: schema{
				Fields:   []schemaField{{Name: "event_id", Type: schemaString}},
				Required: []string{"event_id"},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			s, ok := requireString(args, "event_id")
			if !ok {
				return recoverableError("event_id is required"), nil
			}
			id, err := uuid.Parse(s)
			if err != nil {
				return recoverableError("event_id must be a UUID"), nil
			}
			row, err := reader.GetByID(ctx, id)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			return successText(renderAuditRow(*row), ""), nil
		},
	)
}

func registerAuditRedactor(s *mcpserver.MCPServer, redactor *audit.Redactor) {
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_audit_redact_actor",
			Description: "Redacts audit PII payloads for one actor_id while preserving the hash chain.",
			InputSchema: schema{
				Fields:   []schemaField{{Name: "actor_id", Type: schemaString}},
				Required: []string{"actor_id"},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			s, ok := requireString(args, "actor_id")
			if !ok {
				return recoverableError("actor_id is required"), nil
			}
			id, err := uuid.Parse(s)
			if err != nil {
				return recoverableError("actor_id must be a UUID"), nil
			}
			rows, err := redactor.RedactActor(ctx, id)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			return successText(renderAuditRedaction(id, rows), ""), nil
		},
	)
}
