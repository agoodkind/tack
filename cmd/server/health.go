package main

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// healthCheckTimeout bounds the whole health check so concurrent probes share
// one deadline and a hung store cannot delay the ingress proxy's health check.
const healthCheckTimeout = 2 * time.Second

type healthProbeResult struct {
	name string
	err  error
}

// newHealthHandler serves GET /healthz for the ingress proxy's active
// health check. It starts every probe concurrently under one two-second
// deadline. 200 means every named probe succeeded. 503 names the first failed
// probe in sorted name order. Probes must be cheap reads.
func newHealthHandler(probes map[string]func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
		defer cancel()

		names := make([]string, 0, len(probes))
		for name := range probes {
			names = append(names, name)
		}
		sort.Strings(names)

		results := make(chan healthProbeResult, len(probes))
		for _, name := range names {
			probe := probes[name]
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						slog.ErrorContext(ctx, "health.probe_panic", slog.Any("err", recovered))
					}
				}()
				results <- healthProbeResult{name: name, err: probe(ctx)}
			}()
		}

		errorsByName := make(map[string]error, len(probes))
		for range probes {
			select {
			case result := <-results:
				errorsByName[result.name] = result.err
			case <-ctx.Done():
				for _, name := range names {
					if _, completed := errorsByName[name]; !completed {
						http.Error(w, name+" unhealthy", http.StatusServiceUnavailable)
						return
					}
				}
			}
		}
		for _, name := range names {
			if errorsByName[name] != nil {
				http.Error(w, name+" unhealthy", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}
