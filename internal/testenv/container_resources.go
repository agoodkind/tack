package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/clock"
)

// ContainerCPUSample contains the cumulative cgroup v2 CPU counters of one
// container at one instant.
type ContainerCPUSample struct {
	Container        string
	UsageMicros      int64
	ThrottledMicros  int64
	ThrottledPeriods int64
}

// FoundationDBContainer returns the container that serves a cluster file
// created by FoundationDB or FreshFoundationDB.
func FoundationDBContainer(clusterFile string) string {
	return filepath.Base(filepath.Dir(clusterFile))
}

// RunnerContainer returns the ID of the container this test process runs in,
// or an empty string when the process runs on the host.
func RunnerContainer(ctx context.Context) (string, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	runner, err := selfContainer(ctx, cli)
	if err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "testenv.runner.identified", slog.String("container", runner))
	return runner, nil
}

// ContainerResourceLimits reports HostConfig NanoCPUs and Memory, cgroup cpu.max
// and memory.max, and the Docker daemon CPU count and total memory for an engine
// this process started or the container this process runs in.
func ContainerResourceLimits(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	if err := requireEvidenceContainer(ctx, cli, name); err != nil {
		return "", err
	}
	inspected, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{Size: false})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.resources.inspect_failed", slog.String("container", name), slog.String("err", err.Error()))
		return "", fmt.Errorf("inspect resource container %s: %w", name, err)
	}
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.resources.info_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read Docker daemon information: %w", err)
	}
	var output strings.Builder
	_, _ = fmt.Fprintf(&output, "container=%s at=%s nano_cpus=%d memory=%d daemon_cpus=%d daemon_memory=%d",
		name, clock.Now().UTC().Format(time.RFC3339Nano), inspected.Container.HostConfig.NanoCPUs,
		inspected.Container.HostConfig.Memory, info.Info.NCPU, info.Info.MemTotal)
	for _, file := range []string{"cpu.max", "memory.max"} {
		contents, err := readCgroupFile(ctx, cli, name, file)
		if err != nil {
			return output.String(), err
		}
		_, _ = fmt.Fprintf(&output, " %s=%q", file, contents)
	}
	slog.InfoContext(ctx, "testenv.resources.limits_read", slog.String("container", name))
	return output.String(), nil
}

// ContainerCPU reads the cgroup cpu.stat counters of an engine this process
// started or of the container this process runs in.
func ContainerCPU(ctx context.Context, name string) (ContainerCPUSample, error) {
	empty := ContainerCPUSample{Container: name, UsageMicros: 0, ThrottledMicros: 0, ThrottledPeriods: 0}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		return empty, err
	}
	defer func() { _ = cli.Close() }()
	if err := requireEvidenceContainer(ctx, cli, name); err != nil {
		return empty, err
	}
	contents, err := readCgroupFile(ctx, cli, name, "cpu.stat")
	if err != nil {
		return empty, err
	}
	sample, err := parseCPUStat(name, contents)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.resources.cpu_stat_invalid", slog.String("container", name), slog.String("err", err.Error()))
		return empty, err
	}
	slog.DebugContext(ctx, "testenv.resources.cpu_sampled", slog.String("container", name))
	return sample, nil
}

// parseCPUStat reads usage_usec, throttled_usec, and nr_throttled from a
// cgroup v2 cpu.stat file.
func parseCPUStat(name, contents string) (ContainerCPUSample, error) {
	values := map[string]int64{}
	for line := range strings.SplitSeq(contents, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			continue
		}
		values[key] = number
	}
	for _, key := range []string{"usage_usec", "throttled_usec", "nr_throttled"} {
		if _, found := values[key]; !found {
			return ContainerCPUSample{Container: name, UsageMicros: 0, ThrottledMicros: 0, ThrottledPeriods: 0},
				fmt.Errorf("cpu.stat of %s has no %s: %q", name, key, contents)
		}
	}
	return ContainerCPUSample{
		Container:        name,
		UsageMicros:      values["usage_usec"],
		ThrottledMicros:  values["throttled_usec"],
		ThrottledPeriods: values["nr_throttled"],
	}, nil
}

// readCgroupFile reads one file of a container's cgroup v2 directory.
func readCgroupFile(ctx context.Context, cli *client.Client, name, file string) (string, error) {
	contents, exitCode, err := execInContainer(ctx, cli, name, []string{"cat", "/sys/fs/cgroup/" + file})
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		err := fmt.Errorf("read cgroup %s of %s: exit %d: %s", file, name, exitCode, strings.TrimSpace(contents))
		slog.ErrorContext(ctx, "testenv.resources.cgroup_failed", slog.String("container", name), slog.String("err", err.Error()))
		return "", err
	}
	return strings.TrimSpace(contents), nil
}

// requireEvidenceContainer accepts an engine this process started or the
// container this process runs in, and rejects every other container.
func requireEvidenceContainer(ctx context.Context, cli *client.Client, name string) error {
	owned.Lock()
	isOwned := slices.Contains(owned.containers, name)
	owned.Unlock()
	if isOwned {
		return nil
	}
	runner, err := selfContainer(ctx, cli)
	if err != nil {
		return err
	}
	if runner != "" && runner == name {
		return nil
	}
	err = fmt.Errorf("container %s is neither an engine of this test process nor its runner", name)
	slog.ErrorContext(ctx, "testenv.resources.unowned", slog.String("err", err.Error()))
	return err
}

// selfContainer returns the ID of the container this process runs in, or an
// empty string on the host. Docker sets a container's hostname to its ID.
func selfContainer(ctx context.Context, cli *client.Client) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		slog.ErrorContext(ctx, "testenv.hostname_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read this host's name: %w", err)
	}
	self, err := cli.ContainerInspect(ctx, hostname, client.ContainerInspectOptions{Size: false})
	if cerrdefs.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "testenv.self.inspect_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("inspect container %s: %w", hostname, err)
	}
	if self.Container.Config == nil || self.Container.Config.Hostname != hostname {
		return "", nil
	}
	return self.Container.ID, nil
}
