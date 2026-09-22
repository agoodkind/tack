// Package search provides the native OpenSearch control adapter.
package search

import (
	"crypto/x509"
	"fmt"
	"net/url"
	"strings"
	"time"
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
func (c Config) Validate() error {
	parsed, err := url.Parse(c.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("search endpoint must be one https URL")
	}
	if strings.TrimSpace(c.Username) == "" || c.Password == "" {
		return fmt.Errorf("search credentials are required")
	}
	if c.RequestTimeout <= 0 || c.MaxRetries < 0 {
		return fmt.Errorf("search timeout and retries must be valid")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(c.CA)) {
		return fmt.Errorf("search CA is invalid")
	}
	return nil
}
