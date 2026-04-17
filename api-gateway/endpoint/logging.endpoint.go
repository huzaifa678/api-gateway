package endpoint

import (
	"context"
	"time"

	"github.com/go-kit/kit/endpoint"
	kitlog "github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"go.opentelemetry.io/otel/trace"
)

func LoggingMiddleware(logger kitlog.Logger) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (interface{}, error) {

			begin := time.Now()

			resp, err := next(ctx, request)

			span := trace.SpanFromContext(ctx)
			sc := span.SpanContext()

			traceID := sc.TraceID().String()
			spanID := sc.SpanID().String()

			ctxLogger := kitlog.With(logger,
				"trace_id", traceID,
				"span_id", spanID,
				"level", "info",
			)

			level.Info(ctxLogger).Log(
				"msg", "endpoint called",
				"took", time.Since(begin).String(),
				"error", err,
			)

			return resp, err
		}
	}
}