package telemetry

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "goodkind.io/tack/internal/telemetry"

func setupTracing(endpoint string) (io.Closer, error) {
	resourceAttrs, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("tack-server"),
			attribute.String("service.namespace", "tack"),
		),
	)
	if err != nil {
		return nil, err
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resourceAttrs),
	}

	if strings.TrimSpace(endpoint) != "" {
		exporter, err := newTraceExporter(endpoint)
		if err != nil {
			return nil, err
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	}

	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return traceCloser{shutdowns: []func(context.Context) error{provider.Shutdown}}, nil
}

func tracer() trace.Tracer {
	return otel.Tracer(instrumentationName)
}

// StartSpan starts a child span using Tack's shared instrumentation name.
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return tracer().Start(ctx, name, opts...)
}

func newTraceExporter(endpoint string) (*otlptrace.Exporter, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	options := []otlptracegrpc.Option{}
	if parsedURL, err := url.Parse(endpoint); err == nil && parsedURL.Host != "" {
		endpoint = parsedURL.Host
		if parsedURL.Scheme == "http" {
			options = append(options, otlptracegrpc.WithInsecure())
		}
	}

	return otlptracegrpc.New(ctx, append(options, otlptracegrpc.WithEndpoint(endpoint))...)
}

type traceCloser struct {
	shutdowns []func(context.Context) error
}

func (t traceCloser) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var errs []error
	for i := len(t.shutdowns) - 1; i >= 0; i-- {
		if err := t.shutdowns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
