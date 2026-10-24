//go:build runtimeaudit

package integration_test

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// scalingTables formats the workload table and the storage table for results
// as Markdown with an introduction for each table.
func scalingTables(results []scalingResult) string {
	var tables strings.Builder
	tables.WriteString("\nThe table compares MCP workload and audit recovery measurements across app instance counts.\n\nScaling results:\n")
	tables.WriteString("| Instances | Requests | Ledger events | Throughput (requests/s) | Latency (50th percentile) | Latency (95th percentile) | Latency (99th percentile) | FoundationDB outbox entries | YugabyteDB outbox records | Earlier YugabyteDB outbox records drained | Audit recovery time | Broker records | Broker bytes | Lease takeovers |\n")
	tables.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, result := range results {
		tables.WriteString(result.workloadRow())
	}
	tables.WriteString("\nThe table compares audit storage measurements across app instance counts. YugabyteDB index and total relation bytes are the growth of the audit tables during the run. Bytes per final event and per manifest part divide the total relation bytes of audit.events and audit.event_parts by their row counts. FoundationDB replica bytes multiply the key and value bytes under the run's prefix by the storage replica count.\n\nStorage results:\n")
	tables.WriteString("| Instances | Ledger row bytes | YugabyteDB audit index bytes | YugabyteDB audit total relation bytes | Bytes per final event | Bytes per manifest part | FoundationDB key and value bytes | FoundationDB storage replicas | FoundationDB replica bytes | Pending read receipts | Replica bytes per pending read receipt |\n")
	tables.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, result := range results {
		tables.WriteString(result.storageRow())
	}
	return tables.String()
}

// workloadRow formats the run's workload, backlog, recovery, and broker
// measurements as a Markdown table row. It calculates throughput and latency
// percentiles.
func (result scalingResult) workloadRow() string {
	latencies := slices.Clone(result.latencies)
	slices.Sort(latencies)
	percentile := func(fraction float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[int(fraction*float64(len(latencies)-1))]
	}
	throughput := float64(result.requests) / result.duration.Seconds()
	return fmt.Sprintf("| %s | %d | %d | %.1f | %s | %s | %s | %d | %d | %d | %s | %d | %d | %d |\n",
		result.label, result.requests, result.ledgerEvents, throughput, percentile(0.5), percentile(0.95), percentile(0.99),
		result.backlog, result.yugabyteBacklog, result.drained, result.recovery.Round(time.Millisecond),
		result.brokerRecords, result.brokerBytes, result.takeovers)
}

// storageRow formats the run's YugabyteDB and FoundationDB storage
// measurements as a Markdown table row.
func (result scalingResult) storageRow() string {
	return fmt.Sprintf("| %s | %d | %d | %d | %.0f | %.0f | %d | %d | %d | %d | %.0f |\n",
		result.label, result.databaseBytes, result.ledgerIndexBytes, result.ledgerTotalBytes, result.eventBytes,
		result.partBytes, result.fdbBytes, result.replication, result.replicaBytes, result.receipts, result.receiptBytes)
}
