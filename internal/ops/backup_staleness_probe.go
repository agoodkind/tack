// backup_staleness_probe.go asks the two live systems the staleness check
// cannot date from the object store: the YugabyteDB masters, for whether the
// cluster is fully replicated right now, and the FoundationDB continuous
// backup, for how recent its restorable point is.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// ybMasterHealthPort is the master's HTTP admin port. The configured
	// master addresses carry RPC ports (7100), so the probe swaps this in.
	ybMasterHealthPort = "7000"
	// ybMasterHealthPath is the master endpoint that summarizes cluster
	// health. Verified against yugabytedb/yugabyte:2025.2.3.0-b149, which
	// answers 200 with
	// {"dead_nodes":[],"most_recent_uptime":0,"under_replicated_tablets":[]}.
	ybMasterHealthPath = "/api/v1/health-check"
	// ybMasterHealthTimeout bounds one master's probe so an unreachable host
	// cannot hold up the whole check.
	ybMasterHealthTimeout = 10 * time.Second
	// ybMasterHealthMaxBytes caps the response read. The payload lists every
	// under-replicated tablet, so an unhealthy cluster's response is unbounded
	// in principle; a truncated body fails to decode, which reports unhealthy
	// rather than vouching for a cluster nobody measured.
	ybMasterHealthMaxBytes = 4 << 20
)

// ybMasterHealthCheck is the master health-check payload. Both lists are
// pointers so an absent field is told apart from an empty one: a response that
// never mentions dead nodes has not vouched for anything, and counting that as
// zero would let any endpoint answering 200 certify the cluster healthy.
type ybMasterHealthCheck struct {
	DeadNodes              *[]string `json:"dead_nodes"`
	UnderReplicatedTablets *[]string `json:"under_replicated_tablets"`
}

// probeYBClusterHealth asks each configured master in turn and returns the
// first usable answer, because any master serves the whole cluster's view.
// detail always says what happened, healthy or not: it becomes the marker's
// detail on success and the report's reason on failure.
func probeYBClusterHealth(ctx context.Context, cfg *config.Config) (healthy bool, detail string) {
	urls := ybMasterHealthURLs(cfg.BackupYBMasterAddresses)
	if len(urls) == 0 {
		return false, "TACK_BACKUP_YB_MASTER_ADDRESSES names no master to probe"
	}
	var failures []string
	for _, url := range urls {
		body, err := fetchYBMasterHealth(ctx, url)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		replicated, healthDetail, parseErr := ybClusterHealthFromBody(ctx, body)
		if parseErr != nil {
			failures = append(failures, parseErr.Error())
			continue
		}
		return replicated, healthDetail
	}
	return false, "no master answered the health check: " + strings.Join(failures, "; ")
}

// ybMasterHealthURLs turns the configured comma-separated master addresses
// into health-check URLs on the admin port. The RPC port is dropped and the
// admin port joined back on, which brackets IPv6 literals.
func ybMasterHealthURLs(masterAddresses string) []string {
	var urls []string
	for entry := range strings.SplitSeq(masterAddresses, ",") {
		host := hostFromHostPort(strings.TrimSpace(entry))
		if host == "" {
			continue
		}
		urls = append(urls, "http://"+net.JoinHostPort(host, ybMasterHealthPort)+ybMasterHealthPath)
	}
	return urls
}

// fetchYBMasterHealth reads one master's health-check body. The endpoint
// answers with a JSON body under a text/plain content type, so the status code
// is the only transport-level check. An unreachable master is logged here and
// left to the caller to fall past, because another master serves the same view.
func fetchYBMasterHealth(ctx context.Context, url string) ([]byte, error) {
	logger := telemetry.L(ctx)
	reqCtx, cancel := context.WithTimeout(ctx, ybMasterHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		wrapped := fmt.Errorf("build health-check request for %s: %w", url, err)
		logger.WarnContext(ctx, "backup.staleness.master_unreachable", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("get %s: %w", url, err)
		logger.WarnContext(ctx, "backup.staleness.master_unreachable", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("get %s: status %d", url, resp.StatusCode)
		logger.WarnContext(ctx, "backup.staleness.master_unreachable", slog.String("err", statusErr.Error()))
		return nil, statusErr
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ybMasterHealthMaxBytes))
	if err != nil {
		wrapped := fmt.Errorf("read %s: %w", url, err)
		logger.WarnContext(ctx, "backup.staleness.master_unreachable", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return body, nil
}

// ybClusterHealthFromBody reports whether a master's health payload vouches for
// a cluster with every node alive and every tablet fully replicated. A payload
// missing either list is an error rather than a healthy verdict.
func ybClusterHealthFromBody(ctx context.Context, body []byte) (healthy bool, detail string, err error) {
	logger := telemetry.L(ctx)
	var payload ybMasterHealthCheck
	if err := json.Unmarshal(body, &payload); err != nil {
		wrapped := fmt.Errorf("unmarshal master health check: %w", err)
		logger.WarnContext(ctx, "backup.staleness.health_unparseable", slog.String("err", wrapped.Error()))
		return false, "", wrapped
	}
	if payload.DeadNodes == nil || payload.UnderReplicatedTablets == nil {
		missingErr := fmt.Errorf(
			"master health check omits dead_nodes or under_replicated_tablets")
		logger.WarnContext(ctx, "backup.staleness.health_unparseable", slog.String("err", missingErr.Error()))
		return false, "", missingErr
	}
	deadCount := len(*payload.DeadNodes)
	underReplicatedCount := len(*payload.UnderReplicatedTablets)
	detail = fmt.Sprintf("%d dead nodes, %d under-replicated tablets",
		deadCount, underReplicatedCount)
	return deadCount == 0 && underReplicatedCount == 0, detail, nil
}

const (
	// fdbLastCompleteLogLabel is the line `fdbbackup status` prints for the
	// end of the drained mutation log, which is the recent bound of what a
	// restore can reach. Captured verbatim from foundationdb 7.4.6:
	//   Last complete log version and timestamp        - 100720665, 2026/08/30.01:07:23+0000
	fdbLastCompleteLogLabel = "Last complete log version and timestamp"
	// fdbStatusTimestampLayout parses the timestamps in that output.
	fdbStatusTimestampLayout = "2006/01/02.15:04:05-0700"
	// fdbRestorableStatusText is the predicate a status uses to vouch for a
	// restorable point. On 7.4.6 the steady state of a continuous backup reads
	// "The backup on tag `default' is restorable but continuing to <url>",
	// while a backup whose first snapshot has not finished reads "is in
	// progress to <url>" and has no restorable point yet. The leading verb is
	// part of the match on purpose: the bare word also appears in destination
	// URLs and tag names, so matching it alone would let an in-progress backup
	// vouch for itself and report its advancing log timestamp as fresh.
	fdbRestorableStatusText = " is restorable"
	// fdbNotRestorableStatusText guards the match above against a negated
	// phrasing, so a status that denies restorability can never be read as
	// vouching for it.
	fdbNotRestorableStatusText = " is not restorable"
)

// fdbRestorablePointFromStatus extracts how far a FoundationDB continuous
// backup can restore to, from `fdbbackup status` output. The timestamp stops
// advancing as soon as the backup agent stops draining logs, which is the
// failure this metric exists to surface.
func fdbRestorablePointFromStatus(ctx context.Context, status string) (time.Time, error) {
	lowered := strings.ToLower(status)
	if !strings.Contains(lowered, fdbRestorableStatusText) ||
		strings.Contains(lowered, fdbNotRestorableStatusText) {
		return time.Time{}, fmt.Errorf("fdbbackup status reports no restorable backup")
	}
	for line := range strings.SplitSeq(status, "\n") {
		if strings.Contains(line, fdbLastCompleteLogLabel) {
			return fdbLogTimestampFromLine(ctx, line)
		}
	}
	return time.Time{}, fmt.Errorf("fdbbackup status has no %q line", fdbLastCompleteLogLabel)
}

// fdbLogTimestampFromLine parses the timestamp out of one status detail line,
// which reads "<label>   - <version>, <timestamp>". A line that does not carry
// a parseable timestamp is logged here, because the caller only reports that
// the restorable point is unknown.
func fdbLogTimestampFromLine(ctx context.Context, line string) (time.Time, error) {
	logger := telemetry.L(ctx)
	trimmed := strings.TrimSpace(line)
	_, value, found := strings.Cut(line, " - ")
	if !found {
		shapeErr := fmt.Errorf("fdbbackup status line %q has no value", trimmed)
		logger.WarnContext(ctx, "backup.staleness.fdb_status_unparseable", slog.String("err", shapeErr.Error()))
		return time.Time{}, shapeErr
	}
	_, stamp, found := strings.Cut(value, ",")
	if !found {
		shapeErr := fmt.Errorf("fdbbackup status line %q has no timestamp", trimmed)
		logger.WarnContext(ctx, "backup.staleness.fdb_status_unparseable", slog.String("err", shapeErr.Error()))
		return time.Time{}, shapeErr
	}
	at, err := time.Parse(fdbStatusTimestampLayout, strings.TrimSpace(stamp))
	if err != nil {
		wrapped := fmt.Errorf("parse fdbbackup timestamp %q: %w", strings.TrimSpace(stamp), err)
		logger.WarnContext(ctx, "backup.staleness.fdb_status_unparseable", slog.String("err", wrapped.Error()))
		return time.Time{}, wrapped
	}
	return at, nil
}
