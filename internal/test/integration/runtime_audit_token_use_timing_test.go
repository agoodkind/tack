package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/testenv"
)

const (
	// tokenUseWarmRequests is the number of requests after the first one that
	// the test measures.
	tokenUseWarmRequests = 20
	// tokenUseColdLedgerReads is the most ledger reads a request with cold
	// caches may make.
	tokenUseColdLedgerReads = 1
)

// TestRuntimeAuditTokenUseRequestPath authenticates MCP requests with a real
// API token. It logs the latency of the first request and the median latency of
// the later requests. It permits at most one ledger read on the first request.
// It requires each later request to make zero ledger reads and every request to
// make zero synchronous broker produces. The latency values are measurements
// without a threshold.
func TestRuntimeAuditTokenUseRequestPath(t *testing.T) {
	stack := startRuntimeAuditStack(t)

	t.Setenv("ENV", "production")

	cfg := publicConfig(t)
	cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic = testenv.Kafka(t), stack.topic
	cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", false
	server := buildPublicServer(t, cfg, nil)

	startPublicLedgerOn(t, server, stack)

	firstLatency, firstCounts := countedRPC(t, server, newRequestID(t, "token-use-first"))
	if firstCounts.LedgerReads > tokenUseColdLedgerReads || firstCounts.SyncProduces != 0 {
		t.Fatalf("first request counts = %+v, want at most %d ledger read and zero synchronous produces",
			firstCounts, tokenUseColdLedgerReads)
	}

	laterLatencies := make([]time.Duration, 0, tokenUseWarmRequests)

	for range tokenUseWarmRequests {
		latency, counts := countedRPC(t, server, newRequestID(t, "token-use-later"))
		laterLatencies = append(laterLatencies, latency)

		if counts.LedgerReads != 0 || counts.SyncProduces != 0 {
			t.Fatalf("later request counts = %+v, want zero ledger reads and zero synchronous produces",
				counts)
		}
	}

	slices.Sort(laterLatencies)
	t.Logf("first request %s with counts %+v, median of %d later requests %s",
		firstLatency, firstCounts, tokenUseWarmRequests, laterLatencies[len(laterLatencies)/2])
}

// countedRPC sends one authenticated tools/list request and returns its
// latency and the ledger and broker round trips completed before the response.
func countedRPC(t *testing.T, server *publicServer, requestID string) (time.Duration, telemetry.RequestPathCounts) {
	t.Helper()

	ctx, counter := telemetry.WithRequestPath(context.WithoutCancel(t.Context()))
	body := rpcBody(t, "tools/list", map[string]any{})
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+server.token)

	recorder := httptest.NewRecorder()
	startedAt := clock.Now()

	server.handler.ServeHTTP(recorder, request)

	latency := clock.Now().Sub(startedAt)

	if recorder.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want 200: %s", recorder.Code, recorder.Body.Bytes())
	}

	return latency, counter.Counts()
}
