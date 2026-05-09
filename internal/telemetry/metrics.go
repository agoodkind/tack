package telemetry

import (
	"expvar"
	"strconv"
)

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

// int64Gauge is a live-reading [expvar.Var] backed by a function that returns
// int64. It satisfies the [expvar.Var] interface via String().
type int64Gauge struct {
	fn func() int64
}

// String returns the current gauge value formatted as a JSON number.
func (g *int64Gauge) String() string { return strconv.FormatInt(g.fn(), 10) }

// float64Gauge is a live-reading [expvar.Var] backed by a function that returns
// float64. It satisfies the [expvar.Var] interface via String().
type float64Gauge struct {
	fn func() float64
}

// String returns the current gauge value formatted as a JSON number.
func (g *float64Gauge) String() string { return strconv.FormatFloat(g.fn(), 'g', -1, 64) }

// walStatsProvider is set once by RegisterWALMetrics. It is nil when no WAL
// is configured (dev mode with AUDIT_WAL_DIR unset).
var walStatsProvider *walMetricsSource

type walMetricsSource struct {
	unflushedSegments      func() int64
	oldestUnflushedAgeSecs func() float64
	lastDrainSuccessUnix   func() int64
	idleRotationsTotal     func() int64
	writeErrorsTotal       func() int64
	diskFreeBytes          func() int64
}

// WALStatsSource carries live-reading closures from audit.WALRecorder.Stats().
// Defined here to avoid an import cycle; the audit package calls
// RegisterWALMetrics with a populated WALStatsSource at startup.
type WALStatsSource struct {
	UnflushedSegments      func() int64
	OldestUnflushedAgeSecs func() float64
	LastDrainSuccessUnix   func() int64
	IdleRotationsTotal     func() int64
	WriteErrorsTotal       func() int64
	DiskFreeBytes          func() int64
}

// RegisterWALMetrics wires WAL backlog gauges and counters into expvar. It
// must be called once, after NewWALRecorder, before the server starts serving
// traffic. Calling it more than once panics (expvar.Publish panics on
// duplicate names).
func RegisterWALMetrics(src WALStatsSource) {
	walStatsProvider = &walMetricsSource{
		unflushedSegments:      src.UnflushedSegments,
		oldestUnflushedAgeSecs: src.OldestUnflushedAgeSecs,
		lastDrainSuccessUnix:   src.LastDrainSuccessUnix,
		idleRotationsTotal:     src.IdleRotationsTotal,
		writeErrorsTotal:       src.WriteErrorsTotal,
		diskFreeBytes:          src.DiskFreeBytes,
	}

	expvar.Publish("audit_wal_backlog_segments", &int64Gauge{fn: walStatsProvider.unflushedSegments})
	expvar.Publish("audit_wal_backlog_oldest_age_seconds", &float64Gauge{fn: walStatsProvider.oldestUnflushedAgeSecs})
	expvar.Publish("audit_wal_last_drain_unix", &int64Gauge{fn: walStatsProvider.lastDrainSuccessUnix})
	expvar.Publish("audit_wal_idle_rotations_total", &int64Gauge{fn: walStatsProvider.idleRotationsTotal})
	expvar.Publish("audit_wal_write_errors_total", &int64Gauge{fn: walStatsProvider.writeErrorsTotal})
	expvar.Publish("audit_wal_disk_free_bytes", &int64Gauge{fn: walStatsProvider.diskFreeBytes})
}

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
