package integration

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchClusterProxyEndpoint indexes and searches one node through the
// production adapter and graph behind Traefik. The adapter's connection
// observer must report the proxy as its only endpoint.
func TestSearchClusterProxyEndpoint(t *testing.T) {
	cluster := testenv.StartOpenSearchCluster(t, 1)
	fixture, _ := newClusterQueryFixture(t, cluster)
	requireSearchable(t, fixture, []uuid.UUID{clusterNodeWriter(t, fixture)()})
	proxy, err := url.Parse(cluster.Fixture.Endpoint)
	clusterRequire(t, "parse proxy endpoint", err)
	endpoints, err := fixture.Adapter.Endpoints(t.Context())
	clusterRequire(t, "read adapter endpoints", err)
	if len(endpoints) == 0 {
		t.Fatal("the adapter reported no endpoint")
	}
	for _, endpoint := range endpoints {
		if used, err := url.Parse(endpoint); err != nil || used.Host != proxy.Host {
			t.Fatalf("the adapter used endpoint %q; want only %s", endpoint, proxy.Host)
		}
	}
}

// TestSearchClusterEngineOutage stops the only engine and then commits a
// FoundationDB write. Search fails explicitly, the node's search work fails,
// and its live work stays pending. After the restart, the worker completes
// the pending work and the node becomes searchable.
func TestSearchClusterEngineOutage(t *testing.T) {
	cluster := testenv.StartOpenSearchCluster(t, 1)
	fixture, _ := newClusterQueryFixture(t, cluster)
	write := clusterNodeWriter(t, fixture)
	drainSearchWork(t, fixture.Worker, 500)
	member := cluster.Members()[0]
	cluster.StopMember(t, member)
	nodeID := write()
	if page, err := trySearch(fixture.Harness, clusterPhrase, ""); err == nil {
		t.Fatalf("search during the outage returned %d results instead of an error", len(page.IDs))
	}
	clusterEventually(t, "fail the node's search work during the outage", func() error {
		if _, err := fixture.Worker.RunSlice(t.Context()); err != nil && strings.Contains(err.Error(), nodeID.String()) {
			return nil
		}
		return errors.New("no worker slice failed for the node")
	})
	store := fixture.Stores.SearchWork(clock.Wall{})
	clusterEventually(t, "claim the node's pending live work", func() error {
		work, err := store.Claim(t.Context(), searchdomain.WorkClassLive, "cluster-inspector", time.Minute)
		if err != nil {
			return clusterFailure("claim live work", err)
		}
		clusterRequire(t, "yield inspected work", store.Yield(t.Context(), work))
		if work.NodeID != nodeID {
			return fmt.Errorf("claimed live work of node %s", work.NodeID)
		}
		return nil
	})
	cluster.StartMember(t, member)
	clusterProvision(t, fixture.Adapter)
	requireSearchable(t, fixture, []uuid.UUID{nodeID})
}

// TestSearchClusterScaleOut joins two members behind the proxy, deploys the
// model without node IDs, raises the replica count to one after green
// health, and stops each member in turn. After each stop a new node must
// become searchable, and every earlier node must stay searchable. After each
// restart, the whole cluster must report green health before the next stop.
func TestSearchClusterScaleOut(t *testing.T) {
	cluster := testenv.StartOpenSearchCluster(t, 3)
	cluster.AddMember(t)
	cluster.AddMember(t)
	members := cluster.Members()
	backends := cluster.Backends(t)
	for _, member := range members {
		if backends[cluster.MemberEndpoint(member)] != "UP" {
			t.Fatalf("proxy backends %v lack member %s", backends, member)
		}
	}
	if len(backends) != len(members) {
		t.Fatalf("proxy backends %v, want exactly the %d members", backends, len(members))
	}
	fixture, spec := newClusterQueryFixture(t, cluster)
	requireModelOnEveryMember(t, fixture, spec.Model.ID, len(members))
	awaitGreen := func() {
		clusterEventually(t, "wait for green index health", func() error {
			return clusterFailure("read index health", fixture.Adapter.WaitGreen(t.Context(), fixture.Index, search.DefaultGreenWait))
		})
	}
	awaitGreen()
	spec.Replicas = 1
	clusterRequire(t, "raise the replica count", fixture.Adapter.EnsureIndex(t.Context(), fixture.Index, spec))
	awaitGreen()
	settings, err := fixture.Adapter.IndexSettings(t.Context(), fixture.Index)
	if err != nil || settings.Replicas != 1 {
		t.Fatalf("index settings %+v err %v, want one replica", settings, err)
	}
	write := clusterNodeWriter(t, fixture)
	written := []uuid.UUID{}
	for _, member := range members {
		cluster.StopMember(t, member)
		written = append(written, write())
		requireSearchable(t, fixture, written)
		cluster.StartMember(t, member)
		awaitClusterGreen(t, fixture)
	}
}

// awaitClusterGreen waits until every index reports green health, including
// the ML Commons system indexes. ML Commons creates .plugins-ml-model with
// index.auto_expand_replicas 0-1. The model record has two copies on a
// cluster of two or more members. A restarted member recovers its copy after
// it rejoins. The model record is unavailable when a test stops the member
// with the other copy before that recovery ends.
func awaitClusterGreen(t *testing.T, fixture queryFixture) {
	t.Helper()
	clusterEventually(t, "wait for green cluster health", func() error {
		response, err := fixture.Client.Cluster.Health(t.Context(), &opensearchapi.ClusterHealthReq{
			Params: opensearchapi.ClusterHealthParams{WaitForStatus: "green", Timeout: search.DefaultGreenWait},
		})
		if err != nil {
			return clusterFailure("read cluster health", err)
		}
		if response.TimedOut || response.Status != "green" {
			return fmt.Errorf("cluster health is %s", response.Status)
		}
		return nil
	})
}
