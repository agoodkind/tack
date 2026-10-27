package datagen

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
)

const (
	// searchMixedStatusKey is the special key that returns the FoundationDB status document.
	searchMixedStatusKey = "\xff\xff/status/json"
	// searchMixedStatusTimeout is the longest time that one status read can add to a sample.
	searchMixedStatusTimeout = 1500 * time.Millisecond
)

// SearchMixedFoundationDB is one reading of FoundationDB cluster health from the status
// document.
type SearchMixedFoundationDB struct {
	PerformanceLimitedBy                   string  `json:"performance_limited_by"`
	WorstQueueBytesStorageServer           int64   `json:"worst_queue_bytes_storage_server"`
	WorstQueueBytesLogServer               int64   `json:"worst_queue_bytes_log_server"`
	WorstDurabilityLagStorageServerSeconds float64 `json:"worst_durability_lag_storage_server_seconds"`
	TransactionsPerSecondLimit             float64 `json:"transactions_per_second_limit"`
	ReleasedTransactionsPerSecond          float64 `json:"released_transactions_per_second"`
	// TransactionStartSeconds, ReadSeconds, and CommitSeconds are nil when the status document
	// does not include the latency probe.
	TransactionStartSeconds *float64 `json:"transaction_start_seconds"`
	ReadSeconds             *float64 `json:"read_seconds"`
	CommitSeconds           *float64 `json:"commit_seconds"`
	MaxRunLoopBusy          float64  `json:"max_run_loop_busy"`
	MaxDiskBusy             float64  `json:"max_disk_busy"`
}

type searchMixedStatusDocument struct {
	Cluster searchMixedStatusCluster `json:"cluster"`
}

type searchMixedStatusCluster struct {
	QOS          searchMixedStatusQOS                `json:"qos"`
	LatencyProbe searchMixedStatusLatencyProbe       `json:"latency_probe"`
	Processes    map[string]searchMixedStatusProcess `json:"processes"`
}

type searchMixedStatusQOS struct {
	PerformanceLimitedBy            searchMixedStatusLimit `json:"performance_limited_by"`
	WorstQueueBytesStorageServer    int64                  `json:"worst_queue_bytes_storage_server"`
	WorstQueueBytesLogServer        int64                  `json:"worst_queue_bytes_log_server"`
	WorstDurabilityLagStorageServer searchMixedStatusLag   `json:"worst_durability_lag_storage_server"`
	TransactionsPerSecondLimit      float64                `json:"transactions_per_second_limit"`
	ReleasedTransactionsPerSecond   float64                `json:"released_transactions_per_second"`
}

type searchMixedStatusLimit struct {
	Name string `json:"name"`
}

type searchMixedStatusLag struct {
	Seconds float64 `json:"seconds"`
}

type searchMixedStatusLatencyProbe struct {
	TransactionStartSeconds *float64 `json:"transaction_start_seconds"`
	ReadSeconds             *float64 `json:"read_seconds"`
	CommitSeconds           *float64 `json:"commit_seconds"`
}

type searchMixedStatusProcess struct {
	RunLoopBusy float64               `json:"run_loop_busy"`
	Disk        searchMixedStatusDisk `json:"disk"`
}

type searchMixedStatusDisk struct {
	Busy float64 `json:"busy"`
}

// searchMixedFoundationDBStatus reads cluster health from the FoundationDB status document.
type searchMixedFoundationDBStatus struct {
	db fdb.Database
}

// read returns the cluster health values of the current status document. The read fails when
// it takes longer than searchMixedStatusTimeout.
func (s *searchMixedFoundationDBStatus) read(ctx context.Context) (*SearchMixedFoundationDB, error) {
	transaction, err := s.db.CreateTransaction()
	if err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not create a transaction to read the FoundationDB status document", err)
	}
	defer transaction.Cancel()
	if err := transaction.Options().SetTimeout(searchMixedStatusTimeout.Milliseconds()); err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not set the timeout of the FoundationDB status read", err)
	}
	for {
		document, readErr := transaction.Get(fdb.Key(searchMixedStatusKey)).Get()
		if readErr == nil {
			return decodeSearchMixedStatus(ctx, document)
		}
		var databaseError fdb.Error
		if !errors.As(readErr, &databaseError) {
			return nil, loggedError(ctx, "qa datagen search-mixed: the command could not read the FoundationDB status document", readErr)
		}
		if retryErr := transaction.OnError(databaseError).Get(); retryErr != nil {
			return nil, loggedError(ctx, "qa datagen search-mixed: the command could not retry the FoundationDB status read", retryErr)
		}
	}
}

func decodeSearchMixedStatus(ctx context.Context, document []byte) (*SearchMixedFoundationDB, error) {
	var status searchMixedStatusDocument
	if err := json.Unmarshal(document, &status); err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not decode the FoundationDB status document", err)
	}
	qos := status.Cluster.QOS
	probe := status.Cluster.LatencyProbe
	health := &SearchMixedFoundationDB{
		PerformanceLimitedBy:                   qos.PerformanceLimitedBy.Name,
		WorstQueueBytesStorageServer:           qos.WorstQueueBytesStorageServer,
		WorstQueueBytesLogServer:               qos.WorstQueueBytesLogServer,
		WorstDurabilityLagStorageServerSeconds: qos.WorstDurabilityLagStorageServer.Seconds,
		TransactionsPerSecondLimit:             qos.TransactionsPerSecondLimit,
		ReleasedTransactionsPerSecond:          qos.ReleasedTransactionsPerSecond,
		TransactionStartSeconds:                probe.TransactionStartSeconds,
		ReadSeconds:                            probe.ReadSeconds,
		CommitSeconds:                          probe.CommitSeconds,
		MaxRunLoopBusy:                         0,
		MaxDiskBusy:                            0,
	}
	for _, process := range status.Cluster.Processes {
		health.MaxRunLoopBusy = max(health.MaxRunLoopBusy, process.RunLoopBusy)
		health.MaxDiskBusy = max(health.MaxDiskBusy, process.Disk.Busy)
	}
	return health, nil
}
