package main

import (
	"fmt"

	"goodkind.io/tack/internal/testenv"
)

// startSearchCluster starts an OpenSearch cluster of members behind a
// Traefik proxy. It prints the proxy endpoint and admin login, one line per
// member with its proxy status, and the CA certificate.
func startSearchCluster(step *cliStep, members int) *testenv.OpenSearchCluster {
	cluster := testenv.StartOpenSearchCluster(step, members)
	for range members - 1 {
		cluster.AddMember(step)
	}
	_, _ = fmt.Println(cluster.Fixture.Endpoint, cluster.Fixture.Username, cluster.Fixture.Password)
	printSearchBackends(step, cluster)
	_, _ = fmt.Print(cluster.Fixture.CA)
	return cluster
}

// drillSearchCluster stops each member in turn, prints the proxy status of
// every member while that member is down, and starts the member again.
func drillSearchCluster(step *cliStep, cluster *testenv.OpenSearchCluster) {
	for _, member := range cluster.Members() {
		cluster.StopMember(step, member)
		_, _ = fmt.Println("stopped", member)
		printSearchBackends(step, cluster)
		cluster.StartMember(step, member)
		_, _ = fmt.Println("started", member)
	}
}

// printSearchBackends prints each member's container, proxy backend URL, and
// the health status the proxy reports for it.
func printSearchBackends(step *cliStep, cluster *testenv.OpenSearchCluster) {
	backends := cluster.Backends(step)
	for _, member := range cluster.Members() {
		endpoint := cluster.MemberEndpoint(member)
		_, _ = fmt.Println(member, endpoint, backends[endpoint])
	}
}
