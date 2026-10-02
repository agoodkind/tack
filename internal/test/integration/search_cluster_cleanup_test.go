package integration

import (
	"context"
	"testing"
	"time"

	"goodkind.io/tack/internal/testenv"
)

// startSearchTestCluster releases each test's cluster before the next test
// starts, while the shared FoundationDB and SQL fixtures continue running.
func startSearchTestCluster(t *testing.T, members int) *testenv.OpenSearchCluster {
	t.Helper()
	cluster := testenv.StartOpenSearchCluster(t, members)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
		defer cancel()
		if err := cluster.Close(ctx); err != nil {
			t.Errorf("release disposable search cluster: %v", err)
		}
	})
	return cluster
}
