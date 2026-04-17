package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/go-kit/kit/log/level"
	kithttp "github.com/go-kit/kit/transport/http"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	kitlog "github.com/go-kit/log"
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
	logger kitlog.Logger,
) ForwardService {
	s := &forwardService{}

	s.forward = func(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error) {
		fullURL := baseURL + path
		level.Info(logger).Log(
			"msg", "forwarding request",
			"url", fullURL,
			"method", method,
		)
		u, err := url.Parse(fullURL)
		if err != nil {
			return nil, 0, err
		}

		client := kithttp.NewClient(
			method, 
			u,
			encodeRequest,
			decodeResponse,
		).Endpoint()

		wrapped := circuit.WrapWithBreaker(
			func(ctx context.Context) (interface{}, error) {
				reqHeaders := make(http.Header)

				for k, v := range headers {
					for _, val := range v {
						reqHeaders.Add(k, val)
					}
				}

				otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(reqHeaders))

				return client(ctx, struct {
					Body   []byte
					Header http.Header
				}{body, reqHeaders})
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

func encodeRequest(_ context.Context, req *http.Request, request interface{}) error {
	r := request.(struct {
		Body   []byte
		Header http.Header
	})
	req.Body = io.NopCloser(bytes.NewReader(r.Body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range r.Header {
		for _, val := range v {
			req.Header.Add(k, val)
		}
	}
	return nil
}

func decodeResponse(_ context.Context, resp *http.Response) (interface{}, error) {
	b, _ := io.ReadAll(resp.Body)
	return struct {
		Body   []byte
		Status int
	}{b, resp.StatusCode}, nil
}
