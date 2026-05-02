package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/audit"
)

// RegisterAudit registers tack_audit_query, tack_audit_get, and (when a
// Redactor is configured) tack_audit_redact_actor. Reader tools run as the
// audit_reader role; the redactor tool runs as audit_redactor and can only
// UPDATE the three payload/redacted/redacted_at columns on audit.pii.
// Audit reader tools are audited with audit.read. Redaction is audited with
// audit.pii_redacted.
func RegisterAudit(s *mcpserver.MCPServer, reader *audit.Reader, redactor *audit.Redactor, resolver *Resolver) {
	if reader == nil {
		return
	}
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_audit_query",
			Description: "Query the audit ledger for an org over a bounded time range. oldest and latest are required RFC3339 timestamps. Optional action / actor_id / entity_id filters route through covering indexes. Limit caps at 1000.",
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
				return recoverableError("oldest is required (RFC3339 timestamp)"), nil
			}
			latestStr, ok := requireString(args, "latest")
			if !ok {
				return recoverableError("latest is required (RFC3339 timestamp)"), nil
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

	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_audit_get",
			Description: "Fetch one audit ledger record by event_id (UUID).",
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

	if redactor == nil {
		return
	}
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_audit_redact_actor",
			Description: "GDPR Right to Erasure. Sets audit.pii.payload = NULL for every audit row whose actor_id matches. Hash chain stays valid because it was computed over pii_ref, not the payload.",
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
			n, err := redactor.RedactActor(ctx, id)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			return successText(fmt.Sprintf("Redacted %d audit.pii rows for actor %s.", n, id), ""), nil
		},
	)
}

func renderAuditRows(rows []audit.Row) string {
	if len(rows) == 0 {
		return "No audit events match the filter."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d events\n\n", len(rows))
	fmt.Fprintf(&b, "| time | actor | action | entity | request | trace | seq |\n")
	fmt.Fprintf(&b, "|------|-------|--------|--------|---------|-------|-----|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s/%s | %s | %s | %d |\n",
			r.EventTime.UTC().Format(time.RFC3339),
			r.ActorID,
			r.Action,
			r.EntityKind, r.EntityID,
			auditContextValue(r.Context, "request_id"),
			auditContextValue(r.Context, "trace_id"),
			r.Seq,
		)
	}
	return b.String()
}

func auditContextValue(raw []byte, key string) string {
	var context map[string]any
	if err := json.Unmarshal(raw, &context); err != nil {
		return "-"
	}
	value, _ := context[key].(string)
	if value == "" {
		return "-"
	}
	return value
}

func renderAuditRow(r audit.Row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n\n", r.EventID, r.Action)
	fmt.Fprintf(&b, "occurred:   %s\n", r.EventTime.UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "actor:      %s (kind=%d)\n", r.ActorID, r.ActorKind)
	fmt.Fprintf(&b, "entity:     %s/%s\n", r.EntityKind, r.EntityID)
	fmt.Fprintf(&b, "seq:        %d (shard %d)\n", r.Seq, r.Shard)
	if len(r.Context) > 0 {
		fmt.Fprintf(&b, "context:    %s\n", string(r.Context))
	}
	if len(r.Delta) > 0 && string(r.Delta) != "null" {
		fmt.Fprintf(&b, "delta:      %s\n", string(r.Delta))
	}
	if r.IdempotencyKey != "" {
		fmt.Fprintf(&b, "idempotent: %s\n", r.IdempotencyKey)
	}
	return b.String()
}
