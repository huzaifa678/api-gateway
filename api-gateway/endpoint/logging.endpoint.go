package endpoint

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-kit/kit/endpoint"
	"go.opentelemetry.io/otel/trace"
)

func LoggingMiddleware(logger *slog.Logger) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (interface{}, error) {
			begin := time.Now()
			resp, err := next(ctx, request)

			sc := trace.SpanFromContext(ctx).SpanContext()
			logger.InfoContext(ctx, "endpoint called",
				"trace_id", sc.TraceID().String(),
				"span_id", sc.SpanID().String(),
				"took", time.Since(begin).String(),
				"error", err,
			)
			return resp, err
		}
	}
}
