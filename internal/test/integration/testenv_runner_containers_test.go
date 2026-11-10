package integration_test

import (
	"context"
	"testing"

	"github.com/moby/moby/client"
)

// listRunnerContainers returns the names of containers with child's runner
// label. It lists only running containers unless all is true.
func listRunnerContainers(ctx context.Context, t *testing.T, cli *client.Client, child string, all bool) []string {
	t.Helper()
	labeled := client.Filters{}.Add("label", stoppedRunnerLabel+"="+child)
	listed, err := cli.ContainerList(ctx, client.ContainerListOptions{All: all, Filters: labeled})
	if err != nil {
		t.Fatalf("list containers of runner %s: %v", child, err)
	}
	names := make([]string, 0, len(listed.Items))
	for _, item := range listed.Items {
		names = append(names, item.Names...)
	}
	return names
}
