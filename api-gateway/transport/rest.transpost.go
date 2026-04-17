package transport

import (
	"context"
	"io"
	"net/http"

	kitendpoint "github.com/go-kit/kit/endpoint"
	"github.com/go-kit/kit/log/level"
	kithttp "github.com/go-kit/kit/transport/http"
	kitlog "github.com/go-kit/log"
	"github.com/huzaifa678/SAAS-services/endpoint"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func DecodeRESTRequest(_ context.Context, r *http.Request) (interface{}, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	path := r.URL.Path

	method := r.Method

	headers := map[string][]string{}
	for k, v := range r.Header {
		headers[k] = v
	}

	return endpoint.ForwardRequest{
		Body:   body,
		Header: headers,
		Path:   path,
		Method: method,
	}, nil
}

func EncodeRESTRequest(_ context.Context, w http.ResponseWriter, response interface{}) error {
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

// BillingForward godoc
// @Summary Billing REST endpoint
// @Description Forwards REST requests to Billing Service through API Gateway
// @Tags Billing
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer JWT token"
// @Param path path string false "Dynamic billing route path"
// @Param request body object false "Billing request payload"
// @Success 200 {object} endpoint.ForwardResponseSwagger
// @Failure 400 {object} endpoint.ForwardResponseSwagger
// @Failure 401 {object} endpoint.ForwardResponseSwagger
// @Failure 503 {object} endpoint.ForwardResponseSwagger
// @Router /api/billing/{path} [get]
// @Router /api/billing/{path} [post]
// @Router /api/billing/{path} [put]
// @Router /api/billing/{path} [delete]
func NewRESTHTTPHandler(endpoint kitendpoint.Endpoint, logger kitlog.Logger) http.Handler {
	return kithttp.NewServer(
		endpoint,
		DecodeRESTRequest,
		EncodeRESTRequest,
		kithttp.ServerBefore(func(ctx context.Context, r *http.Request) context.Context {

			propagator := otel.GetTextMapPropagator()
			
			ctx = propagator.Extract(ctx, propagation.HeaderCarrier(r.Header))

			level.Info(logger).Log(
				"msg", "incoming request",
				"method", r.Method,
				"path", r.URL.Path,
			)
			return ctx
		}),
	)
}