package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"testing"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
)

// SearchClusterVariable must equal "1" for a test binary to start a
// multi-member cluster. A three-member cluster takes 24 GiB. The fixture
// dials members and the proxy by container name, which Docker resolves only
// for a process on the engines' network. `TACK_SEARCH_CLUSTER=1 make
// test-search` runs these tests in the runner container.
const SearchClusterVariable = "TACK_SEARCH_CLUSTER"

// skipWithoutSearchCluster skips a cluster test unless both
// [SearchIntegrationVariable] and [SearchClusterVariable] are "1". Outside a
// test binary, cmd/testenv starts the cluster on request.
func skipWithoutSearchCluster(t T) {
	t.Helper()
	skipWithoutSearchIntegration(t)
	if testing.Testing() && os.Getenv(SearchClusterVariable) != "1" {
		_, _ = fmt.Fprintf(t.Output(), "testenv: OpenSearch cluster test skipped; set %s=1 to run it\n", SearchClusterVariable)
		t.SkipNow()
	}
}

// clusterState is the part of the cluster state that lists each member's
// node name by node ID and the committed voting configuration.
type clusterState struct {
	Nodes map[string]struct {
		Name string `json:"name"`
	} `json:"nodes"`
	Metadata struct {
		Coordination struct {
			Voters []string `json:"last_committed_config"`
		} `json:"cluster_coordination"`
	} `json:"metadata"`
}

// settled reports whether the cluster lists exactly the members by node name
// and the committed voting configuration has the size that OpenSearch
// chooses for them. With the default cluster.auto_shrink_voting_configuration,
// OpenSearch keeps an odd voter count. An odd member count commits every
// member as a voter. An even member count commits one member fewer.
func (s clusterState) settled(members []string) bool {
	names := make([]string, 0, len(s.Nodes))
	for _, node := range s.Nodes {
		names = append(names, node.Name)
	}
	slices.Sort(names)
	expected := slices.Sorted(slices.Values(members))
	voters := len(members)
	if voters%2 == 0 {
		voters--
	}
	return slices.Equal(names, expected) && len(s.Metadata.Coordination.Voters) == voters
}

// waitForClusterSize polls the cluster state through the proxy until the
// state lists exactly the joined members and the voting configuration settles.
func (c *OpenSearchCluster) waitForClusterSize(ctx context.Context) error {
	pass := c.Fixture.Password
	configuration := search.Config{
		Endpoint: c.Fixture.Endpoint, CA: c.Fixture.CA, Username: c.Fixture.Username,
		Password: pass, RequestTimeout: openSearchProbeTimeout, MaxRetries: 0,
	}
	stateClient, err := opensearch.NewClient(configuration.ClientConfig(nil))
	if err != nil {
		slog.ErrorContext(ctx, "testenv.cluster.client_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create cluster state client for %s: %w", c.name, err)
	}
	defer func() { _ = stateClient.Close() }()
	filter := []string{"nodes.*.name", "metadata.cluster_coordination.last_committed_config"}
	request := opensearchapi.ClusterStateReq{Metrics: []string{"nodes", "metadata"}, Params: opensearchapi.ClusterStateParams{FilterPath: filter}}
	members := len(c.members)
	for {
		var state clusterState
		response, err := opensearch.Do(ctx, stateClient, http.MethodGet, request, &state)
		if err == nil && !response.IsError() && state.settled(c.members) {
			return nil
		}
		if !sleepOrDone(ctx) {
			wrapped := fmt.Errorf("wait for %d members and voters in cluster %s: %w", members, c.name, ctx.Err())
			slog.ErrorContext(ctx, "testenv.cluster.size_failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
}
