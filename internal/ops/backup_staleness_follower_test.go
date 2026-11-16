// Master-health tests use real IPv6-loopback HTTP to distinguish follower
// pages, usable leader payloads, and malformed responses.

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

// ybFollowerHealthPage preserves a production follower response from
// 2026-09-13, including its leader URL and client diagnostic.
const ybFollowerHealthPage = `Error retrieving leader master URL: <a href="http://yb1:7000/api/v1/health-check?raw">http://yb1:7000/api/v1/health-check?raw</a><br> Error: Network error (yb/util/curl_util.cc:57): curl error: Could not resolve hostname.<br>`

// captureOpsLogs captures default logger output at debug level for warning
// and follower-event assertions.
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

// startYBMasterHealthServer serves ordered responses, repeating the last, on
// IPv6 loopback. A substituted admin port directs configured masters to it.
func startYBMasterHealthServer(t *testing.T, bodies ...string) {
	t.Helper()
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback listener is unavailable: %v", err)
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

// TestYBMasterAnswersAsFollower checks exact production follower recognition
// without misclassifying payloads, malformed JSON, or empty responses.
func TestYBMasterAnswersAsFollower(t *testing.T) {
	if !ybMasterAnswersAsFollower([]byte(ybFollowerHealthPage)) {
		t.Fatal("the production follower page must classify as a follower response")
	}

	if !ybMasterAnswersAsFollower([]byte("\n  " + ybFollowerHealthPage)) {
		t.Fatal("leading whitespace must not prevent follower recognition")
	}

	for _, body := range []string{ybHealthAllGood, "not json", `{"dead_nodes":[]}`, ""} {
		if ybMasterAnswersAsFollower([]byte(body)) {
			t.Errorf("%q must not classify as a follower page", body)
		}
	}
}

// TestProbeYBClusterHealthLogsFollowersAtDebug checks the leader's healthy
// counts after two follower responses, with debug events instead of warnings.
func TestProbeYBClusterHealthLogsFollowersAtDebug(t *testing.T) {
	logs := captureOpsLogs(t)
	startYBMasterHealthServer(t, ybFollowerHealthPage, ybFollowerHealthPage, ybHealthAllGood)
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100,[::1]:7100,[::1]:7100"}

	observation, detail := probeYBClusterHealth(context.Background(), cfg)

	if observation != ybClusterHealthy {
		t.Fatalf(
			"observation = %d, detail %q; expected the leader's healthy payload",
			observation,
			detail,
		)
	}

	if detail != "0 dead nodes, 0 under-replicated tablets" {
		t.Fatalf("detail = %q", detail)
	}

	if strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Fatalf("follower responses must not produce warnings:\n%s", logs.String())
	}

	if strings.Count(logs.String(), "backup.staleness.master_follower") != 2 {
		t.Fatalf("each follower must produce one debug event:\n%s", logs.String())
	}
}

// TestProbeYBClusterHealthReportsWhenEveryMasterIsAFollower checks unavailable
// leader health with per-master logs and address-free public detail.
func TestProbeYBClusterHealthReportsWhenEveryMasterIsAFollower(t *testing.T) {
	logs := captureOpsLogs(t)
	startYBMasterHealthServer(t, ybFollowerHealthPage)
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100,[::1]:7100"}

	observation, detail := probeYBClusterHealth(context.Background(), cfg)

	if observation != ybClusterUnseen {
		t.Fatalf("unusable follower responses must classify as unseen, got %d", observation)
	}

	if detail != "no master answered the health check" {
		t.Fatalf("detail = %q", detail)
	}

	if !strings.Contains(logs.String(), "backup.staleness.masters_unanswered") ||
		!strings.Contains(logs.String(), "answered as a follower") {
		t.Fatalf("logs must record each master response:\n%s", logs.String())
	}
}

// TestProbeYBClusterHealthWarnsOnMalformedPayload checks warning behavior for
// an unreadable body distinct from a recognized follower response.
func TestProbeYBClusterHealthWarnsOnMalformedPayload(t *testing.T) {
	logs := captureOpsLogs(t)
	startYBMasterHealthServer(t, "<html>maintenance</html>")
	cfg := &config.Config{BackupYBMasterAddresses: "[::1]:7100"}

	observation, _ := probeYBClusterHealth(context.Background(), cfg)

	if observation != ybClusterUnseen {
		t.Fatalf("a malformed payload cannot establish health, got observation %d", observation)
	}

	if !strings.Contains(logs.String(), "backup.staleness.health_unparseable") {
		t.Fatalf("malformed health payloads must produce warnings:\n%s", logs.String())
	}
}
