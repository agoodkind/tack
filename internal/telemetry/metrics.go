package telemetry

import "expvar"

// Process-global counters. Exposed at /debug/vars so an external watcher can
// scrape them. expvar is the lightest-weight option; we reach for Prometheus
// when we deploy a second instance and need labelled aggregation.
//
// Naming convention follows the rest of the codebase: dot-separated, lower
// snake. tack_ prefix reserves the namespace inside the global expvar map so
// counters from other packages cannot collide.
var (
	// FDB transaction lifecycle.
	fdbTxTotal    = expvar.NewInt("tack_fdb_tx_total")
	fdbTxErrTotal = expvar.NewInt("tack_fdb_tx_err_total")

	// MCP tool lifecycle. Map keys are tool names (e.g. tack_create_issue).
	mcpToolCalls  = expvar.NewMap("tack_mcp_tool_calls")
	mcpToolErrors = expvar.NewMap("tack_mcp_tool_errors")

	// Resolver miss counters per scope level. Keys are level names like
	// "scope" and "node_id".
	resolverMisses = expvar.NewMap("tack_resolver_miss_total")

	// Audit ledger lifecycle. Keys on records and dropped maps are verbs;
	// the dropped map's secondary dimension (stage) is folded into the key
	// as "verb:stage" so a single expvar.Map suffices.
	auditRecords = expvar.NewMap("tack_audit_records_total")
	auditDropped = expvar.NewMap("tack_audit_dropped_total")
)

// IncFDBTx records one successful FDB transaction.
func IncFDBTx() { fdbTxTotal.Add(1) }

// IncFDBTxErr records one failed FDB transaction. IncFDBTx is not also
// called in the error path, so callers want to mirror this in the same place.
func IncFDBTxErr() { fdbTxErrTotal.Add(1) }

// IncMCPTool bumps the call counter for a named MCP tool.
func IncMCPTool(tool string) { mcpToolCalls.Add(tool, 1) }

// IncMCPToolErr bumps the error counter for a named MCP tool.
func IncMCPToolErr(tool string) { mcpToolErrors.Add(tool, 1) }

// IncResolverMiss bumps the resolver miss counter for a named level.
func IncResolverMiss(level string) { resolverMisses.Add(level, 1) }

// IncAuditRecord bumps the per-verb successful-write counter.
func IncAuditRecord(verb string) { auditRecords.Add(verb, 1) }

// IncAuditDropped bumps the per-(verb,stage) drop counter, where stage names
// the failure point: begin, head_read, hash, insert, head_write, commit.
func IncAuditDropped(verb, stage string) { auditDropped.Add(verb+":"+stage, 1) }
