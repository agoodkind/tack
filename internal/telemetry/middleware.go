package telemetry

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// RequestLogger is HTTP middleware that:
//   - Generates a request ID (or reads X-Request-ID from the incoming header)
//   - Injects a logger with request_id into the context
//   - Logs every request: method, path, status, latency, bytes
//   - Sets X-Request-ID on the response so callers can correlate logs
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}

		l := slog.Default().With(
			slog.String("request_id", reqID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote_addr", r.RemoteAddr),
		)

		ctx := WithLogger(r.Context(), l)
		r = r.WithContext(ctx)

		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		w.Header().Set("X-Request-ID", reqID)

		next.ServeHTTP(rw, r)

		l.Info("request",
			slog.Int("status", rw.status),
			slog.Int64("bytes", rw.bytes),
			slog.Duration("latency", time.Since(start)),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += int64(n)
	return n, err
}
