package telemetry

import "context"

// Tracer is a minimal span-style hook reserved for a future OpenTelemetry
// adoption. The default implementation is a no-op so call sites can wrap an
// operation with Start without paying any cost. When we wire OTel, we swap
// the default tracer for an exporter and every existing call site picks up
// real spans without code changes.
type Tracer interface {
	// Start opens a span named name and returns a child context plus the
	// closer that ends the span. Idiomatic use:
	//
	//   ctx, end := telemetry.DefaultTracer.Start(ctx, "store.node.get")
	//   defer end()
	Start(ctx context.Context, name string) (context.Context, func())
}

// DefaultTracer is the package-level Tracer. Replace via SetTracer when an
// OTel-aware tracer is wired up.
var DefaultTracer Tracer = noopTracer{}

// SetTracer installs t as the package-level tracer. Pass nil to revert to a
// no-op.
func SetTracer(t Tracer) {
	if t == nil {
		DefaultTracer = noopTracer{}
		return
	}
	DefaultTracer = t
}

type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string) (context.Context, func()) {
	return ctx, func() {}
}
