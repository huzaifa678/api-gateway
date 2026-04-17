package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	kitlog "github.com/go-kit/log"
	"github.com/huzaifa678/SAAS-services/utils"
)

func newTestService(t *testing.T, handler http.HandlerFunc) (ForwardService, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	svc := NewForwardService(
		srv.URL,
		"test-svc",
		"test service unavailable",
		utils.CircuitBreakerConfig{TimeoutMs: 1000, ErrorThreshold: 5, ResetTimeoutMs: 5000},
		kitlog.NewNopLogger(),
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
		kitlog.NewNopLogger(),
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
		kitlog.NewNopLogger(),
	)

	// url.Parse fails before the circuit breaker, so the error propagates directly
	// (no fallback body, status 0)
	_, status, err := svc.Forward(context.Background(), nil, http.Header{}, "/path", http.MethodGet)
	if err == nil {
		t.Fatal("expected error for invalid base URL")
	}
	if status != 0 {
		t.Fatalf("expected status 0 for parse error, got %d", status)
	}
}
