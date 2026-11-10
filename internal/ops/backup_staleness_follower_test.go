// Cluster health tests use real HTTP on the IPv6 loopback to check that the
// probe skips follower pages without warnings and reads the leader's response.

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

// ybFollowerHealthPage preserves the health-check response that the original
// fixture recorded from a production follower on 2026-09-13, with the capture
// ending after the leader's link.
const ybFollowerHealthPage = `Error retrieving leader master URL: <a href="http://yb1:7000/api/v1/health-check?raw">http://yb1:7000/api/v1/health-check?raw</a><br> Error: Network error (yb/util/curl_util.cc:57): curl error: Could not resolve hostname.<br>`

// captureOpsLogs records the default logger's output in a buffer at debug
// level. Tests inspect the records for warnings; cleanup restores the logger.
func captureOpsLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(
		slog.New(
			slog.NewJSONHandler(
				&buffer,
				&slog.HandlerOptions{
					AddSource:   false,
					Level:       slog.LevelDebug,
					ReplaceAttr: nil,
				},
			),
		),
	)
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &buffer
}

// startYBMasterHealthServer serves the health path on the IPv6 loopback.
// Requests receive bodies in order, and later requests receive the last body.
// The helper temporarily sets the probe's admin port to this listener's port.
// The configured loopback master addresses all use this server.
func startYBMasterHealthServer(t *testing.T, bodies ...string) {
	t.Helper()
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback listener: %v", err)
	}

	var served atomic.Int64
	server := httptest.NewUnstartedServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != ybMasterHealthPath {
				http.NotFound(w, r)
				return
			}

			index := min(int(served.Add(1))-1, len(bodies)-1)
			_, _ = w.Write([]byte(bodies[index]))
		}),
	)
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

// TestYBMasterAnswersAsFollower checks the captured follower page with and
// without leading whitespace. It rejects healthy JSON, partial JSON, malformed
// text, and an empty body as follower pages.
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

// TestProbeYBClusterHealthPassesOverFollowersQuietly models two follower
// responses before the leader response. It checks the leader's healthy counts,
// two follower log records, and the absence of warnings. The probe emits the
// follower records at debug level.
func TestProbeYBClusterHealthPassesOverFollowersQuietly(t *testing.T) {
	logs := captureOpsLogs(t)
	startYBMasterHealthServer(t, ybFollowerHealthPage, ybFollowerHealthPage, ybHealthAllGood)
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100,[::1]:7100,[::1]:7100"}

	observation, detail := probeYBClusterHealth(context.Background(), cfg)

	if observation != ybClusterHealthy {
		t.Fatalf(
			"observation = %d, detail %q; the leader's payload must be read",
			observation,
			detail,
		)
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

// TestProbeYBClusterHealthReportsWhenEveryMasterIsAFollower checks the unseen
// result when every master returns a follower page. It checks the general
// reason and the journal's failure records. The probe logs master addresses in
// the journal and excludes them from the detail that reports and mail use.
func TestProbeYBClusterHealthReportsWhenEveryMasterIsAFollower(t *testing.T) {
	logs := captureOpsLogs(t)
	startYBMasterHealthServer(t, ybFollowerHealthPage)
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100,[::1]:7100"}

	observation, detail := probeYBClusterHealth(context.Background(), cfg)

	if observation != ybClusterUnseen {
		t.Fatalf("no leader answered, so this guest saw nothing, got observation %d", observation)
	}

	if detail != "no master answered the health check" {
		t.Fatalf("detail = %q", detail)
	}

	if !strings.Contains(logs.String(), "backup.staleness.masters_unanswered") ||
		!strings.Contains(logs.String(), "answered as a follower") {
		t.Fatalf("each master's answer must be logged:\n%s", logs.String())
	}
}

// TestProbeYBClusterHealthStillWarnsOnAnUnreadablePayload checks the unseen
// result and the unparseable log record for a body that is neither a health
// payload nor a follower page. The parser emits that record as a warning.
func TestProbeYBClusterHealthStillWarnsOnAnUnreadablePayload(t *testing.T) {
	logs := captureOpsLogs(t)
	startYBMasterHealthServer(t, "<html>maintenance</html>")
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100"}

	observation, _ := probeYBClusterHealth(context.Background(), cfg)

	if observation != ybClusterUnseen {
		t.Fatalf("an unreadable payload establishes nothing, got observation %d", observation)
	}

	if !strings.Contains(logs.String(), "backup.staleness.health_unparseable") {
		t.Fatalf("an unreadable payload must still warn:\n%s", logs.String())
	}
}
