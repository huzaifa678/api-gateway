package middleware

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Replaces the go-kit TracedEndpoint plus the per-transport propagator Extract
func Tracing(name string) func(http.Handler) http.Handler {
	tracer := otel.Tracer("api-gateway")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, name)
			defer span.End()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
