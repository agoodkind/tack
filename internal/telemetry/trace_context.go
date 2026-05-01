package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// RequestIDHeader is the caller-visible correlation header tack preserves on
// inbound requests and echoes on responses.
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// WithRequestMetadata stores request-scoped correlation metadata in context.
func WithRequestMetadata(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestID returns the caller-visible request correlation identifier.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

// TraceID returns the active trace identifier from the current span context.
func TraceID(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

// SpanID returns the active span identifier from the current span context.
func SpanID(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}

// WithTraceLogger enriches the current context logger with request and trace
// metadata so downstream log lines stay correlated.
func WithTraceLogger(ctx context.Context, attrs ...slog.Attr) context.Context {
	return WithLogger(ctx, loggerWithContext(ctx, L(ctx), attrs...))
}

func loggerWithContext(ctx context.Context, base *slog.Logger, attrs ...slog.Attr) *slog.Logger {
	loggerAttrs := make([]slog.Attr, 0, len(attrs)+3)
	if requestID := RequestID(ctx); requestID != "" {
		loggerAttrs = append(loggerAttrs, slog.String("request_id", requestID))
	}
	if traceID := TraceID(ctx); traceID != "" {
		loggerAttrs = append(loggerAttrs, slog.String("trace_id", traceID))
	}
	if spanID := SpanID(ctx); spanID != "" {
		loggerAttrs = append(loggerAttrs, slog.String("span_id", spanID))
	}
	loggerAttrs = append(loggerAttrs, attrs...)
	return base.With(attrsToArgs(loggerAttrs)...)
}

func attrsToArgs(attrs []slog.Attr) []any {
	args := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	return args
}
