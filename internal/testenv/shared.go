package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	// sharedParent is the directory under the repository root that holds the
	// directories SharedDir hands out. The leading dot keeps go tooling out.
	sharedParent = ".testenv"
	// sharedSentinel is the file the daemon probe looks for.
	sharedSentinel = "sentinel"
	// sharedProbeMount is where the probe container mounts the directory.
	sharedProbeMount = "/probe"
)

// SharedDir returns a new empty directory that this process and the Docker
// daemon see at the same path, for a test that bind-mounts a directory it
// also reads or writes. The directory is under the repository root, which the
// test runner container mounts at its host path (docker-compose.test.yml),
// so the daemon also sees repository files such as the engine overlays at
// the paths this process uses. [Release] removes the directory. The test fails
// with the reason when a probe container started by the daemon cannot read a
// file this process wrote there.
func SharedDir(t T) string {
	t.Helper()
	skipWhenShort(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	directory, err := sharedDir(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", err)
		t.FailNow()
	}
	return directory
}

// sharedDir creates the directory and proves the daemon sees it.
func sharedDir(ctx context.Context) (string, error) {
	root, err := repoRoot(ctx)
	if err != nil {
		return "", err
	}
	parent := filepath.Join(root, sharedParent)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		slog.ErrorContext(ctx, "testenv.shared.mkdir_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("create %s: %w", parent, err)
	}
	directory, err := os.MkdirTemp(parent, "shared-")
	if err != nil {
		slog.ErrorContext(ctx, "testenv.shared.mkdir_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("create a directory under %s: %w", parent, err)
	}
	ownDirectory(directory)
	sentinel := filepath.Join(directory, sharedSentinel)
	if err := os.WriteFile(sentinel, nil, 0o600); err != nil {
		slog.ErrorContext(ctx, "testenv.shared.sentinel_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("write %s: %w", sentinel, err)
	}
	seen, err := daemonSeesSentinel(ctx, directory)
	if err != nil {
		return "", err
	}
	if !seen {
		return "", fmt.Errorf("the Docker daemon cannot see %s at that path, so a bind mount of it would name a different directory; "+
			"a test running in a container must mount the repository at its host path, as `make test-unit` does through docker-compose.test.yml",
			directory)
	}
	if err := os.Remove(sentinel); err != nil {
		slog.ErrorContext(ctx, "testenv.shared.sentinel_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("remove %s: %w", sentinel, err)
	}
	return directory, nil
}

// daemonSeesSentinel runs a container that bind-mounts directory and reports
// whether the sentinel file is in it. The container runs the FoundationDB
// engine image, which the store-backed tests pull anyway.
func daemonSeesSentinel(ctx context.Context, directory string) (bool, error) {
	image, err := serviceImage(ctx, foundationDBService)
	if err != nil {
		return false, err
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = cli.Close() }()
	if err := ensureImage(ctx, cli, engineSpec{kind: "probe", image: image, platform: nil, cmd: nil, env: nil}); err != nil {
		return false, err
	}
	suffix, err := randomHex(ctx, 4)
	if err != nil {
		return false, err
	}
	name := "tack-testenv-probe-" + strconv.Itoa(os.Getpid()) + "-" + suffix
	_, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      image,
			Entrypoint: []string{"test"},
			Cmd:        []string{"-f", sharedProbeMount + "/" + sharedSentinel},
			Labels:     map[string]string{managedLabel: "true"},
		},
		HostConfig: &container.HostConfig{
			Binds:       []string{directory + ":" + sharedProbeMount + ":ro"},
			NetworkMode: "none",
		},
		Name: name,
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.shared.probe_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("create probe container %s: %w", name, err)
	}
	defer func() { _ = removeContainersWith(context.WithoutCancel(ctx), cli, []string{name}) }()
	if _, err := cli.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		slog.ErrorContext(ctx, "testenv.shared.probe_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("start probe container %s: %w", name, err)
	}
	waited := cli.ContainerWait(ctx, name, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result := <-waited.Result:
		return result.StatusCode == 0, nil
	case err := <-waited.Error:
		slog.ErrorContext(ctx, "testenv.shared.probe_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("wait for probe container %s: %w", name, err)
	}
}
