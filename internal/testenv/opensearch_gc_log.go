package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"goodkind.io/tack/internal/clock"
)

const (
	// openSearchGCLogLines is the number of garbage collection log lines the
	// failure diagnostics include. One collection writes about twenty lines.
	openSearchGCLogLines = 300
	// openSearchGCLogTimeout bounds the log read, as the container evidence
	// read is bounded.
	openSearchGCLogTimeout = 20 * time.Second
	// openSearchLogDirectory is the directory where the engine image writes
	// its logs.
	openSearchLogDirectory = "/usr/share/opensearch/logs"
)

// openSearchGCLogFiles are the files the engine can write its garbage
// collection log to. The JVM writes the active log to gc.log and renames a
// full file to gc.log.0, so gc.log is read first.
var openSearchGCLogFiles = []string{
	openSearchLogDirectory + "/gc.log",
	openSearchLogDirectory + "/gc.log.0",
}

// OpenSearchGCLogTail returns the last lines of the garbage collection log of
// an engine this test process created. Each collection line states the heap
// size before and after the collection. The read works on a stopped engine as
// well. The error is nil only when one log file was read.
func OpenSearchGCLogTail(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, openSearchGCLogTimeout)
	defer cancel()
	owned.Lock()
	isOwned := slices.Contains(owned.containers, name)
	owned.Unlock()
	if !isOwned {
		err := fmt.Errorf("container %s was not created by this test process", name)
		slog.ErrorContext(ctx, "testenv.search.gc_log_unowned", slog.String("err", err.Error()))
		return "", err
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	var failures []string
	for _, file := range openSearchGCLogFiles {
		tail, err := containerFileTail(ctx, cli, name, file, openSearchGCLogLines)
		if err == nil {
			return fmt.Sprintf("container=%s at=%s file=%s last %d lines:\n%s",
				name, clock.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), file, openSearchGCLogLines, tail), nil
		}
		failures = append(failures, err.Error())
	}
	err = fmt.Errorf("read the garbage collection log of %s: %s", name, strings.Join(failures, "; "))
	slog.ErrorContext(ctx, "testenv.search.gc_log_unreadable", slog.String("err", err.Error()))
	return "", err
}
