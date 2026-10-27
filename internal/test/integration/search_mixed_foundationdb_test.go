package integration_test

import "testing"

// searchMixedFoundationDBSample is the FoundationDB cluster health of one search-mixed sample.
// A nil field means that the result omits that JSON value or sets it to null.
type searchMixedFoundationDBSample struct {
	PerformanceLimitedBy                   *string  `json:"performance_limited_by"`
	WorstQueueBytesStorageServer           *float64 `json:"worst_queue_bytes_storage_server"`
	WorstQueueBytesLogServer               *float64 `json:"worst_queue_bytes_log_server"`
	WorstDurabilityLagStorageServerSeconds *float64 `json:"worst_durability_lag_storage_server_seconds"`
	TransactionsPerSecondLimit             *float64 `json:"transactions_per_second_limit"`
	ReleasedTransactionsPerSecond          *float64 `json:"released_transactions_per_second"`
	TransactionStartSeconds                *float64 `json:"transaction_start_seconds"`
	ReadSeconds                            *float64 `json:"read_seconds"`
	CommitSeconds                          *float64 `json:"commit_seconds"`
	MaxRunLoopBusy                         *float64 `json:"max_run_loop_busy"`
	MaxDiskBusy                            *float64 `json:"max_disk_busy"`
}

// complete reports whether the sample has a performance limit name and a non-negative value
// for the commit latency and every queue, lag, rate, and busy value. The transaction start
// and read latencies can be null.
func (health *searchMixedFoundationDBSample) complete() bool {
	if health == nil || health.PerformanceLimitedBy == nil || *health.PerformanceLimitedBy == "" {
		return false
	}
	required := []*float64{
		health.WorstQueueBytesStorageServer, health.WorstQueueBytesLogServer,
		health.WorstDurabilityLagStorageServerSeconds, health.TransactionsPerSecondLimit,
		health.ReleasedTransactionsPerSecond, health.CommitSeconds, health.MaxRunLoopBusy, health.MaxDiskBusy,
	}
	for _, value := range required {
		if value == nil || *value < 0 {
			return false
		}
	}
	return true
}

// requireSearchMixedFoundationDB fails the test unless at least one sample reports complete
// FoundationDB cluster health.
func requireSearchMixedFoundationDB(t *testing.T, output []byte, envelope searchMixedOutput) {
	t.Helper()
	for _, sample := range envelope.Result.Samples {
		if sample.FoundationDB.complete() {
			return
		}
	}
	t.Fatalf("qa datagen search-mixed: at least one sample must report the FoundationDB performance limit, a non-negative commit latency, and non-negative queue, lag, rate, and busy values: %s", output)
}
