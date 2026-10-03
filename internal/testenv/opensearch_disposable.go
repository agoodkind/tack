package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// openSearchDataPath is the engine's data path in the image.
	openSearchDataPath = "/usr/share/opensearch/data"
	// openSearchMLDiskThreshold is the ML Commons free-space floor that
	// Configs sets on every Tack search member. ML Commons refuses a local
	// model deploy or prediction below it.
	openSearchMLDiskThreshold = "plugins.ml_commons.disk_free_space_threshold=1gb"
	// openSearchInfoInterval is the minimum cluster.info.update.interval of
	// OpenSearch 3.8.0. The disk threshold monitor reads disk use at that
	// interval.
	openSearchInfoInterval = "cluster.info.update.interval=10s"
)

// openSearchEngineSettings are the container settings of one engine. With an
// empty dataVolume the data path stays on the container's own filesystem.
// The image entrypoint passes each nodeSettings entry to OpenSearch as an -E
// setting.
type openSearchEngineSettings struct {
	memoryBytes  int64
	dataVolume   string
	nodeSettings []string
}

// openSearchHostConfig shares holder's network stack and applies the memory
// limit and the optional data volume.
func openSearchHostConfig(holder string, settings openSearchEngineSettings) *container.HostConfig {
	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode("container:" + holder),
		Resources:   container.Resources{Memory: settings.memoryBytes},
	}
	if settings.dataVolume != "" {
		hostConfig.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: settings.dataVolume, Target: openSearchDataPath}}
	}
	return hostConfig
}

// DisposableOpenSearchOptions sizes one disposable engine. DataBytes is the
// size of the ext4 filesystem mounted at the data path.
type DisposableOpenSearchOptions struct {
	MemoryBytes int64
	DataBytes   int64
}

// DisposableEngine is one engine that belongs to one test. Its data path is
// a size-bounded filesystem on the Docker host disk, and the disk threshold
// monitor reads that filesystem. A test fills it to cross the flood-stage
// watermark without filling the host disk or a shared engine.
type DisposableEngine struct {
	Fixture OpenSearchFixture
}

// DisposableOpenSearch starts an engine that no other test shares on a data
// disk of DataBytes, and requires the container's cgroup memory limit to
// equal MemoryBytes. The test's cleanup removes the engine, the address
// holder, and the data disk, also when the test or the start fails. Like a
// cluster, the engine starts only when [SearchClusterVariable] is "1".
func DisposableOpenSearch(t *testing.T, options DisposableOpenSearchOptions) *DisposableEngine {
	t.Helper()
	skipWhenShort(t)
	skipWithoutSearchCluster(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	var fixture OpenSearchFixture
	var created []string
	var disk openSearchDataDisk
	t.Cleanup(func() { removeDisposableOpenSearch(t, fixture.Container, created, disk) })
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("open docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()
	disk, err = createOpenSearchDataDisk(ctx, cli, options.DataBytes)
	if err != nil {
		t.Fatalf("create the disposable OpenSearch data disk: %v", err)
	}
	fixture, created, err = provisionOpenSearch(ctx, openSearchEngineSettings{
		memoryBytes: options.MemoryBytes, dataVolume: disk.dataVolume,
		nodeSettings: []string{openSearchInfoInterval, openSearchMLDiskThreshold},
	})
	if err != nil {
		t.Fatalf("start disposable OpenSearch: %v", err)
	}
	engine := &DisposableEngine{Fixture: fixture}
	limit := strings.TrimSpace(engine.run(t, "cat", "/sys/fs/cgroup/memory.max"))
	if limit != strconv.FormatInt(options.MemoryBytes, 10) {
		t.Fatalf("OpenSearch %s cgroup memory.max = %s, want %d", fixture.Container, limit, options.MemoryBytes)
	}
	return engine
}

// removeDisposableOpenSearch logs the engine state, removes the engine and
// the address holder, then removes the data disk that the engine mounted.
func removeDisposableOpenSearch(t *testing.T, engineName string, created []string, disk openSearchDataDisk) {
	t.Helper()
	cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), provisionTimeout)
	defer stop()
	if engineName != "" {
		evidence, err := OpenSearchResourceEvidence(cleanup, engineName)
		t.Logf("disposable OpenSearch state before removal (read error %v):\n%s", err, evidence)
	}
	if err := removeContainers(cleanup, created); err != nil {
		t.Errorf("remove disposable OpenSearch %v: %v", created, err)
		return
	}
	t.Logf("removed disposable OpenSearch containers %v", created)
	cli, err := dockerClient(cleanup)
	if err != nil {
		t.Errorf("open docker client to remove the data disk %s: %v", disk.helper, err)
		return
	}
	defer func() { _ = cli.Close() }()
	if err := removeOpenSearchDataDisk(cleanup, cli, disk); err != nil {
		t.Errorf("remove the disposable OpenSearch data disk %s: %v", disk.helper, err)
		return
	}
	t.Logf("removed data disk helper %s, volumes %s and %s, and loop device %s",
		disk.helper, disk.dataVolume, disk.imageVolume, disk.device)
}

// MemoryCurrent returns the cgroup memory.current of the engine container.
func (e *DisposableEngine) MemoryCurrent(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(e.run(t, "cat", "/sys/fs/cgroup/memory.current"))
}

// run runs command inside the engine and fails the test on a nonzero exit.
func (e *DisposableEngine) run(t *testing.T, command ...string) string {
	t.Helper()
	ctx := t.Context()
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("open docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()
	output, code, err := execInContainer(ctx, cli, e.Fixture.Container, command)
	if err != nil || code != 0 {
		wrapped := fmt.Errorf("run %v in %s: exit %d: %s: %w", command, e.Fixture.Container, code, output, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.fixture_exec_failed", slog.String("err", wrapped.Error()))
		t.Fatal(wrapped)
	}
	return output
}
