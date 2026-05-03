package service

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/huzaifa678/SAAS-services/circuit"
	"github.com/huzaifa678/SAAS-services/utils"
)

type ForwardService interface {
	Forward(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error)
}

type forwardService struct {
	forward func(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error)
}

func NewForwardService(
	baseURL string,
	serviceName string,
	fallbackMsg string,
	cbCfg utils.CircuitBreakerConfig,
	logger *slog.Logger,
) ForwardService {
	httpClient := &http.Client{}
	s := &forwardService{}

	s.forward = func(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error) {
		fullURL := baseURL + path
		logger.InfoContext(ctx, "forwarding request", "url", fullURL, "method", method)

		wrapped := circuit.WrapWithBreaker(
			func(ctx context.Context) (interface{}, error) {
				req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(body))
				if err != nil {
					return nil, err
				}
				req.Header.Set("Content-Type", "application/json")
				for k, v := range headers {
					for _, val := range v {
						req.Header.Add(k, val)
					}
				}
				otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

				resp, err := httpClient.Do(req)
				if err != nil {
					return nil, err
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				return struct {
					Body   []byte
					Status int
				}{b, resp.StatusCode}, nil
			},
			serviceName,
			cbCfg,
		)

		res, err := wrapped(ctx)
		if err != nil {
			fallback := []byte(`{"errors":[{"message":"` + fallbackMsg + `"}]}`)
			return fallback, http.StatusServiceUnavailable, nil
		}

		r := res.(struct {
			Body   []byte
			Status int
		})
		return r.Body, r.Status, nil
	}

	return s
}

func (s *forwardService) Forward(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error) {
	return s.forward(ctx, body, headers, path, method)
}
