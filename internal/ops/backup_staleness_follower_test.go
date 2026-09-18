// backup_staleness_follower_test.go proves the cluster health probe passes
// over follower masters quietly and still reads the leader's answer, over real
// HTTP on the IPv6 loopback.

package ops

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"goodkind.io/tack/internal/config"
)

// ybFollowerHealthPage is the body a follower master served on production for
// the health check on 2026-09-13, cut after the leader's link.
const ybFollowerHealthPage = `Error retrieving leader master URL: <a href="http://yb1:7000/api/v1/health-check?raw">http://yb1:7000/api/v1/health-check?raw</a><br> Error: Network error (yb/util/curl_util.cc:57): curl error: Could not resolve hostname.<br>`

// captureOpsLogs routes the default logger into a buffer for the test, at
// debug level, so a test can count the warnings a run emits.
func captureOpsLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil,
	})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

// startYBMasterHealthServer serves the health path on the IPv6 loopback, with
// each request answered by the next body in bodies (the last body repeats).
// The probe's admin port is pointed at the listener for the test's duration,
// so every configured master address reaches this server.
func startYBMasterHealthServer(t *testing.T, bodies ...string) {
	t.Helper()
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback listener: %v", err)
	}
	var served atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ybMasterHealthPath {
			http.NotFound(w, r)
			return
		}
		index := min(int(served.Add(1))-1, len(bodies)-1)
		_, _ = w.Write([]byte(bodies[index]))
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	previousPort := ybMasterHealthPort
	ybMasterHealthPort = port
	t.Cleanup(func() { ybMasterHealthPort = previousPort })
}

// TestYBMasterAnswersAsFollower pins the page a follower serves against the
// answers that must still count as payloads to read.
func TestYBMasterAnswersAsFollower(t *testing.T) {
	if !ybMasterAnswersAsFollower([]byte(ybFollowerHealthPage)) {
		t.Fatal("the production follower page must read as a follower answer")
	}
	if !ybMasterAnswersAsFollower([]byte("\n  " + ybFollowerHealthPage)) {
		t.Fatal("leading whitespace must not hide a follower page")
	}
	for _, body := range []string{ybHealthAllGood, "not json", `{"dead_nodes":[]}`, ""} {
		if ybMasterAnswersAsFollower([]byte(body)) {
			t.Errorf("%q must not read as a follower page", body)
		}
	}
}

// TestProbeYBClusterHealthPassesOverFollowersQuietly is the production shape:
// two followers answer with the page before the leader answers with the
// payload. The probe must read the leader's counts and log the followers at
// debug, with no warning.
func TestProbeYBClusterHealthPassesOverFollowersQuietly(t *testing.T) {
	logs := captureOpsLogs(t)
	startYBMasterHealthServer(t, ybFollowerHealthPage, ybFollowerHealthPage, ybHealthAllGood)
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100,[::1]:7100,[::1]:7100"}

	healthy, detail := probeYBClusterHealth(context.Background(), cfg)

	if !healthy {
		t.Fatalf("healthy = false, detail %q; the leader's payload must be read", detail)
	}
	if detail != "0 dead nodes, 0 under-replicated tablets" {
		t.Fatalf("detail = %q", detail)
	}
	if strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Fatalf("a follower's page must not warn:\n%s", logs.String())
	}
	if strings.Count(logs.String(), "backup.staleness.master_follower") != 2 {
		t.Fatalf("each follower must be logged once at debug:\n%s", logs.String())
	}
}

// TestProbeYBClusterHealthReportsWhenEveryMasterIsAFollower proves a cluster
// with no reachable leader still fails the probe with a reason that names it.
func TestProbeYBClusterHealthReportsWhenEveryMasterIsAFollower(t *testing.T) {
	captureOpsLogs(t)
	startYBMasterHealthServer(t, ybFollowerHealthPage)
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100,[::1]:7100"}

	healthy, detail := probeYBClusterHealth(context.Background(), cfg)

	if healthy {
		t.Fatal("no leader answered, so the cluster must not read as healthy")
	}
	if !strings.Contains(detail, "no master answered the health check") || !strings.Contains(detail, "answered as a follower") {
		t.Fatalf("detail = %q", detail)
	}
}

// TestProbeYBClusterHealthStillWarnsOnAnUnreadablePayload keeps the warning
// for a body that is neither the payload nor a follower's page.
func TestProbeYBClusterHealthStillWarnsOnAnUnreadablePayload(t *testing.T) {
	logs := captureOpsLogs(t)
	startYBMasterHealthServer(t, "<html>maintenance</html>")
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100"}

	healthy, _ := probeYBClusterHealth(context.Background(), cfg)

	if healthy {
		t.Fatal("an unreadable payload must not read as healthy")
	}
	if !strings.Contains(logs.String(), "backup.staleness.health_unparseable") {
		t.Fatalf("an unreadable payload must still warn:\n%s", logs.String())
	}
}
