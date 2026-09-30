package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// DisposableOpenSearch starts an uncached engine for one resource regression.
func DisposableOpenSearch(t T, memoryBytes int64) OpenSearchFixture {
	t.Helper()
	skipWhenShort(t)
	skipWithoutSearchIntegration(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	fixture, err := provisionOpenSearch(ctx, memoryBytes)
	if err != nil {
		_, _ = fmt.Fprintln(t.Output(), "start disposable OpenSearch:", err)
		t.FailNow()
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(t.Output(), "inspect disposable OpenSearch:", err)
		t.FailNow()
	}
	defer func() { _ = cli.Close() }()
	output, exitCode, err := execInContainer(ctx, cli, fixture.Container, []string{"cat", "/sys/fs/cgroup/memory.max"})
	if err != nil || exitCode != 0 || strings.TrimSpace(output) != strconv.FormatInt(memoryBytes, 10) {
		_, _ = fmt.Fprintf(t.Output(), "OpenSearch memory.max=%q, exit=%d, error=%v, want=%d\n", output, exitCode, err, memoryBytes)
		t.FailNow()
	}
	_, _ = fmt.Fprintf(t.Output(), "OpenSearch %s cgroup memory.max=%d\n", fixture.Container, memoryBytes)
	return fixture
}

// Close removes this fixture's engine without removing another test's engine.
func (fixture OpenSearchFixture) Close(ctx context.Context) error {
	if err := removeContainers(ctx, []string{fixture.Container}); err != nil {
		slog.ErrorContext(ctx, "testenv.search.cleanup_failed", slog.String("container", fixture.Container), slog.String("err", err.Error()))
		return fmt.Errorf("remove search fixture %s: %w", fixture.Container, err)
	}
	return nil
}
