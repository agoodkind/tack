// The staleness check reads current replication health from the YugabyteDB
// masters and the latest restorable point from the FoundationDB continuous
// backup. Object-store records cannot establish those live readings.

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

// ybMasterHealthPort selects the master's HTTP admin port instead of the
// configured RPC port (7100). Tests can set this package variable to their
// listener's port.
var ybMasterHealthPort = "7000"

const (
	// ybMasterHealthPath selects the master's cluster health endpoint. The
	// original verification used yugabytedb/yugabyte:2025.2.3.0-b149 and recorded
	// HTTP 200 with this response:
	// {"dead_nodes":[],"most_recent_uptime":0,"under_replicated_tablets":[]}.
	ybMasterHealthPath = "/api/v1/health-check"
	// ybMasterHealthTimeout limits each master's request duration when the
	// host does not answer.
	ybMasterHealthTimeout = 10 * time.Second
	// ybMasterHealthMaxBytes limits the response read, including the list of
	// under-replicated tablets that can grow with the cluster. A cutoff inside
	// JSON causes a decode error. The probe skips that answer and returns unseen
	// if every master fails; it does not certify the cluster as healthy.
	ybMasterHealthMaxBytes = 4 << 20
)

// ybMasterHealthCheck contains the master's health-check payload. Pointer
// fields distinguish absent lists from empty lists. The parser requires both
// lists before interpreting empty arrays as healthy; HTTP 200 alone cannot
// establish cluster health.
type ybMasterHealthCheck struct {
	DeadNodes              *[]string `json:"dead_nodes"`
	UnderReplicatedTablets *[]string `json:"under_replicated_tablets"`
}

// probeYBClusterHealth requests each configured master in order and returns
// the first usable answer. The leader supplies cluster health. A follower page
// identifies the leader; the probe logs that response at debug level and asks
// the next master.
//
// TACK-529 distinguishes an observed cluster state from this guest's inability
// to obtain a usable answer. A master's payload establishes replication
// health; unanswered requests cannot establish that state. The returned detail
// becomes the healthy marker's detail or the report's failure reason. Client
// errors appear in the journal rather than in that detail.
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
			telemetry.L(ctx).DebugContext(ctx, "backup.staleness.master_follower", slog.String("url", url))
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
	// The journal records each master's failure and address. Reports and mail
	// use the general failure reason below.
	telemetry.L(ctx).WarnContext(ctx, "backup.staleness.masters_unanswered",
		slog.Any("failures", failures))
	return ybClusterUnseen, "no master answered the health check"
}

// ybMasterHealthURLs converts comma-separated master addresses into
// health-check URLs. It replaces each RPC port with the admin port and uses
// brackets around IPv6 literals.
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

// fetchYBMasterHealth reads one master's health-check response. The endpoint
// returns JSON with a text/plain content type; this function checks HTTP status
// without requiring a JSON content type. It logs request failures and returns
// them to the caller, which can try another master for the cluster's health.
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

// ybClusterHealthFromBody reports healthy only when the payload lists
// zero dead nodes and zero under-replicated tablets. It returns an error if
// either list is absent.
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
	// fdbLastCompleteLogLabel identifies the latest complete mutation log
	// version and timestamp in `fdbbackup status` output. The restore uses that
	// point as its latest log boundary. The original capture used FoundationDB
	// 7.4.6:
	//   Last complete log version and timestamp        - 100720665, 2026/08/30.01:07:23+0000
	fdbLastCompleteLogLabel = "Last complete log version and timestamp"
	// fdbStatusTimestampLayout parses timestamps from the status output.
	fdbStatusTimestampLayout = "2006/01/02.15:04:05-0700"
	// fdbRestorableStatusText matches the phrase that confirms a restorable
	// point. In the original FoundationDB 7.4.6 observation, a continuous backup
	// reported "The backup on tag `default' is restorable but continuing to <url>".
	// Before the first snapshot finished, it reported "is in progress to <url>"
	// and did not yet provide a restorable point.
	//
	// The match includes the leading verb. The word "restorable" alone can also
	// appear in URLs or tag names and falsely classify an advancing log timestamp
	// as a fresh restorable point before the snapshot finishes.
	fdbRestorableStatusText = " is restorable"
	// fdbNotRestorableStatusText rejects a status that explicitly denies
	// restorability, even if another part of the text matches the positive phrase.
	fdbNotRestorableStatusText = " is not restorable"
)

// errFDBNoRestorablePoint reports that the status does not confirm a
// restorable point. It differs from an error parsing a restorable status.
var errFDBNoRestorablePoint = errors.New("fdbbackup status reports no restorable backup")

// fdbRestorablePointFromStatus extracts the restorable timestamp and version
// from `fdbbackup status`. A restore needs the version after the source cluster
// can no longer convert that timestamp (TACK-468). The metric detects a backup
// agent that stops draining logs by checking whether the complete-log timestamp
// has stopped advancing.
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

// fdbRestorablePointFromLine parses a status detail line in the form
// "<label>   - <version>, <timestamp>". It logs missing components here; the
// caller reports that the restorable point is unknown.
func fdbRestorablePointFromLine(ctx context.Context, line string) (fdbRestorablePoint, error) {
	logger := telemetry.L(ctx)
	var none fdbRestorablePoint
	trimmed := strings.TrimSpace(line)
	_, value, found := strings.Cut(line, " - ")
	if !found {
		shapeErr := fmt.Errorf("fdbbackup status line %q has no value", trimmed)
		logger.WarnContext(ctx, "backup.staleness.fdb_status_unparseable", slog.String("err", shapeErr.Error()))
		return none, shapeErr
	}
	rawVersion, stamp, found := strings.Cut(value, ",")
	if !found {
		shapeErr := fmt.Errorf("fdbbackup status line %q has no timestamp", trimmed)
		logger.WarnContext(ctx, "backup.staleness.fdb_status_unparseable", slog.String("err", shapeErr.Error()))
		return none, shapeErr
	}
	version, err := strconv.ParseInt(strings.TrimSpace(rawVersion), 10, 64)
	if err != nil {
		wrapped := fmt.Errorf("parse fdbbackup version %q: %w", strings.TrimSpace(rawVersion), err)
		logger.WarnContext(ctx, "backup.staleness.fdb_status_unparseable", slog.String("err", wrapped.Error()))
		return none, wrapped
	}
	at, err := time.Parse(fdbStatusTimestampLayout, strings.TrimSpace(stamp))
	if err != nil {
		wrapped := fmt.Errorf("parse fdbbackup timestamp %q: %w", strings.TrimSpace(stamp), err)
		logger.WarnContext(ctx, "backup.staleness.fdb_status_unparseable", slog.String("err", wrapped.Error()))
		return none, wrapped
	}
	return fdbRestorablePoint{Version: version, At: at.UTC()}, nil
}
