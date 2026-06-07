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

// RegisterAudit registers audit query, get, and redaction tools. The querier
// routes tack_audit_query reads between ClickHouse and Yugabyte; reader serves
// the single-row get and backs the querier when none is supplied.
func RegisterAudit(s *mcpserver.MCPServer, reader *audit.Reader, querier audit.RowQuerier, redactor *audit.Redactor, resolver *Resolver) {
	if reader == nil {
		return
	}
	if querier == nil {
		querier = reader
	}
	registerAuditQuery(s, querier, resolver)
	registerAuditGet(s, reader)
	if redactor != nil {
		registerAuditRedactor(s, redactor)
	}
}

// registerAuditQuery wires the tack_audit_query tool to the routing querier.
func registerAuditQuery(s *mcpserver.MCPServer, querier audit.RowQuerier, resolver *Resolver) {
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
		auditQueryHandler(querier, resolver),
	)
}

// auditQueryHandler returns the tack_audit_query handler. It resolves the
// workspace to an org, parses the bounded time range, builds the filter, and
// renders the routed query result.
func auditQueryHandler(querier audit.RowQuerier, resolver *Resolver) func(context.Context, mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}
		slug, ok := requireString(args, resolver.EntryPointParamName())
		if !ok {
			return recoverableError(resolver.EntryPointParamName() + " is required"), nil
		}
		oldest, latest, rangeErr := parseAuditRange(args)
		if rangeErr != "" {
			return recoverableError(rangeErr), nil
		}
		ws, err := resolver.Workspace(ctx, slug)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		rows, err := querier.Query(ctx, buildAuditFilter(args, ws.OrgID, oldest, latest))
		if err != nil {
			return classifyError(ctx, err), nil
		}
		return successText(renderAuditRows(rows), ""), nil
	}
}

// parseAuditRange reads and validates the mandatory oldest/latest bounds. It
// returns a non-empty message describing the first problem, which the caller
// renders as a recoverable tool error.
func parseAuditRange(args argMap) (time.Time, time.Time, string) {
	oldestStr, ok := requireString(args, "oldest")
	if !ok {
		return time.Time{}, time.Time{}, "oldest is required as an RFC3339 timestamp"
	}
	latestStr, ok := requireString(args, "latest")
	if !ok {
		return time.Time{}, time.Time{}, "latest is required as an RFC3339 timestamp"
	}
	oldest, err := time.Parse(time.RFC3339, oldestStr)
	if err != nil {
		return time.Time{}, time.Time{}, "oldest must be RFC3339: " + err.Error()
	}
	latest, err := time.Parse(time.RFC3339, latestStr)
	if err != nil {
		return time.Time{}, time.Time{}, "latest must be RFC3339: " + err.Error()
	}
	if !latest.After(oldest) {
		return time.Time{}, time.Time{}, "latest must be after oldest"
	}
	return oldest, latest, ""
}

// buildAuditFilter assembles the query filter from the resolved org, the parsed
// range, and the optional action/actor/entity/correlation arguments.
func buildAuditFilter(args argMap, orgID uuid.UUID, oldest, latest time.Time) audit.QueryFilter {
	filter := audit.QueryFilter{
		OrgID:     orgID,
		Oldest:    oldest,
		Latest:    latest,
		Action:    optionalString(args, "action"),
		ActorID:   uuid.Nil,
		EntityID:  uuid.Nil,
		RequestID: optionalString(args, "request_id"),
		TraceID:   optionalString(args, "trace_id"),
		Limit:     auditLimit(args),
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
	return filter
}

// auditLimit parses the optional limit, clamping to (0, 1000] and defaulting to
// 100 when absent or unparseable.
func auditLimit(args argMap) int {
	const defaultLimit, maxLimit = 100, 1000
	v := optionalString(args, "limit")
	if v == "" {
		return defaultLimit
	}
	var n int
	if _, perr := fmt.Sscanf(v, "%d", &n); perr != nil {
		return defaultLimit
	}
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}
