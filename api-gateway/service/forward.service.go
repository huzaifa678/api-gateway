package service

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/sony/gobreaker"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/huzaifa678/SAAS-services/circuit"
	"github.com/huzaifa678/SAAS-services/utils"
)

type ForwardService interface {
	Forward(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error)
}

type httpForwarder struct {
	baseURL string
	client  *http.Client
	logger  *slog.Logger
}

func (f *httpForwarder) Forward(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error) {
	fullURL := f.baseURL + path
	f.logger.InfoContext(ctx, "forwarding request", "url", fullURL, "method", method)

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		for _, val := range v {
			req.Header.Add(k, val)
		}
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

// breakerProxy is a protection proxy. it controls access to the wrapped
// ForwardService through a circuit breaker and substitutes a fallback response
// when the breaker is open or the upstream call fails
type breakerProxy struct {
	next        ForwardService
	cb          *gobreaker.CircuitBreaker
	fallbackMsg string
}

func (p *breakerProxy) Forward(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error) {
	type result struct {
		body   []byte
		status int
	}

	res, err := p.cb.Execute(func() (interface{}, error) {
		b, status, err := p.next.Forward(ctx, body, headers, path, method)
		if err != nil {
			return nil, err
		}
		return result{body: b, status: status}, nil
	})
	if err != nil {
		fallback := []byte(`{"errors":[{"message":"` + p.fallbackMsg + `"}]}`)
		return fallback, http.StatusServiceUnavailable, nil
	}

	r := res.(result)
	return r.body, r.status, nil
}

// NewForwardService composes the proxy chain a protection proxy in front of
// the real HTTP forwarder and returns it behind the ForwardService interface
func NewForwardService(
	baseURL string,
	serviceName string,
	fallbackMsg string,
	cbCfg utils.CircuitBreakerConfig,
	logger *slog.Logger,
) ForwardService {
	// Timeout mechanism so that the gateway does not hang on a slow upstream
	// and times out allowing the stalled backend to fail fast and for the goroutines
	// and sockets to not pile up and exhaust the gateway.
	real := &httpForwarder{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
		logger: logger,
	}

	return &breakerProxy{
		next:        real,
		cb:          circuit.NewBreaker(serviceName, cbCfg),
		fallbackMsg: fallbackMsg,
	}
}
