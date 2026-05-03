package transport

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-kit/kit/endpoint"
	"github.com/huzaifa678/SAAS-services/errors"
	ep "github.com/huzaifa678/SAAS-services/endpoint"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func DecodeRESTRequest(_ context.Context, r *http.Request) (interface{}, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	headers := map[string][]string{}
	for k, v := range r.Header {
		headers[k] = v
	}

	return ep.ForwardRequest{
		Body:   body,
		Header: headers,
		Path:   r.URL.Path,
		Method: r.Method,
	}, nil
}

func EncodeRESTRequest(_ context.Context, w http.ResponseWriter, response interface{}) error {
	resp := response.(ep.ForwardResponse)
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
// @Success 200 {object} ep.ForwardResponseSwagger
// @Failure 400 {object} ep.ForwardResponseSwagger
// @Failure 401 {object} ep.ForwardResponseSwagger
// @Failure 503 {object} ep.ForwardResponseSwagger
// @Router /api/billing/{path} [get]
// @Router /api/billing/{path} [post]
// @Router /api/billing/{path} [put]
// @Router /api/billing/{path} [delete]
func NewRESTHTTPHandler(e endpoint.Endpoint, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		logger.InfoContext(ctx, "incoming request", "method", r.Method, "path", r.URL.Path)

		req, err := DecodeRESTRequest(ctx, r)
		if err != nil {
			errors.EncodeError(ctx, err, w)
			return
		}

		resp, err := e(ctx, req)
		if err != nil {
			errors.EncodeError(ctx, err, w)
			return
		}

		if err := EncodeRESTRequest(ctx, w, resp); err != nil {
			errors.EncodeError(ctx, err, w)
		}
	})
}
