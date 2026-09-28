package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/telemetry"
)

// openSearchProbeTimeout bounds one readiness request.
const openSearchProbeTimeout = 10 * time.Second

// waitForOpenSearchHealth polls cluster health with the fixture's credentials
// and the production client settings until the engine returns HTTP 200.
// The security plugin rejects requests until its index exists. An HTTP 200
// requires completed TLS, authentication, and security initialization.
func waitForOpenSearchHealth(ctx context.Context, fixture OpenSearchFixture) error {
	pass := fixture.Password
	configuration := search.Config{
		Endpoint: fixture.Endpoint, CA: fixture.CA, Username: fixture.Username,
		Password: pass, RequestTimeout: openSearchProbeTimeout, MaxRetries: 0,
	}
	client, err := opensearch.NewClient(configuration.ClientConfig(nil))
	if err != nil {
		wrapped := fmt.Errorf("create OpenSearch readiness client: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.fixture_health_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer func() { _ = client.Close() }()
	var lastFailure string
	for {
		response, err := opensearch.Do[json.RawMessage](ctx, client, http.MethodGet,
			opensearchapi.ClusterHealthReq{Indices: nil, Header: nil, Params: opensearchapi.ClusterHealthParams{}}, nil)
		switch {
		case err != nil:
			lastFailure = err.Error()
		case response.StatusCode == http.StatusOK:
			return nil
		default:
			lastFailure = fmt.Sprintf("status %d", response.StatusCode)
		}
		if !sleepOrDone(ctx) {
			wrapped := fmt.Errorf("wait for authenticated OpenSearch health at %s: %s: %w", fixture.Endpoint, lastFailure, ctx.Err())
			telemetry.L(ctx).ErrorContext(ctx, "search.fixture_health_failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
}
