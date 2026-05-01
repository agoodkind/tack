package telemetry

import (
	"context"
	"log/slog"

	connect "connectrpc.com/connect"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// ConnectUnaryInterceptor returns a server interceptor that applies the same
// request, trace, and logger correlation contract as the HTTP middleware.
func ConnectUnaryInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			requestID := RequestID(ctx)
			if requestID == "" {
				requestID = req.Header().Get(RequestIDHeader)
			}
			if requestID == "" {
				requestID = uuid.New().String()
			}

			ctx = WithRequestMetadata(ctx, requestID)
			if TraceID(ctx) == "" {
				ctx = propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(req.Header()))
			}
			ctx, span := StartSpan(ctx, req.Spec().Procedure,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("rpc.system", "connect"),
					attribute.String("rpc.method", req.Spec().Procedure),
					attribute.String("http.request.method", req.HTTPMethod()),
				),
			)
			defer span.End()

			ctx = WithTraceLogger(ctx,
				slog.String("rpc_procedure", req.Spec().Procedure),
				slog.String("rpc_protocol", req.Peer().Protocol),
			)

			res, err := next(ctx, req)
			if res != nil {
				res.Header().Set(RequestIDHeader, requestID)
			}
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, connect.CodeOf(err).String())
			} else {
				span.SetStatus(codes.Ok, "ok")
			}
			L(ctx).InfoContext(ctx, "rpc.request",
				slog.String("procedure", req.Spec().Procedure),
				slog.String("protocol", req.Peer().Protocol),
				slog.String("method", req.HTTPMethod()),
				slog.String("status", connect.CodeOf(err).String()),
			)
			return res, err
		}
	})
}
