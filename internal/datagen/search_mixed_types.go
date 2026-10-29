package datagen

import "time"

// SearchMixedBurst reports the ingestion burst that creates issues back to back during the
// search load.
type SearchMixedBurst struct {
	Planned int
	Created int
	Failed  int
	Started time.Duration
	Elapsed time.Duration
}

// SearchMixedSessions reports search sessions that alternate pages between endpoints.
type SearchMixedSessions struct {
	Checks               int `json:"checks"`
	Pages                int `json:"pages"`
	Replays              int `json:"replays"`
	Endpoints            int `json:"endpoints"`
	// Unavailable counts the unavailable responses that the session checks repeated.
	// A first page that stays unavailable after searchConvergenceDeadline also counts as a
	// continuation error.
	Unavailable          int `json:"unavailable"`
	// ExpiredDuringOutage counts continued pages that still returned the unavailable response
	// when the session idle timeout prevented another retry. Session checks do not count
	// these pages as continuation errors.
	ExpiredDuringOutage  int `json:"expired_during_outage"`
	ContinuationErrors   int `json:"continuation_errors"`
	MissingCorpusIssues  int `json:"missing_corpus_issues"`
	ReplayMismatches     int `json:"replay_mismatches"`
	CrossTenantResults   int `json:"cross_tenant_results"`
	DeletedNodesReturned int `json:"deleted_nodes_returned"`
}

// SearchMixedPending reports deletion checks and whether deletion work remained pending.
type SearchMixedPending struct {
	Checks          int `json:"checks"`
	PendingObserved int `json:"pending_observed"`
	// Unavailable counts the unavailable responses that the deletion check repeated, including
	// the responses of a check that failed.
	Unavailable          int `json:"unavailable"`
	DeletedNodesReturned int `json:"deleted_nodes_returned"`
}

// SearchMixedRevocation reports search checks after the run removes a member from an organization.
type SearchMixedRevocation struct {
	Checks                    int
	PagesWithinCacheLifetime  int
	ResultsAfterCacheLifetime int
	// Unavailable is the number of searches that returned the unavailable response.
	Unavailable  int
	RefusedAfter time.Duration
	Refused                   bool
}

// SearchMixedRebuildResult reports the index rebuild that the run starts.
type SearchMixedRebuildResult struct {
	Requested   bool
	TargetIndex string
	Started     time.Duration
	Switched    time.Duration
	FinalState  string
}

// SearchMixedCatchUp reports the wait for search work still pending after writes and phases end.
type SearchMixedCatchUp struct {
	Completed bool
	Elapsed   time.Duration
	// DrainedAfter is the time from the end of writes to the first reading without pending work
	// with an enqueue time at or before the end of writes. It is zero when Completed is false.
	DrainedAfter time.Duration
}

// SearchMixedSample is one reading of pending search work, the rebuild state, the OpenSearch
// nodes, and the FoundationDB cluster health.
type SearchMixedSample struct {
	Offset            time.Duration
	OldestPendingWork time.Duration
	OldestClass       string
	// WorkClasses lists classes that have age keys and omits classes without age keys.
	// It is nil when the pending work read fails.
	WorkClasses       []SearchMixedWorkClass
	RebuildState      string
	Nodes             []SearchMixedNodeResources
	Error             string
	// FoundationDB is nil when the status read fails. FoundationDBError is the read failure.
	FoundationDB      *SearchMixedFoundationDB
	FoundationDBError string
}

// SearchMixedWorkClass reports one class's count of buckets with age keys
// and the work record state for its oldest age key in one sample.
type SearchMixedWorkClass struct {
	Class string `json:"class"`
	// NonemptyBuckets counts the class's buckets that contain at least one age key.
	NonemptyBuckets int `json:"nonempty_buckets"`
	// OldestRecord reports the work record state for the class's oldest age key:
	// live, missing, or superseded.
	OldestRecord SearchMixedOldestRecord `json:"oldest_record"`
}

// SearchMixedOldestRecord is the state of the work record with the same class, bucket,
// organization, and node as an age key.
type SearchMixedOldestRecord string

const (
	// SearchMixedOldestRecordLive means the work record exists and stores the age key's enqueue time.
	SearchMixedOldestRecordLive SearchMixedOldestRecord = "live"
	// SearchMixedOldestRecordMissing means the work record does not exist.
	SearchMixedOldestRecordMissing SearchMixedOldestRecord = "missing"
	// SearchMixedOldestRecordSuperseded means the work record exists
	// and stores a different enqueue time than the age key.
	SearchMixedOldestRecordSuperseded SearchMixedOldestRecord = "superseded"
)

// SearchMixedNodeResources is one OpenSearch node reading from the node statistics API.
type SearchMixedNodeResources struct {
	Name               string `json:"name"`
	CPUPercent         int    `json:"cpu_percent"`
	ProcessCPUPercent  int    `json:"process_cpu_percent"`
	MemoryUsedBytes    int64  `json:"memory_used_bytes"`
	MemoryTotalBytes   int64  `json:"memory_total_bytes"`
	HeapUsedBytes      int64  `json:"heap_used_bytes"`
	HeapMaxBytes       int64  `json:"heap_max_bytes"`
	DiskTotalBytes     int64  `json:"disk_total_bytes"`
	DiskAvailableBytes int64  `json:"disk_available_bytes"`
}
