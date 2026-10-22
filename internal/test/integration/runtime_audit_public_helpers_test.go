package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"expvar"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/testenv"
)

// publicServer serves the MCP, health, diagnostics, and unknown routes with the production request recorder.
// It uses the runtime graph and authenticates MCP requests with the production authentication middleware.
type publicServer struct {
	handler     http.Handler
	config      *config.Config
	graph       *appruntime.Graph
	token       string
	userID      uuid.UUID
	orgID       uuid.UUID
	workspace   string
	clusterFile string
}

// publicResponse contains the status, headers, and exact body of one response.
type publicResponse struct {
	status int
	header http.Header
	body   []byte
}

// startPublicServer builds the runtime graph with a new FoundationDB key prefix and
// bootstraps test identities.
// It registers the HTTP routes with a recorder that saves read events using the store callback's result.
// When store is nil, the recorder uses graph.ProductReads.
func startPublicServer(t *testing.T, store func(*appruntime.Graph) audit.ReadLifecycle) *publicServer {
	t.Helper()
	ctx := t.Context()
	clusterFile := testenv.FoundationDB(t)
	identity := uuid.New()
	prefix := append([]byte("tack-test:"), identity[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
	t.Setenv("DATABASE_URL", testenv.Ledger(t))
	t.Setenv("FDB_CLUSTER_FILE", clusterFile)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	cfg.AuditKafkaBrokers, cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", "", true
	graph, err := appruntime.BuildGraph(ctx, cfg)
	if err != nil {
		t.Fatalf("appruntime.BuildGraph failed: %v", err)
	}
	t.Cleanup(graph.Close)
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("datagen.ParseScale failed: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(ctx, cfg, clock.Now().UnixNano(), scale)
	if err != nil {
		t.Fatalf("datagen.BootstrapIdentities failed: %v", err)
	}
	readStore := graph.ProductReads
	if store != nil {
		readStore = store(graph)
	}
	recorder := mcpadapter.NewRequestRecorder(readStore)
	mux := http.NewServeMux()
	mux.Handle("/mcp", recorder.MCP(graph.AuthMiddleware, graph.MCPHandler))
	mux.Handle("GET /healthz", recorder.Health(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := graph.PingFoundationDB(request.Context()); err != nil {
			http.Error(writer, "fdb unhealthy", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte("ok"))
	})))
	mux.Handle("/debug/vars", recorder.Diagnostics(expvar.Handler()))
	mux.Handle("/", recorder.UnknownRoute())
	workspace := identities.Workspaces[0]
	return &publicServer{
		handler: telemetry.RequestLogger(mux), config: cfg, graph: graph,
		token: workspace.Actors[0].Token, userID: workspace.Actors[0].UserID, orgID: workspace.OrgID,
		workspace: workspace.Slug, clusterFile: clusterFile,
	}
}

// send sends one request with requestID and an optional bearer value and
// returns the exact response.
func (server *publicServer) send(t *testing.T, method, path, bearer, requestID string, body []byte) publicResponse {
	t.Helper()
	request := httptest.NewRequestWithContext(context.WithoutCancel(t.Context()), method, path, bytes.NewReader(body))
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	server.handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("io.ReadAll failed for the %s %s response: %v", method, path, err)
	}
	return publicResponse{status: response.StatusCode, header: response.Header, body: payload}
}

// rpc sends one authenticated JSON-RPC request to the MCP route.
func (server *publicServer) rpc(t *testing.T, requestID, method string, params map[string]any) publicResponse {
	t.Helper()
	return server.send(t, http.MethodPost, "/mcp", server.token, requestID, rpcBody(t, method, params))
}

// rpcBody encodes one JSON-RPC request.
func rpcBody(t *testing.T, method string, params map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": uuid.NewString(), "method": method, "params": params})
	if err != nil {
		t.Fatalf("json.Marshal failed for the %s request: %v", method, err)
	}
	return body
}

// payloadDigest returns the lowercase hexadecimal SHA-256 digest of body.
func payloadDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// newRequestID returns a unique request ID for one test request.
func newRequestID(t *testing.T, label string) string {
	t.Helper()
	return "runtime-audit-" + label + "-" + uuid.NewString()
}

// publicDeliveryTimeout limits how long publicLedger.row waits for a read event in the ledger.
const publicDeliveryTimeout = 2 * time.Minute
