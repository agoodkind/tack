// Live probes read YugabyteDB replication health and the FoundationDB backup's
// most recent restorable point independently of object-store records.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybMasterHealthPort replaces the configured RPC port for HTTP health probes.
// Tests substitute a local listener's port.
var ybMasterHealthPort = "7000"

const (
	// ybMasterHealthPath returns cluster health from the leader master.
	// yugabytedb/yugabyte:2025.2.3.0-b149 returns HTTP 200 with
	// {"dead_nodes":[],"most_recent_uptime":0,"under_replicated_tablets":[]}.
	ybMasterHealthPath = "/api/v1/health-check"
	// ybMasterHealthTimeout bounds each master's probe.
	ybMasterHealthTimeout = 10 * time.Second
	// ybMasterHealthMaxBytes limits responses that list under-replicated tablets.
	// Truncated JSON must fail decoding rather than establish healthy status.
	ybMasterHealthMaxBytes = 4 << 20
)

// ybMasterHealthCheck requires both health lists. Pointer fields distinguish
// absent fields from empty lists; incomplete payloads cannot establish health.
type ybMasterHealthCheck struct {
	DeadNodes              *[]string `json:"dead_nodes"`
	UnderReplicatedTablets *[]string `json:"under_replicated_tablets"`
}

// probeYBClusterHealth returns the first usable master response. Followers
// provide leader links instead of health payloads and require another probe.
// Missing responses establish observation failure, not cluster degradation
// (TACK-529). Healthy detail becomes marker detail; other detail explains the
// report's result. Only journal logs include per-master client errors.
func probeYBClusterHealth(ctx context.Context, cfg *config.Config) (ybClusterObservation, string) {
	urls := ybMasterHealthURLs(cfg.BackupYBMasterAddresses)
	if len(urls) == 0 {
		return ybClusterUnseen, "TACK_BACKUP_YB_MASTER_ADDRESSES names no master to probe"
	}

	var failures []string

	for _, url := range urls {
		body, err := fetchYBMasterHealth(ctx, url)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}

		if ybMasterAnswersAsFollower(body) {
			telemetry.L(ctx).DebugContext(
				ctx,
				"backup.staleness.master_follower",
				slog.String("url", url),
			)
			failures = append(failures, url+" answered as a follower")
			continue
		}

		replicated, healthDetail, parseErr := ybClusterHealthFromBody(ctx, body)
		if parseErr != nil {
			failures = append(failures, parseErr.Error())
			continue
		}

		if replicated {
			return ybClusterHealthy, healthDetail
		}

		return ybClusterDegraded, healthDetail
	}

	// Journal logs include master addresses. Report and email detail exclude
	// those client errors and explain the observation failure instead.
	telemetry.L(ctx).WarnContext(
		ctx,
		"backup.staleness.masters_unanswered",
		slog.Any("failures", failures),
	)

	return ybClusterUnseen, "no master answered the health check"
}

// ybMasterHealthURLs replaces RPC ports with the admin port and brackets IPv6
// literals in health-check URLs.
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

// fetchYBMasterHealth checks HTTP status and reads the JSON response body.
// The endpoint uses text/plain, not a JSON content type. Logged probe failures
// permit the caller to try another master.
func fetchYBMasterHealth(ctx context.Context, url string) ([]byte, error) {
	logger := telemetry.L(ctx)
	reqCtx, cancel := context.WithTimeout(ctx, ybMasterHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		wrapped := fmt.Errorf("build health-check request for %s: %w", url, err)
		logger.WarnContext(
			ctx,
			"backup.staleness.master_unreachable",
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("get %s: %w", url, err)
		logger.WarnContext(
			ctx,
			"backup.staleness.master_unreachable",
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("get %s: status %d", url, resp.StatusCode)
		logger.WarnContext(
			ctx,
			"backup.staleness.master_unreachable",
			slog.String("err", statusErr.Error()),
		)

		return nil, statusErr
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ybMasterHealthMaxBytes))
	if err != nil {
		wrapped := fmt.Errorf("read %s: %w", url, err)
		logger.WarnContext(
			ctx,
			"backup.staleness.master_unreachable",
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	return body, nil
}

// ybClusterHealthFromBody requires every node alive and every tablet fully
// replicated. Missing health lists return an error instead of a healthy
// verdict.
func ybClusterHealthFromBody(
	ctx context.Context,
	body []byte,
) (healthy bool, detail string, err error) {
	logger := telemetry.L(ctx)
	var payload ybMasterHealthCheck
	if err := json.Unmarshal(body, &payload); err != nil {
		wrapped := fmt.Errorf("unmarshal master health check: %w", err)
		logger.WarnContext(
			ctx,
			"backup.staleness.health_unparseable",
			slog.String("err", wrapped.Error()),
		)

		return false, "", wrapped
	}

	if payload.DeadNodes == nil || payload.UnderReplicatedTablets == nil {
		missingErr := fmt.Errorf("master health check omits dead_nodes or under_replicated_tablets")
		logger.WarnContext(
			ctx,
			"backup.staleness.health_unparseable",
			slog.String("err", missingErr.Error()),
		)

		return false, "", missingErr
	}

	deadCount := len(*payload.DeadNodes)
	underReplicatedCount := len(*payload.UnderReplicatedTablets)
	detail = fmt.Sprintf(
		"%d dead nodes, %d under-replicated tablets",
		deadCount,
		underReplicatedCount,
	)

	return deadCount == 0 && underReplicatedCount == 0, detail, nil
}

const (
	// fdbLastCompleteLogLabel identifies the latest restorable mutation log
	// in FoundationDB 7.4.6 status output, for example:
	//   Last complete log version and timestamp        - 100720665, 2026/08/30.01:07:23+0000
	fdbLastCompleteLogLabel = "Last complete log version and timestamp"
	// fdbStatusTimestampLayout parses timestamps in that output.
	fdbStatusTimestampLayout = "2006/01/02.15:04:05-0700"
	// fdbRestorableStatusText matches the complete restorability predicate.
	// FoundationDB 7.4.6 reports "is restorable but continuing to <url>"
	// after the initial snapshot and "is in progress to <url>" before it.
	// Matching only "restorable" would also accept tag names and URLs.
	fdbRestorableStatusText = " is restorable"
	// fdbNotRestorableStatusText excludes explicitly negated restorability.
	fdbNotRestorableStatusText = " is not restorable"
)

// errFDBNoRestorablePoint distinguishes confirmed absence of a restorable
// point from unreadable status.
var errFDBNoRestorablePoint = errors.New("fdbbackup status does not report a restorable backup")

// fdbRestorablePointFromStatus extracts restorable time and version from
// fdbbackup status. The version permits restore after the source cluster can
// no longer convert that time (TACK-468). Log timestamps stop advancing when
// the backup agent stops processing mutation logs.
func fdbRestorablePointFromStatus(ctx context.Context, status string) (fdbRestorablePoint, error) {
	var none fdbRestorablePoint
	lowered := strings.ToLower(status)
	if !strings.Contains(lowered, fdbRestorableStatusText) ||
		strings.Contains(lowered, fdbNotRestorableStatusText) {
		return none, errFDBNoRestorablePoint
	}

	for line := range strings.SplitSeq(status, "\n") {
		if strings.Contains(line, fdbLastCompleteLogLabel) {
			return fdbRestorablePointFromLine(ctx, line)
		}
	}

	return none, fmt.Errorf("fdbbackup status has no %q line", fdbLastCompleteLogLabel)
}

// fdbRestorablePointFromLine parses "<label> - <version>, <timestamp>" and
// logs malformed values. The caller reports the restorable point as unknown.
func fdbRestorablePointFromLine(ctx context.Context, line string) (fdbRestorablePoint, error) {
	logger := telemetry.L(ctx)
	var none fdbRestorablePoint
	trimmed := strings.TrimSpace(line)
	_, value, found := strings.Cut(line, " - ")
	if !found {
		shapeErr := fmt.Errorf("fdbbackup status line %q has no value", trimmed)
		logger.WarnContext(
			ctx,
			"backup.staleness.fdb_status_unparseable",
			slog.String("err", shapeErr.Error()),
		)

		return none, shapeErr
	}

	rawVersion, stamp, found := strings.Cut(value, ",")
	if !found {
		shapeErr := fmt.Errorf("fdbbackup status line %q has no timestamp", trimmed)
		logger.WarnContext(
			ctx,
			"backup.staleness.fdb_status_unparseable",
			slog.String("err", shapeErr.Error()),
		)

		return none, shapeErr
	}

	version, err := strconv.ParseInt(strings.TrimSpace(rawVersion), 10, 64)
	if err != nil {
		wrapped := fmt.Errorf("parse fdbbackup version %q: %w", strings.TrimSpace(rawVersion), err)
		logger.WarnContext(
			ctx,
			"backup.staleness.fdb_status_unparseable",
			slog.String("err", wrapped.Error()),
		)

		return none, wrapped
	}

	at, err := time.Parse(fdbStatusTimestampLayout, strings.TrimSpace(stamp))
	if err != nil {
		wrapped := fmt.Errorf("parse fdbbackup timestamp %q: %w", strings.TrimSpace(stamp), err)
		logger.WarnContext(
			ctx,
			"backup.staleness.fdb_status_unparseable",
			slog.String("err", wrapped.Error()),
		)

		return none, wrapped
	}

	return fdbRestorablePoint{Version: version, At: at.UTC()}, nil
}
