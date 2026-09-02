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
	auditSpilled = expvar.NewMap("tack_audit_spilled_total")
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

// IncAuditSpilled bumps the per-verb counter of events the primary recorder
// refused and the outbox took instead. A spilled event is not dropped: it is
// owed to the ledger by the relay, and this counter is what says how many.
func IncAuditSpilled(verb string) { auditSpilled.Add(verb, 1) }

// Audit pipeline metrics. The producer (Kafka) and the consumer
// (audit-consumer projector) each get their own expvar handles so the operator
// can drill in without cross-correlating every signal in /debug/vars at once.
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

	// audit_partition_headroom_weeks gauge: count of future weekly partitions
	// beyond the one covering now(). A stalled partition-manager shows up here
	// before audit.events runs out of partitions.
	auditPartitionHeadroomWeeks = expvar.NewInt("tack_audit_partition_headroom_weeks")
	// audit_partition_maintenance_total{result="ok|error"}.
	auditPartitionMaintenanceTotal = expvar.NewMap("tack_audit_partition_maintenance_total")
)

// latencyBucketsMs is the upper-bound layout for millisecond latency
// histograms. The list is intentionally short so /debug/vars stays cheap to
// scrape; it covers the spread an operator cares about during incident
// triage (sub-ms produce up to a 5s timeout).
var latencyBucketsMs = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

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

// SetAuditPartitionHeadroomWeeks publishes the current count of future weekly
// partitions available beyond now().
func SetAuditPartitionHeadroomWeeks(weeks int64) { auditPartitionHeadroomWeeks.Set(weeks) }

// IncAuditPartitionMaintenance records one maintenance run outcome (result is
// "ok" or "error").
func IncAuditPartitionMaintenance(result string) { auditPartitionMaintenanceTotal.Add(result, 1) }

// expvarInt64 is a settable [expvar.Var] holding one int64. [expvar.Int]
// does the same job but only exposes Add and Set(int64) on the concrete
// type; the small struct keeps the gauge map's value type explicit.
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
