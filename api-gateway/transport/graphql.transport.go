package transport

import (
	"context"
	"io"
	"net/http"

	kitendpoint "github.com/go-kit/kit/endpoint"
	kithttp "github.com/go-kit/kit/transport/http"
	"github.com/huzaifa678/SAAS-services/endpoint"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"github.com/huzaifa678/SAAS-services/errors"
)

func DecodeGraphQLRequest(_ context.Context, r *http.Request) (interface{}, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	headers := map[string][]string{}
	for k, v := range r.Header {
		headers[k] = v
	}

	return endpoint.ForwardRequest{
		Body:   body,
		Header: headers,
		Path: r.URL.Path,
		Method: r.Method,
	}, nil
}

func EncodeGraphQLResponse(_ context.Context, w http.ResponseWriter, response interface{}) error {
	resp := response.(endpoint.ForwardResponse)
	if resp.Error != "" {
		http.Error(w, resp.Error, http.StatusServiceUnavailable)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, err := w.Write(resp.Body)
	return err
}

// GraphQLAuth godoc
// @Summary Auth GraphQL endpoint
// @Description Forwards GraphQL requests to the Auth Service
// @Tags Auth
// @Accept json
// @Produce json
// @Param Authorization header string false "Bearer JWT token"
// @Param request body object true "GraphQL query payload"
// @Success 200 {object} endpoint.ForwardResponseSwagger
// @Failure 503 {object} endpoint.ForwardResponseSwagger
// @Router /api/auth/ [post]
// GraphQLSubscription godoc
// @Summary Subscription GraphQL endpoint
// @Description Forwards GraphQL requests to the Subscription Service
// @Tags Subscription
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer JWT token"
// @Param request body object true "GraphQL query payload"
// @Success 200 {object} endpoint.ForwardResponseSwagger
// @Failure 503 {object} endpoint.ForwardResponseSwagger
// @Router /api/subscription/ [post]
func NewGraphQLHTTPHandler(endpoint kitendpoint.Endpoint) http.Handler {
	return kithttp.NewServer(
		endpoint,
		DecodeGraphQLRequest,
		EncodeGraphQLResponse,
		kithttp.ServerBefore(func(ctx context.Context, r *http.Request) context.Context {
			return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
		}),
		kithttp.ServerErrorEncoder(errors.EncodeError),
	)
}
