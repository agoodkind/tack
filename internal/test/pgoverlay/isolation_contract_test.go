//go:build pgoverlay

package pgoverlay_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func checkDecoyProject(ctx context.Context, name, project string) error {
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		return fmt.Errorf("create Docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	inspected, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{Size: false})
	if err != nil {
		return fmt.Errorf("inspect isolation decoy: %w", err)
	}
	if inspected.Container.Config == nil {
		return fmt.Errorf("isolation decoy does not have container configuration")
	}
	actual := inspected.Container.Config.Labels["com.docker.compose.project"]
	if actual != project {
		return fmt.Errorf("isolation decoy has project %q; expected %q", actual, project)
	}
	return nil
}

func TestIsolationRetainsFirstNetwork(t *testing.T) {
	testenv.PostgreSQL(t)
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatalf("create Docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()
	listed, err := cli.ContainerList(t.Context(), client.ContainerListOptions{})
	if err != nil {
		t.Fatalf("list test containers: %v", err)
	}
	project := os.Getenv(projectVariable)
	network := os.Getenv(networkVariable)
	for _, item := range listed.Items {
		if item.Labels["com.docker.compose.project"] != project ||
			item.Labels["com.docker.compose.service"] != "testenv-engine" {
			continue
		}
		inspected, err := cli.ContainerInspect(
			t.Context(),
			item.ID,
			client.ContainerInspectOptions{Size: false},
		)
		if err != nil {
			t.Fatalf("inspect test engine: %v", err)
		}
		if inspected.Container.NetworkSettings != nil {
			if _, attached := inspected.Container.NetworkSettings.Networks[network]; attached {
				return
			}
		}
	}
	t.Fatalf("the first project %s does not have a test engine on network %s", project, network)
}
