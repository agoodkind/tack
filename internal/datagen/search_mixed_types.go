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
	ContinuationErrors   int `json:"continuation_errors"`
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
	RefusedAfter              time.Duration
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

// SearchMixedSample is one reading of pending search work, the rebuild state, and the
// OpenSearch nodes.
type SearchMixedSample struct {
	Offset            time.Duration
	OldestPendingWork time.Duration
	OldestClass       string
	RebuildState      string
	Nodes             []SearchMixedNodeResources
	Error             string
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
