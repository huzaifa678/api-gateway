package endpoint

import (
	"context"

	"github.com/go-kit/kit/endpoint"
	"go.opentelemetry.io/otel"
)

func TracedEndpoint(name string, e endpoint.Endpoint) endpoint.Endpoint {
	tracer := otel.Tracer("api-gateway")
	return func(ctx context.Context, request interface{}) (interface{}, error) {
		ctx, span := tracer.Start(ctx, name)
		defer span.End()
		return e(ctx, request)
	}
}