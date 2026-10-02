package integration

import (
	"fmt"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func requireClusterDistribution(t *testing.T, cluster *testenv.OpenSearchCluster, fixture queryFixture) {
	t.Helper()
	before := cluster.SearchRequests(t)
	for range 12 {
		callSearch(t, fixture.Harness, clusterPhrase, "")
	}
	clusterEventually(t, "accept public searches on every proxy backend", func() error {
		after := cluster.SearchRequests(t)
		for _, member := range cluster.Members() {
			endpoint := cluster.MemberEndpoint(member)
			if after[endpoint] <= before[endpoint] {
				return fmt.Errorf("backend %s accepted no public search request", endpoint)
			}
		}
		for _, member := range cluster.Members() {
			endpoint := cluster.MemberEndpoint(member)
			t.Logf("proxy backend %s accepted %d OpenSearch query requests during twelve public calls", endpoint, after[endpoint]-before[endpoint])
		}
		return nil
	})
}
