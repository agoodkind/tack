package search

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/opensearch-project/opensearch-go/v4/opensearchtransport"
	"goodkind.io/tack/internal/telemetry"
)

// endpointObserver records every connection URL the official client reports
// in its connection lifecycle and routing events.
type endpointObserver struct {
	mutex     sync.Mutex
	endpoints map[string]struct{}
}

var _ opensearchtransport.ConnectionObserver = (*endpointObserver)(nil)

func newEndpointObserver() *endpointObserver {
	return &endpointObserver{mutex: sync.Mutex{}, endpoints: map[string]struct{}{}}
}

func (o *endpointObserver) record(endpoint string) {
	if endpoint == "" {
		return
	}
	o.mutex.Lock()
	defer o.mutex.Unlock()
	o.endpoints[endpoint] = struct{}{}
}

func (o *endpointObserver) snapshot() []string {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	return slices.Collect(maps.Keys(o.endpoints))
}

func (o *endpointObserver) OnPromote(event opensearchtransport.ConnectionEvent) { o.record(event.URL) }

func (o *endpointObserver) OnDemote(event opensearchtransport.ConnectionEvent) { o.record(event.URL) }

func (o *endpointObserver) OnOverloadDetected(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnOverloadCleared(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnDiscoveryAdd(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnDiscoveryRemove(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnDiscoveryUnchanged(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnHealthCheckPass(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnHealthCheckFail(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnStandbyPromote(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnStandbyDemote(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnWarmupRequest(event opensearchtransport.ConnectionEvent) {
	o.record(event.URL)
}

func (o *endpointObserver) OnRoute(event opensearchtransport.RouteEvent) {
	o.record(event.Selected.URL)
	for _, candidate := range event.Candidates {
		o.record(candidate.URL)
	}
}

func (o *endpointObserver) OnShardMapInvalidation(event opensearchtransport.ShardMapInvalidationEvent) {
	o.record(event.ConnURL)
}

// Endpoints returns, in sorted order, every endpoint that the official
// client's connection pool metrics list and that its observer events report.
// An observer event can report an endpoint before the client sends a request
// to it.
func (a *Adapter) Endpoints(ctx context.Context) ([]string, error) {
	metrics, err := a.client.Metrics()
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch client metrics: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.client.metrics_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	endpoints := map[string]struct{}{}
	for _, endpoint := range a.observer.snapshot() {
		endpoints[endpoint] = struct{}{}
	}
	for _, connection := range metrics.Connections {
		metric, isMetric := connection.(opensearchtransport.ConnectionMetric)
		if !isMetric {
			wrapped := fmt.Errorf("OpenSearch client reported connection metric type %T", connection)
			telemetry.L(ctx).ErrorContext(ctx, "search.client.metrics_invalid", slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		endpoints[metric.URL] = struct{}{}
	}
	return slices.Sorted(maps.Keys(endpoints)), nil
}
