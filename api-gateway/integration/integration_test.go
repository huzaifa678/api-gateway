package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kitlog "github.com/go-kit/log"
	"github.com/golang-jwt/jwt/v5"
	"github.com/huzaifa678/SAAS-services/endpoint"
	"github.com/huzaifa678/SAAS-services/interceptor"
	"github.com/huzaifa678/SAAS-services/service"
	"github.com/huzaifa678/SAAS-services/transport"
	"github.com/huzaifa678/SAAS-services/utils"
	"github.com/redis/go-redis/v9"
)

const (
	jwtSecret   = "integration-secret"
	redisAddr   = "localhost:6379"
)

var cbCfg = utils.CircuitBreakerConfig{
	TimeoutMs:      2000,
	ErrorThreshold: 3,
	ResetTimeoutMs: 5000,
}

// makeJWT creates a signed JWT for the given userID.
func makeJWT(userID string) string {
	claims := interceptor.MyClaims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
	return token
}

// buildMux wires up the full gateway mux against the provided upstream test servers.
func buildMux(authURL, subURL, billURL string) http.Handler {
	logger := kitlog.NewNopLogger()

	authSvc := service.NewForwardService(authURL, "auth-service", "auth unavailable", cbCfg, logger)
	subSvc := service.NewForwardService(subURL, "sub-service", "sub unavailable", cbCfg, logger)
	billSvc := service.NewForwardService(billURL, "bill-service", "bill unavailable", cbCfg, logger)

	authEp := endpoint.MakeAuthEndpoint(authSvc)
	subEp := endpoint.MakeSubscriptionEndpoint(subSvc)
	billEp := endpoint.MakeBillingEndpoint(billSvc)

	subEp = interceptor.JWTMiddleware(jwtSecret)(subEp)
	billEp = interceptor.JWTMiddleware(jwtSecret)(billEp)

	mux := http.NewServeMux()
	mux.Handle("/api/auth/", transport.NewGraphQLHTTPHandler(authEp))
	mux.Handle("/api/subscription/", transport.NewGraphQLHTTPHandler(subEp))
	mux.Handle("/api/billing/", transport.NewRESTHTTPHandler(billEp, logger))

	return transport.CORSMiddleware([]string{"http://localhost:3000"})(mux)
}

// --- Auth ---

func TestIntegration_AuthForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{"login":{"token":"abc"}}}`))
	}))
	defer upstream.Close()

	gw := httptest.NewServer(buildMux(upstream.URL, "http://localhost:19991", "http://localhost:19992"))
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/api/auth/", "application/json", strings.NewReader(`{"query":"mutation { login }"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestIntegration_AuthForward_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	gw := httptest.NewServer(buildMux(upstream.URL, "http://localhost:19991", "http://localhost:19992"))
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/api/auth/", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 from upstream, got %d", resp.StatusCode)
	}
}

// --- Subscription (JWT required) ---

func TestIntegration_SubscriptionForward_NoJWT(t *testing.T) {
	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", "http://localhost:19992"))
	defer gw.Close()

	// go-kit HTTP server returns 500 when the endpoint returns an error (unauthorized)
	resp, err := http.Post(gw.URL+"/api/subscription/", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 (endpoint error from missing JWT), got %d", resp.StatusCode)
	}
}

func TestIntegration_SubscriptionForward_WithJWT(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{"subscription":{"plan":"pro"}}}`))
	}))
	defer upstream.Close()

	gw := httptest.NewServer(buildMux("http://localhost:19990", upstream.URL, "http://localhost:19992"))
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/api/subscription/", strings.NewReader(`{"query":"{ subscription }"}`))
	req.Header.Set("Authorization", "Bearer "+makeJWT("user1"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Billing (JWT required, REST) ---

func TestIntegration_BillingForward_NoJWT(t *testing.T) {
	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", "http://localhost:19992"))
	defer gw.Close()

	// go-kit HTTP server returns 500 when the endpoint returns an error (unauthorized)
	resp, err := http.Get(gw.URL + "/api/billing/invoices")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 (endpoint error from missing JWT), got %d", resp.StatusCode)
	}
}

func TestIntegration_BillingForward_WithJWT(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"inv_1","amount":100}]`))
	}))
	defer upstream.Close()

	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", upstream.URL))
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/api/billing/invoices", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("user1"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Circuit Breaker ---

// TestIntegration_CircuitBreaker_FallbackOnUnreachable verifies that when the upstream
// is completely unreachable, the forward service returns the fallback body with 503.
// NOTE: The circuit breaker state is not shared across Forward() calls because
// WrapWithBreaker creates a new gobreaker instance per call — the breaker trip
// behavior is covered in circuit/breaker_test.go unit tests.
func TestIntegration_CircuitBreaker_FallbackOnUnreachable(t *testing.T) {
	logger := kitlog.NewNopLogger()
	svc := service.NewForwardService(
		"http://localhost:19999", // nothing listening
		"dead-svc",
		"dead service unavailable",
		utils.CircuitBreakerConfig{TimeoutMs: 500, ErrorThreshold: 1, ResetTimeoutMs: 10000},
		logger,
	)

	body, status, err := svc.Forward(context.Background(), nil, http.Header{}, "/test", http.MethodGet)
	if err != nil {
		t.Fatalf("expected fallback (no error), got: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 fallback, got %d", status)
	}
	if !strings.Contains(string(body), "dead service unavailable") {
		t.Fatalf("expected fallback message in body, got %q", string(body))
	}
}

// --- CORS ---

func TestIntegration_CORS_Preflight(t *testing.T) {
	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", "http://localhost:19992"))
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodOptions, gw.URL+"/api/auth/", nil)
	req.Header.Set("Origin", "http://localhost:3000")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 preflight, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("expected CORS header, got %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

// --- Rate Limiting (requires real Redis) ---

func TestIntegration_RateLimit_Redis(t *testing.T) {
	rc := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := rc.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available at %s: %v", redisAddr, err)
	}
	defer rc.Close()

	// flush any leftover keys from previous runs
	rc.FlushDB(context.Background())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{}}`))
	}))
	defer upstream.Close()

	logger := kitlog.NewNopLogger()
	authSvc := service.NewForwardService(upstream.URL, "auth-service", "auth unavailable", cbCfg, logger)
	authEp := endpoint.MakeAuthEndpoint(authSvc)
	authEp = endpoint.RateLimitMiddleware(rc, 2, 2, "integ-auth", logger, 30*time.Second)(authEp)

	mux := http.NewServeMux()
	mux.Handle("/api/auth/", transport.NewGraphQLHTTPHandler(authEp))
	gw := httptest.NewServer(mux)
	defer gw.Close()

	// first 2 requests should succeed (burst=2)
	for i := 0; i < 2; i++ {
		resp, err := http.Post(gw.URL+"/api/auth/", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, resp.StatusCode)
		}
	}

	// 3rd request should be rate-limited
	resp, err := http.Post(gw.URL+"/api/auth/", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 rate limit, got %d", resp.StatusCode)
	}
}

// --- Header propagation ---

func TestIntegration_HeadersPropagatedToUpstream(t *testing.T) {
	var gotHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	gw := httptest.NewServer(buildMux(upstream.URL, "http://localhost:19991", "http://localhost:19992"))
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/api/auth/", strings.NewReader(`{}`))
	req.Header.Set("X-Request-ID", "req-abc-123")
	req.Header.Set("Content-Type", "application/json")
	http.DefaultClient.Do(req)

	if gotHeaders.Get("X-Request-Id") != "req-abc-123" {
		t.Fatalf("expected X-Request-ID forwarded, got %q", gotHeaders.Get("X-Request-Id"))
	}
}

// --- Response body passthrough ---

func TestIntegration_ResponseBodyPassthrough(t *testing.T) {
	payload := `{"data":{"user":{"id":"u1","email":"test@example.com"}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, payload)
	}))
	defer upstream.Close()

	gw := httptest.NewServer(buildMux(upstream.URL, "http://localhost:19991", "http://localhost:19992"))
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/api/auth/", "application/json", strings.NewReader(`{"query":"{ user }"}`))
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	data, ok := result["data"]
	if !ok {
		t.Fatalf("expected 'data' key in response, got %v", result)
	}
	_ = data
}
