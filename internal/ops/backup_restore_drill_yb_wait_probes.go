// The scratch yugabyted wait reads the container's running state from Docker,
// runs the step's readiness command, and reads engine progress counters from
// status pages. It fetches the pages with curl from the yugabyte image.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// These constants identify progress counters in stall errors and the running
// tablet state.
const (
	ybCounterMasterAnswers  = "master_answers"
	ybCounterTServerAnswers = "tserver_answers"
	ybCounterRunningTablets = "running_tablets"
	ybCounterSSTFiles       = "sst_files"
	ybCounterDataBytes      = "data_bytes"
	ybTabletStateRunning    = "RUNNING"
	// ybScratchDataDir identifies the scratch engine's data directory under the
	// --base_dir that the drill supplies.
	ybScratchDataDir = "/home/yugabyte/var/data"
)

// ybScratchTablet contains the fields that the wait reads from one tablet
// server status entry. The status page indexes entries by tablet ID.
type ybScratchTablet struct {
	State       string `json:"state"`
	NumSSTFiles int64  `json:"num_sst_files"`
}

// newYBScratchWatch builds the watch for one step in the scratch container.
// master specifies the scratch master address for restoration failure checks.
// An empty master identifies a step that does not run a restoration. readyCmd
// specifies the readiness command, and env supplies its environment.
func newYBScratchWatch(
	r *restoreDrillCtx,
	container string,
	master string,
	env []string,
	readyCmd []string,
) ybScratchWatch {
	return ybScratchWatch{
		Running: func(ctx context.Context) (bool, error) {
			health, err := inspectLedgerNodeHealth(ctx, r.Cli, container)
			return health.running, err
		},
		Failed: newYBScratchFailureProbe(r, container, master),
		Ready: func(ctx context.Context) (bool, error) {
			exitCode, _, err := containerExecStreaming(
				ctx,
				r.Cli,
				container,
				readyCmd,
				env,
				devNull{},
			)
			if err != nil {
				return false, err
			}

			return exitCode == 0, nil
		},
		Progress: func(ctx context.Context) (map[string]int64, error) {
			return readYBScratchCounters(ctx, r, container)
		},
	}
}

// readYBScratchCounters reads the master and tablet server status pages and
// the data directory's size. Each successful page read sets its answer counter
// to 1 to detect startup progress. The tablet page supplies a running-tablet
// count and a file count across all tablets. Those counts can increase during
// restoration. The function returns an error only if it cannot read any
// counter.
func readYBScratchCounters(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
) (map[string]int64, error) {
	counters := map[string]int64{}
	var failures []string
	if _, err := fetchYBScratchPage(
		ctx,
		r,
		container,
		"http://"+ybScratchHost(container)+":7000/",
	); err != nil {
		failures = append(failures, "master: "+err.Error())
	} else {
		counters[ybCounterMasterAnswers] = 1
	}

	body, err := fetchYBScratchPage(
		ctx,
		r,
		container,
		"http://"+ybScratchHost(container)+":9000/api/v1/tablets",
	)
	if err != nil {
		failures = append(failures, "tablet server: "+err.Error())
	} else {
		counters[ybCounterTServerAnswers] = 1
		tabletCounters, parseErr := ybScratchTabletCounters(ctx, body)
		if parseErr != nil {
			failures = append(failures, parseErr.Error())
		}

		maps.Copy(counters, tabletCounters)
	}

	dataBytes, err := readYBScratchDataBytes(ctx, r, container)
	if err != nil {
		failures = append(failures, err.Error())
	} else {
		counters[ybCounterDataBytes] = dataBytes
	}

	if len(counters) == 0 {
		return nil, errors.New(strings.Join(failures, "; "))
	}

	return counters, nil
}

// readYBScratchDataBytes reads the scratch data directory's size without logs.
// A master can spend minutes replaying its catalog on a slow disk without a
// new status page or tablet. Data writes provide a progress reading then.
//
// On QA on 2026-09-15, the master answered its status page but did not become
// ready as leader while log fsync operations took up to 0.19 s each. fsync
// requires the operating system to finish writing data to storage.
//
// Engine logs share the data directory and grow with every heartbeat. Counting
// them would mistake log writes from a stalled engine for restoration progress.
func readYBScratchDataBytes(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
) (int64, error) {
	res, err := containerExec(
		ctx,
		r.Cli,
		container,
		[]string{"du", "-sb", "--exclude=logs", ybScratchDataDir},
	)
	if err != nil {
		wrapped := fmt.Errorf("du %s: %w", ybScratchDataDir, err)
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.restore_drill.yb.data_bytes_unreadable",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	return parseYBScratchDataBytes(ctx, res.ExitCode, res.Stdout)
}

// parseYBScratchDataBytes reads the byte count from du -sb output. GNU du can
// exit 1 after compaction removes a file during the directory scan and still
// print the total. Exit 1 with a total counts as a reading.
func parseYBScratchDataBytes(ctx context.Context, exitCode int, stdout string) (int64, error) {
	fields := strings.Fields(stdout)
	if exitCode > 1 || len(fields) == 0 {
		err := fmt.Errorf(
			"du %s exited %d with %q",
			ybScratchDataDir,
			exitCode,
			strings.TrimSpace(stdout),
		)
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.restore_drill.yb.data_bytes_unreadable",
			slog.String("err", err.Error()),
		)

		return 0, err
	}

	bytes, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		wrapped := fmt.Errorf("parse du %s output %q: %w", ybScratchDataDir, fields[0], err)
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.restore_drill.yb.data_bytes_unreadable",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	return bytes, nil
}

// fetchYBScratchPage runs curl inside the scratch container and returns the
// body when curl reports success.
func fetchYBScratchPage(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	url string,
) (string, error) {
	res, err := containerExec(ctx, r.Cli, container, []string{"curl", "-sf", "-m", "20", url})
	if err != nil {
		return "", err
	}

	if res.ExitCode != 0 {
		return "", fmt.Errorf("curl %s exited %d", url, res.ExitCode)
	}

	return res.Stdout, nil
}

// ybScratchTabletCounters counts running tablets and sums file counts across
// all tablets in the tablet server's status page.
func ybScratchTabletCounters(ctx context.Context, body string) (map[string]int64, error) {
	var tablets map[string]ybScratchTablet
	if err := json.Unmarshal([]byte(body), &tablets); err != nil {
		wrapped := fmt.Errorf("unmarshal tablet server tablets page: %w", err)
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.restore_drill.yb.tablets_unparseable",
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	var running, files int64

	for _, tablet := range tablets {
		if tablet.State == ybTabletStateRunning {
			running++
		}

		files += tablet.NumSSTFiles
	}

	return map[string]int64{ybCounterRunningTablets: running, ybCounterSSTFiles: files}, nil
}
