// Package search provides the native OpenSearch control adapter.
package search

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchtransport"
	"goodkind.io/tack/internal/telemetry"
)

// Config contains the environment-backed settings for OpenSearch.
type Config struct {
	Endpoint       string
	CA             string
	Username       string
	Password       string
	RequestTimeout time.Duration
	MaxRetries     int
}

// Validate rejects incomplete or insecure native search settings.
func (c Config) Validate(ctx context.Context) error {
	parsed, err := url.Parse(c.Endpoint)
	if err != nil {
		wrapped := fmt.Errorf("parse search endpoint: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.config.endpoint_invalid", slog.String("err", wrapped.Error()))
		return loggedModelError{err: wrapped}
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("search endpoint must be one https URL")
	}
	if strings.TrimSpace(c.Username) == "" || c.Password == "" {
		return errors.New("search credentials are required")
	}
	if c.RequestTimeout <= 0 || c.MaxRetries < 0 {
		return errors.New("search timeout and retries must be valid")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(c.CA)) {
		return errors.New("search CA is invalid")
	}
	return nil
}

// retryStatuses are the response statuses the official client retries.
// OpenSearch returns 429 when the ML Commons memory circuit breaker rejects a
// request because JVM heap use exceeds its threshold.
var retryStatuses = []int{
	http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
}

// retryBackoff waits one second per attempt before each client retry.
// OpenSearch refreshes the cached JVM statistics that the memory circuit
// breaker reads once per second.
func retryBackoff(attempt int) time.Duration {
	return time.Duration(attempt) * time.Second
}

// ClientConfig returns the official client settings for this configuration.
// The client sends every request to the one configured endpoint and never
// discovers other cluster members, because the environment's proxy selects
// the OpenSearch node.
func (c Config) ClientConfig(observer opensearchtransport.ConnectionObserver) opensearch.Config {
	discoverNodesOnStart := false
	pass := c.Password
	return opensearch.Config{
		Addresses:            []string{c.Endpoint},
		Username:             c.Username,
		Password:             pass,
		CACert:               []byte(c.CA),
		RequestTimeout:       c.RequestTimeout,
		RetryOnStatus:        retryStatuses,
		MaxRetries:           c.MaxRetries,
		RetryBackoff:         retryBackoff,
		EnableRetryOnTimeout: true,
		EnableMetrics:        true,
		Observer:             observer,
		DiscoverNodesOnStart: &discoverNodesOnStart,
	}
}
