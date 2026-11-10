package integration_test

import (
	"expvar"
	"net/http"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// droppedAuditMapName is the expvar map that counts audit events dropped
	// by stage.
	droppedAuditMapName = "tack_audit_dropped_total"
	// durableAdmissionDropKey is the key that counts auth.token_used events
	// with a failed outbox append.
	durableAdmissionDropKey = "auth.token_used:durable_admission"
	// tokenUseStorageRecovery is how long the test waits for a request to
	// succeed after FoundationDB resumes.
	tokenUseStorageRecovery = 60 * time.Second
)

// TestRuntimeAuditTokenUseRefusedWhenOutboxAppendFails authenticates with a
// real API token on a server that appends auth.token_used events to the
// FoundationDB outbox. The test pauses the FoundationDB container and the
// append fails. The request must receive HTTP 503 with the audit unavailable
// body, and the durable admission drop counter must increase. After
// FoundationDB resumes, a request must succeed and the outbox must contain its
// auth.token_used event.
func TestRuntimeAuditTokenUseRefusedWhenOutboxAppendFails(t *testing.T) {
	stack := startRuntimeAuditStack(t)

	t.Setenv("ENV", "production")

	cfg := publicConfig(t)
	cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic = testenv.Kafka(t), stack.topic
	cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", false
	cfg.AuditReadFlushInterval = tokenUseNeverFlushInterval
	server := buildPublicServer(t, cfg, nil)
	container := testenv.FoundationDBContainer(server.clusterFile)
	dropped := droppedAuditEvents(t)

	setContainerPaused(t, container, true)

	refusedID := newRequestID(t, "token-use-storage-refused")
	refused := server.rpc(t, refusedID, "tools/list", map[string]any{})

	setContainerPaused(t, container, false)

	if refused.status != http.StatusServiceUnavailable || string(refused.body) != auditUnavailableResponse {
		t.Fatalf("response = %d %s, want 503 %s", refused.status, refused.body, auditUnavailableResponse)
	}

	if after := droppedAuditEvents(t); after <= dropped {
		t.Fatalf("%s count = %d after the failed append, want more than %d",
			durableAdmissionDropKey, after, dropped)
	}

	deadline := clock.Now().Add(tokenUseStorageRecovery)

	for {
		recoveredID := newRequestID(t, "token-use-storage-recovered")

		response := server.rpc(t, recoveredID, "tools/list", map[string]any{})
		if response.status == http.StatusOK {
			outboxTokenUsed(t, server, recoveredID)

			return
		}

		if clock.Now().After(deadline) {
			t.Fatalf("tools/list status = %d after FoundationDB resumed, want 200 within %s: %s",
				response.status, tokenUseStorageRecovery, response.body)
		}

		time.Sleep(time.Second)
	}
}

// droppedAuditEvents returns the count of auth.token_used events dropped at the
// durable admission stage in this process.
func droppedAuditEvents(t *testing.T) int64 {
	t.Helper()

	dropped, published := expvar.Get(droppedAuditMapName).(*expvar.Map)
	if !published {
		t.Fatalf("the %s counter is not published", droppedAuditMapName)
	}

	count, counted := dropped.Get(durableAdmissionDropKey).(*expvar.Int)
	if !counted {
		return 0
	}

	return count.Value()
}
