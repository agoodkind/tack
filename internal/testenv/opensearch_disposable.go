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
	// openSearchFillerPath is the file that fills the data path.
	openSearchFillerPath = openSearchDataPath + "/tack-disk-filler"
	// openSearchDataMode lets the image's own user write the tmpfs root.
	openSearchDataMode = 0o1777
	// openSearchInfoInterval is the minimum cluster.info.update.interval of
	// OpenSearch 3.8.0. The disk threshold monitor reads disk use at that
	// interval.
	openSearchInfoInterval = "cluster.info.update.interval=10s"
	// fillChunkBytes is the dd block size of the filler.
	fillChunkBytes = 1 << 20
)

// openSearchEngineSettings are the container settings of one engine. With a
// zero dataTmpfsBytes the data path stays on the container's own
// filesystem. The image entrypoint passes each nodeSettings entry to
// OpenSearch as an -E setting.
type openSearchEngineSettings struct {
	memoryBytes    int64
	dataTmpfsBytes int64
	nodeSettings   []string
}

// openSearchHostConfig shares holder's network stack and applies the memory
// limit and the optional tmpfs data path.
func openSearchHostConfig(holder string, settings openSearchEngineSettings) *container.HostConfig {
	hostConfig := &container.HostConfig{
		NetworkMode: container.NetworkMode("container:" + holder),
		Resources:   container.Resources{Memory: settings.memoryBytes},
	}
	if settings.dataTmpfsBytes > 0 {
		hostConfig.Mounts = []mount.Mount{{
			Type: mount.TypeTmpfs, Target: openSearchDataPath,
			TmpfsOptions: &mount.TmpfsOptions{SizeBytes: settings.dataTmpfsBytes, Mode: openSearchDataMode},
		}}
	}
	return hostConfig
}

// DisposableOpenSearchOptions sizes one disposable engine. DataBytes is the
// size of the tmpfs mounted at the data path. The tmpfs counts toward
// MemoryBytes.
type DisposableOpenSearchOptions struct {
	MemoryBytes int64
	DataBytes   int64
}

// DisposableEngine is one engine that belongs to one test. Its data path is
// a size-bounded tmpfs, and the disk threshold monitor reads that tmpfs. A
// test fills it to cross the flood-stage watermark without writing to the
// host disk or a shared engine.
type DisposableEngine struct {
	Fixture OpenSearchFixture
}

// DisposableOpenSearch starts an engine that no other test shares and
// requires the container's cgroup memory limit to equal MemoryBytes. The
// test's cleanup removes the engine with its tmpfs and the address holder,
// also when the test or the start fails. Like a cluster, the engine starts
// only when [SearchClusterVariable] is "1".
func DisposableOpenSearch(t *testing.T, options DisposableOpenSearchOptions) *DisposableEngine {
	t.Helper()
	skipWhenShort(t)
	skipWithoutSearchCluster(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	fixture, created, err := provisionOpenSearch(ctx, openSearchEngineSettings{
		memoryBytes: options.MemoryBytes, dataTmpfsBytes: options.DataBytes,
		nodeSettings: []string{openSearchInfoInterval},
	})
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), provisionTimeout)
		defer stop()
		if fixture.Container != "" {
			evidence, err := OpenSearchResourceEvidence(cleanup, fixture.Container)
			t.Logf("disposable OpenSearch state before removal (read error %v):\n%s", err, evidence)
		}
		if err := removeContainers(cleanup, created); err != nil {
			t.Errorf("remove disposable OpenSearch %v: %v", created, err)
			return
		}
		t.Logf("removed disposable OpenSearch containers %v", created)
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

// DataUsage returns the size and the used bytes of the data path, read with
// df inside the engine.
func (e *DisposableEngine) DataUsage(t *testing.T) (int64, int64) {
	t.Helper()
	output := e.run(t, "df", "-B1", "--output=size,used", openSearchDataPath)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) != 2 {
		t.Fatalf("read data path usage from %q", output)
	}
	size, sizeErr := strconv.ParseInt(fields[0], 10, 64)
	used, usedErr := strconv.ParseInt(fields[1], 10, 64)
	if sizeErr != nil || usedErr != nil {
		t.Fatalf("parse data path usage %q: %v %v", output, sizeErr, usedErr)
	}
	return size, used
}

// FillData writes a filler file until the data path is at least percent
// used, and returns the size and used bytes after the write.
func (e *DisposableEngine) FillData(t *testing.T, percent int64) (int64, int64) {
	t.Helper()
	size, used := e.DataUsage(t)
	missing := size*percent/100 - used
	if missing > 0 {
		chunks := (missing + fillChunkBytes - 1) / fillChunkBytes
		e.run(t, "dd", "if=/dev/zero", "of="+openSearchFillerPath,
			"bs="+strconv.Itoa(fillChunkBytes), "count="+strconv.FormatInt(chunks, 10))
	}
	return e.DataUsage(t)
}

// FreeData removes the filler file and returns the size and used bytes
// after the removal.
func (e *DisposableEngine) FreeData(t *testing.T) (int64, int64) {
	t.Helper()
	e.run(t, "rm", "-f", openSearchFillerPath)
	return e.DataUsage(t)
}

// MemoryCurrent returns the cgroup memory.current of the engine container,
// which includes the tmpfs pages of the data path.
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
