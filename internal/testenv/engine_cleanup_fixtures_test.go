//go:build integration

package testenv_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"slices"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

const diagnosticRunLabel = "io.goodkind.tack.testenv.run"

// diagnosticBroker resolves a public bootstrap to one address holder and one
// broker with that exact network namespace and matching owner labels.
func diagnosticBroker(t *testing.T, cli *client.Client, bootstrap string) container.Summary {
	t.Helper()
	address, _, err := net.SplitHostPort(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := testenv.RunnerContainer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	filters := client.Filters{}.Add("label", "io.goodkind.tack.testenv").
		Add(
			"label",
			"io.goodkind.tack.testenv.host="+host,
		).
		Add(
			"label",
			"io.goodkind.tack.testenv.runner="+runner,
		)
	listed, err := cli.ContainerList(
		t.Context(),
		client.ContainerListOptions{All: true, Filters: filters},
	)
	if err != nil {
		t.Fatal(err)
	}
	var holders []container.Summary
	for _, item := range listed.Items {
		if !slices.ContainsFunc(
			item.Names,
			func(name string) bool {
				return strings.HasPrefix(name, "/tack-testenv-kafka-address-")
			},
		) {
			continue
		}
		inspection, err := cli.ContainerInspect(
			t.Context(),
			item.ID,
			client.ContainerInspectOptions{Size: false},
		)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.Container.NetworkSettings == nil {
			t.Fatalf("Docker omitted the address holder network settings for %s", item.ID)
		}
		for _, endpoint := range inspection.Container.NetworkSettings.Networks {
			if endpoint.IPAddress.String() == address {
				holders = append(holders, item)
				break
			}
		}
	}
	if len(holders) != 1 || holders[0].Labels[diagnosticRunLabel] == "" {
		t.Fatalf(
			"bootstrap %s matched %d address holders; require one identified owner",
			bootstrap,
			len(holders),
		)
	}
	holder := holders[0]
	var brokers []container.Summary
	for _, item := range listed.Items {
		if item.ID == holder.ID ||
			item.Labels[diagnosticRunLabel] != holder.Labels[diagnosticRunLabel] {
			continue
		}
		inspection, err := cli.ContainerInspect(
			t.Context(),
			item.ID,
			client.ContainerInspectOptions{Size: false},
		)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.Container.HostConfig == nil ||
			!inspection.Container.HostConfig.NetworkMode.IsContainer() {
			continue
		}
		shared, err := cli.ContainerInspect(
			t.Context(),
			inspection.Container.HostConfig.NetworkMode.ConnectedContainer(),
			client.ContainerInspectOptions{Size: false},
		)
		if err != nil {
			t.Fatal(err)
		}
		if shared.Container.ID == holder.ID {
			brokers = append(brokers, item)
		}
	}
	if len(brokers) != 1 {
		t.Fatalf(
			"bootstrap %s matched %d broker namespaces; require exactly one",
			bootstrap,
			len(brokers),
		)
	}
	return brokers[0]
}

func diagnosticHelper(
	t *testing.T,
	cli *client.Client,
	image string,
	labels map[string]string,
	entrypoint []string,
	command []string,
) string {
	t.Helper()
	created, err := cli.ContainerCreate(
		t.Context(),
		client.ContainerCreateOptions{
			Config: &container.Config{
				Image:      image,
				Entrypoint: entrypoint,
				Cmd:        command,
				Labels:     labels,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := cli.ContainerRemove(
			context.Background(),
			created.ID,
			client.ContainerRemoveOptions{Force: true, RemoveVolumes: true},
		)
		if err != nil && !cerrdefs.IsNotFound(err) {
			t.Error(err)
		}
	})
	if _, err := cli.ContainerStart(
		t.Context(),
		created.ID,
		client.ContainerStartOptions{},
	); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func waitForDiagnosticExit(t *testing.T, cli *client.Client, identifier string) {
	t.Helper()
	waiting := cli.ContainerWait(
		t.Context(),
		identifier,
		client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning},
	)
	select {
	case <-waiting.Result:
	case err := <-waiting.Error:
		t.Fatal(err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

func readCleanupDiagnostics(t *testing.T, output []byte) map[string]string {
	t.Helper()
	diagnostics := make(map[string]string)
	lines := bufio.NewScanner(bytes.NewReader(output))
	lines.Buffer(make([]byte, 1024), 128<<10)
	for lines.Scan() {
		var record struct {
			Message     string `json:"msg"`
			Container   string `json:"container"`
			Diagnostics string `json:"err"`
		}
		if err := json.Unmarshal(lines.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Message == "testenv.engine.exited" {
			diagnostics[record.Container] = record.Diagnostics
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	return diagnostics
}
