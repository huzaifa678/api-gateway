package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// statusRecorder captures the response status code so the logging middleware can
// report it defaulting to 200
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Replaces the go-kit LoggingMiddleware
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			begin := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			sc := trace.SpanFromContext(r.Context()).SpanContext()
			logger.InfoContext(r.Context(), "request handled",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"trace_id", sc.TraceID().String(),
				"span_id", sc.SpanID().String(),
				"took", time.Since(begin).String(),
			)
		})
	}
}
