package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huzaifa678/SAAS-services/circuit"
	"github.com/huzaifa678/SAAS-services/utils"
)

type countingForwarder struct {
	calls int
	err   error
}

func (c *countingForwarder) Forward(_ context.Context, _ []byte, _ http.Header, _, _ string) ([]byte, int, error) {
	c.calls++
	if c.err != nil {
		return nil, 0, c.err
	}
	return []byte(`ok`), http.StatusOK, nil
}

// NewCachingProxy returns next unchanged when caching is off.
func TestNewCachingProxy_DisabledIsPassthrough(t *testing.T) {
	var next ForwardService = &countingForwarder{}
	if NewCachingProxy(next, nil, time.Minute) != next {
		t.Fatal("nil client should return next unchanged")
	}
	if NewCachingProxy(next, nil, 0) != next {
		t.Fatal("ttl <= 0 should return next unchanged")
	}
}

// Non-GET methods bypass the cache entirely (Redis is never touched).
func TestCacheProxy_NonGetBypassesCache(t *testing.T) {
	backend := &countingForwarder{}
	p := &cacheProxy{next: backend, rdb: nil, ttl: time.Minute}

	_, _, err := p.Forward(context.Background(), []byte(`{}`), http.Header{}, "/x", http.MethodPost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backend.calls != 1 {
		t.Fatalf("expected backend called once, got %d", backend.calls)
	}
}

// TestBreakerProxy_TripsAfterThreshold is a regression test for the bug where
// the circuit breaker was rebuilt on every request and therefore never tripped.
// With the breaker held by the proxy, consecutive failures must open the circuit
// and short-circuit further calls before they reach the upstream.
func TestBreakerProxy_TripsAfterThreshold(t *testing.T) {
	const threshold = 3
	backend := &countingForwarder{err: errors.New("upstream down")}
	proxy := &breakerProxy{
		next:        backend,
		cb:          circuit.NewBreaker("trip-test", utils.CircuitBreakerConfig{TimeoutMs: 1000, ErrorThreshold: threshold, ResetTimeoutMs: 60000}),
		fallbackMsg: "unavailable",
	}

	for i := 0; i < threshold; i++ {
		_, status, err := proxy.Forward(context.Background(), nil, http.Header{}, "/x", http.MethodGet)
		if err != nil {
			t.Fatalf("call %d: fallback should suppress error, got %v", i+1, err)
		}
		if status != http.StatusServiceUnavailable {
			t.Fatalf("call %d: expected 503 fallback, got %d", i+1, status)
		}
	}

	callsBefore := backend.calls
	// Circuit should now be open: this call must be short-circuited, not forwarded.
	body, status, err := proxy.Forward(context.Background(), nil, http.Header{}, "/x", http.MethodGet)
	if err != nil {
		t.Fatalf("unexpected error while open: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 fallback while open, got %d", status)
	}
	if backend.calls != callsBefore {
		t.Fatalf("breaker open but backend still called: before=%d after=%d", callsBefore, backend.calls)
	}
	if len(body) == 0 {
		t.Fatal("expected fallback body")
	}
}

var nopLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestService(t *testing.T, handler http.HandlerFunc) (ForwardService, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	svc := NewForwardService(
		srv.URL,
		"test-svc",
		"test service unavailable",
		utils.CircuitBreakerConfig{TimeoutMs: 1000, ErrorThreshold: 5, ResetTimeoutMs: 5000},
		nopLogger,
	)
	return svc, srv
}

func TestForwardService_Success(t *testing.T) {
	svc, srv := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":"ok"}`))
	})
	defer srv.Close()

	body, status, err := svc.Forward(context.Background(), []byte(`{}`), http.Header{}, "/test", http.MethodPost)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if string(body) != `{"data":"ok"}` {
		t.Fatalf("unexpected body: %q", string(body))
	}
}

func TestForwardService_ForwardsHeaders(t *testing.T) {
	var receivedAuth string
	svc, srv := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	headers := http.Header{"Authorization": {"Bearer mytoken"}}
	svc.Forward(context.Background(), nil, headers, "/test", http.MethodGet)

	if receivedAuth != "Bearer mytoken" {
		t.Fatalf("expected Authorization header forwarded, got %q", receivedAuth)
	}
}

func TestForwardService_FallbackOnUnreachable(t *testing.T) {
	svc := NewForwardService(
		"http://localhost:19999", // nothing listening here
		"dead-svc",
		"dead service unavailable",
		utils.CircuitBreakerConfig{TimeoutMs: 100, ErrorThreshold: 1, ResetTimeoutMs: 1000},
		nopLogger,
	)

	body, status, err := svc.Forward(context.Background(), []byte(`{}`), http.Header{}, "/test", http.MethodPost)
	if err != nil {
		t.Fatalf("unexpected error (fallback should suppress it): %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", status)
	}
	if len(body) == 0 {
		t.Fatal("expected fallback body")
	}
}

func TestForwardService_InvalidURL(t *testing.T) {
	svc := NewForwardService(
		"://bad-url",
		"bad-svc",
		"fallback",
		utils.CircuitBreakerConfig{TimeoutMs: 100, ErrorThreshold: 1, ResetTimeoutMs: 1000},
		nopLogger,
	)

	// http.NewRequestWithContext fails inside the circuit breaker wrapper,
	// so the error is caught and the fallback body + 503 is returned.
	body, status, err := svc.Forward(context.Background(), nil, http.Header{}, "/path", http.MethodGet)
	if err != nil {
		t.Fatalf("expected fallback (no error), got: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 fallback, got %d", status)
	}
	if len(body) == 0 {
		t.Fatal("expected fallback body")
	}
}
