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

// Phase 2 Wave 1 audit pipeline metrics. Producer (Kafka), consumer
// (audit-consumer projector), and dual-write parity each get their own
// expvar handles so the operator can drill in without cross-correlating
// every signal in /debug/vars at once.
//
// Histograms are emulated with two expvar.Map instances per metric: one
// recording bucket counts keyed by an upper-bound label ("le_<value>" or
// "le_+Inf"), and one recording the cumulative sum and observation count
// under fixed keys "sum" and "count". The bucket layout is fixed at
// import time so the operator can scrape it directly.
var (
	// audit_kafka_produce_total{result="ok|error"}
	auditKafkaProduceTotal = expvar.NewMap("tack_audit_kafka_produce_total")
	// audit_kafka_produce_latency_ms histogram (buckets + sum/count map).
	auditKafkaProduceLatencyMs    = expvar.NewMap("tack_audit_kafka_produce_latency_ms")
	auditKafkaProduceLatencyStats = expvar.NewMap("tack_audit_kafka_produce_latency_ms_stats")
	// audit_kafka_produce_inflight gauge.
	auditKafkaProduceInflight = expvar.NewInt("tack_audit_kafka_produce_inflight")

	// audit_consumer_lag_messages{topic,partition} gauge map.
	auditConsumerLagMessages = expvar.NewMap("tack_audit_consumer_lag_messages")
	// audit_consumer_processed_total{result="ok|error|skip"}.
	auditConsumerProcessedTotal = expvar.NewMap("tack_audit_consumer_processed_total")
	// audit_consumer_batch_latency_ms histogram (poll-to-commit).
	auditConsumerBatchLatencyMs    = expvar.NewMap("tack_audit_consumer_batch_latency_ms")
	auditConsumerBatchLatencyStats = expvar.NewMap("tack_audit_consumer_batch_latency_ms_stats")
	// audit_consumer_offset_committed{topic,partition} gauge map.
	auditConsumerOffsetCommitted = expvar.NewMap("tack_audit_consumer_offset_committed")

	// audit_dual_write_total{path="primary|secondary",result="ok|error"}.
	auditDualWriteTotal = expvar.NewMap("tack_audit_dual_write_total")
	// audit_dual_write_skew_seconds histogram of |secondary_ack - primary_commit|.
	auditDualWriteSkewSeconds      = expvar.NewMap("tack_audit_dual_write_skew_seconds")
	auditDualWriteSkewSecondsStats = expvar.NewMap("tack_audit_dual_write_skew_seconds_stats")
)

// latencyBucketsMs is the upper-bound layout for millisecond latency
// histograms. The list is intentionally short so /debug/vars stays cheap to
// scrape; it covers the spread an operator cares about during incident
// triage (sub-ms produce up to a 5s timeout).
var latencyBucketsMs = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// skewBucketsSeconds is the upper-bound layout for the dual-write skew
// histogram. Wave 1 expects sub-second skew when both sides are healthy;
// anything above 30s indicates a stuck secondary.
var skewBucketsSeconds = []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60}

// IncAuditKafkaProduce records one produce attempt outcome (result is "ok"
// or "error").
func IncAuditKafkaProduce(result string) { auditKafkaProduceTotal.Add(result, 1) }

// ObserveAuditKafkaProduceLatency records one produce latency sample in
// milliseconds.
func ObserveAuditKafkaProduceLatency(ms float64) {
	observeHistogram(auditKafkaProduceLatencyMs, auditKafkaProduceLatencyStats, ms, latencyBucketsMs)
}

// IncAuditKafkaProduceInflight increments the inflight gauge before a
// produce call.
func IncAuditKafkaProduceInflight() { auditKafkaProduceInflight.Add(1) }

// DecAuditKafkaProduceInflight decrements the inflight gauge after a
// produce call.
func DecAuditKafkaProduceInflight() { auditKafkaProduceInflight.Add(-1) }

// SetAuditConsumerLag publishes the latest lag value for one topic/partition.
func SetAuditConsumerLag(topic string, partition int32, lag int64) {
	key := topic + ":" + strconv.FormatInt(int64(partition), 10)
	auditConsumerLagMessages.Set(key, &expvarInt64{value: lag})
}

// IncAuditConsumerProcessed records one record outcome (result is "ok",
// "error", or "skip").
func IncAuditConsumerProcessed(result string, n int64) {
	auditConsumerProcessedTotal.Add(result, n)
}

// ObserveAuditConsumerBatchLatency records one poll-to-commit batch latency
// sample in milliseconds.
func ObserveAuditConsumerBatchLatency(ms float64) {
	observeHistogram(auditConsumerBatchLatencyMs, auditConsumerBatchLatencyStats, ms, latencyBucketsMs)
}

// SetAuditConsumerOffsetCommitted publishes the most recently committed
// offset for one topic/partition.
func SetAuditConsumerOffsetCommitted(topic string, partition int32, offset int64) {
	key := topic + ":" + strconv.FormatInt(int64(partition), 10)
	auditConsumerOffsetCommitted.Set(key, &expvarInt64{value: offset})
}

// IncAuditDualWrite records one path/result outcome of a DualRecorder write.
func IncAuditDualWrite(path, result string) {
	auditDualWriteTotal.Add(path+":"+result, 1)
}

// ObserveAuditDualWriteSkew records the |secondary - primary| skew in seconds.
func ObserveAuditDualWriteSkew(seconds float64) {
	observeHistogram(auditDualWriteSkewSeconds, auditDualWriteSkewSecondsStats, seconds, skewBucketsSeconds)
}

// expvarInt64 is a settable [expvar.Var] holding one int64. [expvar.Int]
// does the same job but only exposes Add and Set(int64) on the concrete
// type; using the small struct keeps the gauge map's value type consistent
// with the existing int64Gauge shape used by the WAL block above.
type expvarInt64 struct{ value int64 }

func (g *expvarInt64) String() string { return strconv.FormatInt(g.value, 10) }

// observeHistogram records one sample into the bucket+stats map pair.
// Callers pass the same buckets layout every time; the keys "le_<bound>"
// and "le_+Inf" are stable across calls so an external scraper can read
// them as a Prometheus-compatible histogram.
func observeHistogram(buckets, stats *expvar.Map, sample float64, bounds []float64) {
	for _, b := range bounds {
		if sample <= b {
			buckets.Add(histogramBucketKey(b), 1)
		}
	}
	buckets.Add("le_+Inf", 1)
	stats.Add("count", 1)
	addFloat(stats, "sum", sample)
}

// histogramBucketKey formats one upper bound as a stable expvar map key.
// Floats are rendered with the shortest representation that round-trips so
// keys do not flap between runs.
func histogramBucketKey(bound float64) string {
	return "le_" + strconv.FormatFloat(bound, 'f', -1, 64)
}

// addFloat increments a float-valued counter inside an [expvar.Map]. expvar
// only exposes integer Add; this helper finds-or-creates an [expvar.Float]
// under the key and bumps it.
func addFloat(m *expvar.Map, key string, delta float64) {
	v := m.Get(key)
	f, ok := v.(*expvar.Float)
	if !ok {
		f = new(expvar.Float)
		m.Set(key, f)
	}
	f.Add(delta)
}
