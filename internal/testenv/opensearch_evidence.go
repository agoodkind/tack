package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/clock"
)

// OpenSearchResourceEvidence reads container state and cgroup memory from an
// engine created by this test process before Release removes it.
func OpenSearchResourceEvidence(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	owned.Lock()
	isOwned := slices.Contains(owned.containers, name)
	owned.Unlock()
	if !isOwned {
		err := fmt.Errorf("container %s was not created by this test process", name)
		slog.ErrorContext(ctx, "testenv.search.evidence_unowned", slog.String("err", err.Error()))
		return "", err
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	inspected, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.search.evidence_inspect_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("inspect search evidence container: %w", err)
	}
	engine := inspected.Container
	var output strings.Builder
	_, _ = fmt.Fprintf(&output, "container=%s at=%s image=%s running=%t restart_count=%d oom_killed=%t exit_code=%d configured_memory=%d\n",
		name, clock.Now().UTC().Format(time.RFC3339Nano), engine.Image, engine.State.Running,
		engine.RestartCount, engine.State.OOMKilled, engine.State.ExitCode, engine.HostConfig.Memory)
	if !engine.State.Running {
		output.WriteString("cgroup files are unavailable because the container is stopped\n")
		return output.String(), nil
	}
	for _, file := range []string{"memory.current", "memory.max", "memory.peak", "memory.events", "memory.stat"} {
		contents, exitCode, err := execInContainer(ctx, cli, name, []string{"cat", "/sys/fs/cgroup/" + file})
		_, _ = fmt.Fprintf(&output, "%s exit_code=%d contents=%s\n", file, exitCode, contents)
		if err != nil {
			return output.String(), err
		}
		if exitCode != 0 {
			err := fmt.Errorf("read search cgroup %s: exit %d", file, exitCode)
			slog.ErrorContext(ctx, "testenv.search.evidence_cgroup_failed", slog.String("err", err.Error()))
			return output.String(), err
		}
	}
	return output.String(), nil
}
