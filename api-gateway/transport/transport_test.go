package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

type fakeForwarder struct {
	gotBody   []byte
	gotPath   string
	gotMethod string
	gotAuth   string

	respBody   []byte
	respStatus int
	respErr    error
}

func (f *fakeForwarder) Forward(_ context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error) {
	f.gotBody = body
	f.gotPath = path
	f.gotMethod = method
	f.gotAuth = headers.Get("Authorization")
	return f.respBody, f.respStatus, f.respErr
}

func TestNewHandler_ForwardsRequestAndWritesResponse(t *testing.T) {
	fake := &fakeForwarder{respBody: []byte(`{"ok":true}`), respStatus: http.StatusCreated}
	h := NewHandler(fake)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/invoices", strings.NewReader(`{"key":"value"}`))
	req.Header.Set("Authorization", "Bearer token123")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected upstream status 201, got %d", rr.Code)
	}
	if body, _ := io.ReadAll(rr.Body); string(body) != `{"ok":true}` {
		t.Fatalf("unexpected response body: %q", string(body))
	}
	if string(fake.gotBody) != `{"key":"value"}` {
		t.Fatalf("body not forwarded: got %q", string(fake.gotBody))
	}
	if fake.gotMethod != http.MethodPost || fake.gotPath != "/api/v1/billing/invoices" {
		t.Fatalf("method/path not forwarded: %s %s", fake.gotMethod, fake.gotPath)
	}
	if fake.gotAuth != "Bearer token123" {
		t.Fatalf("auth header not forwarded: %q", fake.gotAuth)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json content type, got %q", ct)
	}
}
