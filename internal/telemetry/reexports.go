package telemetry

import "goodkind.io/gklog/trace"

// RequestIDHeader is the caller-visible correlation header tack preserves on
// inbound requests and echoes on responses. Re-exported from gklog/trace so
// existing call sites that import this package keep compiling.
const RequestIDHeader = trace.RequestIDHeader

// Trace context helpers, re-exported from gklog/trace.
var (
	WithRequestMetadata = trace.WithRequestMetadata
	RequestID           = trace.RequestID
	TraceID             = trace.TraceID
	SpanID              = trace.SpanID
	WithTraceLogger     = trace.WithTraceLogger
	StartSpan           = trace.StartSpan
	RequestLogger       = trace.RequestLogger
)

// QueryTracer is the pgx.QueryTracer implementation tack attaches to its
// pgxpool. Re-exported from gklog/trace.
type QueryTracer = trace.QueryTracer
