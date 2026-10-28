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
	// Unavailable counts the unavailable responses that the session checks repeated. A request
	// that still returns the unavailable response at the retry deadline also counts as a
	// continuation error.
	Unavailable          int `json:"unavailable"`
	ContinuationErrors   int `json:"continuation_errors"`
	MissingCorpusIssues  int `json:"missing_corpus_issues"`
	ReplayMismatches     int `json:"replay_mismatches"`
	CrossTenantResults   int `json:"cross_tenant_results"`
	DeletedNodesReturned int `json:"deleted_nodes_returned"`
}

// SearchMixedPending reports deletion checks and whether deletion work remained pending.
type SearchMixedPending struct {
	Checks               int `json:"checks"`
	PendingObserved      int `json:"pending_observed"`
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
}

// SearchMixedSample is one reading of pending search work, the rebuild state, the OpenSearch
// nodes, and the FoundationDB cluster health.
type SearchMixedSample struct {
	Offset            time.Duration
	OldestPendingWork time.Duration
	OldestClass       string
	// WorkClasses lists each class with pending search work and omits classes without it.
	// It is nil when the pending work read fails.
	WorkClasses       []SearchMixedWorkClass
	RebuildState      string
	Nodes             []SearchMixedNodeResources
	Error             string
	// FoundationDB is nil when the status read fails. FoundationDBError is the read failure.
	FoundationDB      *SearchMixedFoundationDB
	FoundationDBError string
}

// SearchMixedWorkClass records one class's pending search work in one sample.
// It reports whether the class's oldest age key has a live work record or is stale.
type SearchMixedWorkClass struct {
	Class string `json:"class"`
	// NonemptyBuckets counts the class's buckets that contain at least one age key.
	NonemptyBuckets int `json:"nonempty_buckets"`
	// OldestHasLiveRecord is true when the class's oldest age key has a live work record.
	// It is false when that key is stale.
	OldestHasLiveRecord bool `json:"oldest_has_live_record"`
}

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
