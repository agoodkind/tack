//go:build macrunnerfixture && runtimeaudit

package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// MacRunnerOwnershipFixture exercises real engine creation and owner labels.
func MacRunnerOwnershipFixture(t *testing.T, mode string) {
	t.Helper()
	ctx := t.Context()
	docker, err := dockerClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = docker.Close() }()
	runner, err := selfContainer(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := docker.ContainerInspect(ctx, runner, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image := inspection.Container.Image
	t.Logf("MAC_SELF_MOUNTS mounts=%+v requests=%+v", inspection.Container.Mounts, inspection.Container.HostConfig.Mounts)
	macRunnerChangeAdmission(t, mode)
	owner, err := prepareCreate(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "independent" && !owner.native {
		t.Fatal("native runner identity did not verify")
	}
	ordinary, err := startEngine(ctx, docker, engineSpec{kind: "ownership", image: image, entrypoint: []string{"/bin/sleep"}, cmd: []string{"240"}})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "independent" {
		fmt.Println("MAC_INDEPENDENT_ENGINE=" + ordinary.name)
		time.Sleep(4 * time.Minute)
		return
	}
	fixed := "fixed-" + owner.run
	if _, err := startEngine(ctx, docker, engineSpec{kind: "fixed", name: fixed, image: image, entrypoint: []string{"/bin/sleep"}, cmd: []string{"240"}}); err != nil {
		t.Fatal(err)
	}
	clusterName := "tack-testenv-search-" + owner.run
	for member := 1; member <= 2; member++ {
		name := fmt.Sprintf("%s-%d", clusterName, member)
		if err := startClusterContainer(ctx, docker, clusterContainer{name: name, image: image, cmd: []string{"version"}}); err != nil {
			t.Fatal(err)
		}
	}
	cluster := &OpenSearchCluster{name: clusterName, proxy: clusterName + "-proxy"}
	cluster.authority, err = newClusterAuthority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := cluster.startProxy(ctx, docker); err != nil {
		t.Fatal(err)
	}
	slog.Info("testenv.cluster.started", slog.String("cluster", clusterName), slog.Int("planned", 2))
	network, _, err := createLedgerClusterNetwork(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := joinWhenContainerized(ctx, docker, network); err != nil {
		t.Fatal(err)
	}
	if _, err := startEngine(ctx, docker, engineSpec{kind: "network-member", image: image, entrypoint: []string{"/bin/sleep"}, cmd: []string{"240"}, attachNetwork: network}); err != nil {
		t.Fatal(err)
	}
	labels := ownerLabels(owner)
	if mode == "unknown" {
		labels = map[string]string{managedLabel: "true"}
	}
	volumeFixture, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: "volume-" + owner.run, Config: &container.Config{Image: image, Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"240"}, Labels: labels},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Target: "/fixture-volume"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerStart(ctx, volumeFixture.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	fmt.Println("MAC_FIXTURE_VOLUME_CONTAINER=" + volumeFixture.ID)
	for _, path := range []string{"/root/.cache/go-build/mac-runner-marker", "/go/pkg/mod/mac-runner-marker"} {
		if err := os.WriteFile(path, []byte("MAC_RUNNER_CACHE_MARKER\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("MAC_RUNNER_SIGNAL_READY")
	switch mode {
	case "release":
		time.Sleep(time.Second)
		if err := Release(context.WithoutCancel(ctx)); err != nil {
			t.Fatal(err)
		}
	case "wait", "shared":
		time.Sleep(4 * time.Minute)
	case "mutation":
		path := filepath.Join("example_test.go")
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(contents, []byte("\n// Fixture mutation.\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		time.Sleep(time.Second)
	}
}

func macRunnerChangeAdmission(t *testing.T, mode string) {
	t.Helper()
	if mode == "missing-admission" {
		if err := os.Remove(macRunnerAdmissionPath); err != nil {
			t.Fatal(err)
		}
	}
	if mode != "mismatched-admission" {
		return
	}
	contents, err := os.ReadFile(macRunnerAdmissionPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt macRunnerAdmission
	if err := json.Unmarshal(contents, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.RunnerID = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	contents, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(macRunnerAdmissionPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
