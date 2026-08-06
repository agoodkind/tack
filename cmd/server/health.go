package main

import (
	"context"
	"net/http"
	"time"
)

// healthProbeTimeout bounds each datastore probe so a hung store makes the
// endpoint report unhealthy instead of hanging the proxy's health check.
const healthProbeTimeout = 2 * time.Second

// newHealthHandler serves GET /healthz for the ingress proxy's active
// health check. 200 means every named probe succeeded within its timeout;
// 503 names the first failing probe. Probes must be cheap reads.
func newHealthHandler(probes map[string]func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, probe := range probes {
			ctx, cancel := context.WithTimeout(r.Context(), healthProbeTimeout)
			err := probe(ctx)
			cancel()
			if err != nil {
				http.Error(w, name+" unhealthy", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}
