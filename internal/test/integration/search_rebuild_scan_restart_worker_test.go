package integration_test

import (
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
)

// newScanRestartWorker builds a SearchWorker over the stores with nil Writer,
// Documents, and Replacer ports. A copy scheduling step does not use those ports.
func newScanRestartWorker(t *testing.T, stores *fdbadapter.Stores) *service.SearchWorker {
	t.Helper()
	policies := stores.SearchPolicySet()
	rollouts := stores.SearchRollouts(clock.Wall{}, policies)
	settings := config.SearchWorkerSettings{
		PageBytes:        1 << 16,
		MaxPages:         32,
		MaxBytes:         5 << 20,
		SliceBudget:      2 * time.Second,
		OperationTimeout: 10 * time.Second,
		Lease:            scanRestartLease,
		IdleInterval:     50 * time.Millisecond,
		Concurrency:      1,
		ClassWeights: map[string]int{
			"live":    1,
			"access":  1,
			"cleanup": 1,
			"rescan":  1,
			"rollout": 1,
			"rebuild": 1,
			"copy":    1,
		},
		ReplacementPauseLimit:  config.SearchReplacementPauseLimit,
		WriteCompletionHorizon: config.SearchWriteCompletionHorizon,
	}
	worker, err := service.NewSearchWorker(
		service.SearchWorkerPorts{
			Store:     stores.SearchWork(clock.Wall{}),
			Reader:    stores.SearchContent(policies),
			Access:    stores.SearchAccess(policies),
			Writer:    nil,
			Rollouts:  rollouts,
			Sessions:  rollouts,
			Documents: nil,
			Rebuilds:  stores.SearchRebuilds(clock.Wall{}),
			Replacer:  nil,
			Audit:     appruntime.SearchWorkerAudit(stores),
		},
		clock.Wall{},
		settings,
		0,
	)
	if err != nil {
		t.Fatalf("create search worker: %v", err)
	}

	return worker
}
