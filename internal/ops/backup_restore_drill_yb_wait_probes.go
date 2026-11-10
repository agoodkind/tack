// backup_restore_drill_yb_wait_probes.go wires the scratch yugabyted wait to
// what it reads: the container's running state from the Docker daemon, the
// step's readiness command, and progress counters from the engine's own status
// pages, fetched with the curl the yugabyte image carries.

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

// Progress counter names, as they appear in a stall error.
const (
	ybCounterMasterAnswers  = "master_answers"
	ybCounterTServerAnswers = "tserver_answers"
	ybCounterRunningTablets = "running_tablets"
	ybCounterSSTFiles       = "sst_files"
	ybCounterDataBytes      = "data_bytes"
	ybTabletStateRunning    = "RUNNING"
	// ybScratchDataDir is the scratch engine's data directory under the
	// --base_dir the drill starts it with.
	ybScratchDataDir = "/home/yugabyte/var/data"
)

// ybScratchTablet is the part of one tablet server status entry the wait
// reads. The page is keyed by tablet id.
type ybScratchTablet struct {
	State       string `json:"state"`
	NumSSTFiles int64  `json:"num_sst_files"`
}

// newYBScratchWatch builds the watch for one step against the scratch
// container. master is the scratch master address whose restorations the
// failure check reads, or "" for a step that runs no restoration. readyCmd is
// the step's readiness command and env its environment.
func newYBScratchWatch(r *restoreDrillCtx, container, master string, env, readyCmd []string) ybScratchWatch {
	return ybScratchWatch{
		Running: func(ctx context.Context) (bool, error) {
			health, err := inspectLedgerNodeHealth(ctx, r.Cli, container)
			return health.running, err
		},
		Failed: newYBScratchFailureProbe(r, container, master),
		Ready: func(ctx context.Context) (bool, error) {
			exitCode, _, err := containerExecStreaming(ctx, r.Cli, container, readyCmd, env, devNull{})
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

// readYBScratchCounters reads the master's and tablet server's status pages
// and the data directory's size. A page that answers sets its answers counter
// to 1, so an engine moving through its start reads as moving; the tablet page
// adds the count of running tablets and the files they hold, which rise as a
// restoration lands. Only a reading where no counter could be read is an
// error.
func readYBScratchCounters(ctx context.Context, r *restoreDrillCtx, container string) (map[string]int64, error) {
	counters := map[string]int64{}
	var failures []string
	if _, err := fetchYBScratchPage(ctx, r, container, "http://"+ybScratchHost(container)+":7000/"); err != nil {
		failures = append(failures, "master: "+err.Error())
	} else {
		counters[ybCounterMasterAnswers] = 1
	}
	body, err := fetchYBScratchPage(ctx, r, container, "http://"+ybScratchHost(container)+":9000/api/v1/tablets")
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

// readYBScratchDataBytes reads how many bytes the scratch engine's data
// directory holds, leaving out its logs. A master replaying its catalog onto a
// slow disk shows no new status page and no new tablet for minutes while it
// writes, so the bytes it writes are the reading that moves in that phase
// (observed on QA 2026-09-15: the master answered its page but stayed not
// leader-ready while its log fsyncs took up to 0.19 s each). The engine's logs
// sit under the same directory and grow on every heartbeat, so counting them
// would read a wedged engine as one still moving.
func readYBScratchDataBytes(ctx context.Context, r *restoreDrillCtx, container string) (int64, error) {
	res, err := containerExec(ctx, r.Cli, container, []string{"du", "-sb", "--exclude=logs", ybScratchDataDir})
	if err != nil {
		wrapped := fmt.Errorf("du %s: %w", ybScratchDataDir, err)
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.data_bytes_unreadable", slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	return parseYBScratchDataBytes(ctx, res.ExitCode, res.Stdout)
}

// parseYBScratchDataBytes reads the byte count from one `du -sb` output. GNU
// du exits 1 when a file vanished during the walk, which compaction causes,
// and still prints the total, so exit 1 with a total is a reading.
func parseYBScratchDataBytes(ctx context.Context, exitCode int, stdout string) (int64, error) {
	fields := strings.Fields(stdout)
	if exitCode > 1 || len(fields) == 0 {
		err := fmt.Errorf("du %s exited %d with %q", ybScratchDataDir, exitCode, strings.TrimSpace(stdout))
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.data_bytes_unreadable", slog.String("err", err.Error()))
		return 0, err
	}
	bytes, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		wrapped := fmt.Errorf("parse du %s output %q: %w", ybScratchDataDir, fields[0], err)
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.data_bytes_unreadable", slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	return bytes, nil
}

// fetchYBScratchPage runs curl inside the scratch container and returns the
// body of a page that answered with a success status.
func fetchYBScratchPage(ctx context.Context, r *restoreDrillCtx, container, url string) (string, error) {
	res, err := containerExec(ctx, r.Cli, container, []string{"curl", "-sf", "-m", "20", url})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("curl %s exited %d", url, res.ExitCode)
	}
	return res.Stdout, nil
}

// ybScratchTabletCounters counts running tablets and the files they hold from
// the tablet server's tablet page.
func ybScratchTabletCounters(ctx context.Context, body string) (map[string]int64, error) {
	var tablets map[string]ybScratchTablet
	if err := json.Unmarshal([]byte(body), &tablets); err != nil {
		wrapped := fmt.Errorf("unmarshal tablet server tablets page: %w", err)
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.tablets_unparseable",
			slog.String("err", wrapped.Error()))
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
