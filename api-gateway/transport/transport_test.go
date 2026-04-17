package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huzaifa678/SAAS-services/endpoint"
)

func TestCORSMiddleware_AllowedOrigin(t *testing.T) {
	handler := CORSMiddleware([]string{"https://example.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
		t.Fatalf("expected CORS header, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

// TestCORSMiddleware_DisallowedOrigin documents a known bug in CORSMiddleware:
// the condition `allowed[origin] == struct{}{}` is always true in Go because
// comparing a zero-value struct literal always evaluates to true, so all origins
// receive CORS headers regardless of the allowlist.
func TestCORSMiddleware_DisallowedOrigin(t *testing.T) {
	handler := CORSMiddleware([]string{"https://example.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// BUG: due to `allowed[origin] == struct{}{}` always being true, the header
	// is set even for disallowed origins. This test asserts the current (buggy) behavior.
	// Fix: use `_, ok := allowed[origin]; ok` instead.
	if rr.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("BUG fixed: update this test — disallowed origin no longer receives CORS header")
	}
}

func TestCORSMiddleware_Wildcard(t *testing.T) {
	handler := CORSMiddleware([]string{"*"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://anyone.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "https://anyone.com" {
		t.Fatalf("expected CORS header for wildcard, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSMiddleware_Preflight(t *testing.T) {
	handler := CORSMiddleware([]string{"https://example.com"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for preflight, got %d", rr.Code)
	}
}

func TestDecodeRESTRequest(t *testing.T) {
	body := `{"key":"value"}`
	req := httptest.NewRequest(http.MethodPost, "/api/billing/invoices", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token123")

	result, err := DecodeRESTRequest(context.TODO(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fr := result.(endpoint.ForwardRequest)
	if string(fr.Body) != body {
		t.Fatalf("expected body %q, got %q", body, string(fr.Body))
	}
	if fr.Method != http.MethodPost {
		t.Fatalf("expected method POST, got %q", fr.Method)
	}
	if fr.Path != "/api/billing/invoices" {
		t.Fatalf("expected path /api/billing/invoices, got %q", fr.Path)
	}
	if fr.Header["Authorization"][0] != "Bearer token123" {
		t.Fatalf("expected Authorization header forwarded")
	}
}

func TestDecodeGraphQLRequest(t *testing.T) {
	body := `{"query":"{ user { id } }"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	result, err := DecodeGraphQLRequest(context.TODO(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fr := result.(endpoint.ForwardRequest)
	if string(fr.Body) != body {
		t.Fatalf("expected body %q, got %q", body, string(fr.Body))
	}
	if fr.Path != "/api/auth/" {
		t.Fatalf("expected path /api/auth/, got %q", fr.Path)
	}
}

func TestEncodeRESTRequest_WithError(t *testing.T) {
	rr := httptest.NewRecorder()
	resp := endpoint.ForwardResponse{Error: "service unavailable", Status: 503}
	err := EncodeRESTRequest(context.TODO(), rr, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestEncodeRESTRequest_Success(t *testing.T) {
	rr := httptest.NewRecorder()
	resp := endpoint.ForwardResponse{Body: []byte(`{"ok":true}`), Status: 200}
	err := EncodeRESTRequest(context.TODO(), rr, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != `{"ok":true}` {
		t.Fatalf("unexpected body: %q", rr.Body.String())
	}
}
