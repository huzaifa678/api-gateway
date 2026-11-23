package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/huzaifa678/SAAS-services/interceptor"
	"github.com/huzaifa678/SAAS-services/middleware"
	"github.com/huzaifa678/SAAS-services/service"
	"github.com/huzaifa678/SAAS-services/transport"
	"github.com/huzaifa678/SAAS-services/utils"
	"github.com/redis/go-redis/v9"
)

// redisOrSkip returns a flushed Redis client or skips the test when none is up.
func redisOrSkip(t *testing.T) *redis.Client {
	t.Helper()
	rc := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := rc.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available at %s: %v", redisAddr, err)
	}
	rc.FlushDB(context.Background())
	return rc
}

const (
	jwtSecret = "integration-secret"
	redisAddr = "localhost:6379"
)

var (
	cbCfg     = utils.CircuitBreakerConfig{TimeoutMs: 2000, ErrorThreshold: 3, ResetTimeoutMs: 5000}
	nopLogger = slog.New(slog.NewTextHandler(io.Discard, nil))
)

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
func buildMux(authURL, subURL, billURL, agentURL string) http.Handler {
	authSvc := service.NewForwardService(authURL, "auth-service", "auth unavailable", cbCfg, nopLogger)
	subSvc := service.NewForwardService(subURL, "sub-service", "sub unavailable", cbCfg, nopLogger)
	billSvc := service.NewForwardService(billURL, "bill-service", "bill unavailable", cbCfg, nopLogger)
	agentSvc := service.NewForwardService(agentURL, "agent-service", "agent unavailable", cbCfg, nopLogger)

	jwt := interceptor.JWTMiddleware(jwtSecret)

	mux := http.NewServeMux()
	mux.Handle("/api/auth/", transport.NewHandler(authSvc))
	mux.Handle("/api/subscription/", jwt(transport.NewHandler(subSvc)))
	mux.Handle("/api/v1/billing/", jwt(transport.NewHandler(billSvc)))
	mux.Handle("/api/v1/agent/", jwt(transport.NewHandler(agentSvc)))

	return transport.CORSMiddleware([]string{"http://localhost:3000"})(mux)
}

// --- Auth ---

func TestIntegration_AuthForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{"login":{"token":"abc"}}}`))
	}))
	defer upstream.Close()

	gw := httptest.NewServer(buildMux(upstream.URL, "http://localhost:19991", "http://localhost:19992", "http://localhost:19993"))
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

	gw := httptest.NewServer(buildMux(upstream.URL, "http://localhost:19991", "http://localhost:19992", "http://localhost:19993"))
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
	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", "http://localhost:19992", "http://localhost:19993"))
	defer gw.Close()

	// endpoint returns an error (unauthorized) → EncodeError → 500
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

	gw := httptest.NewServer(buildMux("http://localhost:19990", upstream.URL, "http://localhost:19992", "http://localhost:19993"))
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
	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", "http://localhost:19992", "http://localhost:19993"))
	defer gw.Close()

	// endpoint returns an error (unauthorized) → EncodeError → 500
	resp, err := http.Get(gw.URL + "/api/v1/billing/invoices")
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

	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", upstream.URL, "http://localhost:19993"))
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/api/v1/billing/invoices", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("user1"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Agent (JWT required, REST) ---

func TestIntegration_AgentForward_NoJWT(t *testing.T) {
	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", "http://localhost:19992", "http://localhost:19993"))
	defer gw.Close()

	// endpoint returns an error (unauthorized) → EncodeError → 500
	resp, err := http.Get(gw.URL + "/api/v1/agent/conversations/c1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 (endpoint error from missing JWT), got %d", resp.StatusCode)
	}
}

func TestIntegration_AgentForward_WithJWT(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"c1","messages":[]}`))
	}))
	defer upstream.Close()

	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", "http://localhost:19992", upstream.URL))
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/api/v1/agent/conversations/c1", nil)
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

func TestIntegration_CircuitBreaker_FallbackOnUnreachable(t *testing.T) {
	svc := service.NewForwardService(
		"http://localhost:19999", // nothing listening
		"dead-svc",
		"dead service unavailable",
		utils.CircuitBreakerConfig{TimeoutMs: 500, ErrorThreshold: 1, ResetTimeoutMs: 10000},
		nopLogger,
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
	gw := httptest.NewServer(buildMux("http://localhost:19990", "http://localhost:19991", "http://localhost:19992", "http://localhost:19993"))
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

	rc.FlushDB(context.Background())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{}}`))
	}))
	defer upstream.Close()

	authSvc := service.NewForwardService(upstream.URL, "auth-service", "auth unavailable", cbCfg, nopLogger)
	authHandler := middleware.RateLimit(rc, 2, 2, "integ-auth", nopLogger, 30*time.Second)(transport.NewHandler(authSvc))

	mux := http.NewServeMux()
	mux.Handle("/api/auth/", authHandler)
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

// --- Response caching (requires real Redis) ---

func TestIntegration_Cache_GET(t *testing.T) {
	rc := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := rc.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available at %s: %v", redisAddr, err)
	}
	defer rc.Close()
	rc.FlushDB(context.Background())

	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":"ok"}`))
	}))
	defer upstream.Close()

	svc := service.NewCachingProxy(
		service.NewForwardService(upstream.URL, "bill-service", "bill unavailable", cbCfg, nopLogger),
		rc, 30*time.Second, nopLogger,
	)

	auth := http.Header{"Authorization": {"Bearer user1"}}

	// First GET reaches the backend and is cached.
	svc.Forward(context.Background(), nil, auth, "/api/v1/billing/invoices", http.MethodGet)
	// Second identical GET is served from cache.
	svc.Forward(context.Background(), nil, auth, "/api/v1/billing/invoices", http.MethodGet)
	if hits != 1 {
		t.Fatalf("expected backend hit once (cached), got %d", hits)
	}

	// A different caller must not read the first caller's cache.
	other := http.Header{"Authorization": {"Bearer user2"}}
	svc.Forward(context.Background(), nil, other, "/api/v1/billing/invoices", http.MethodGet)
	if hits != 2 {
		t.Fatalf("expected separate cache per caller, got %d hits", hits)
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

	gw := httptest.NewServer(buildMux(upstream.URL, "http://localhost:19991", "http://localhost:19992", "http://localhost:19993"))
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

	gw := httptest.NewServer(buildMux(upstream.URL, "http://localhost:19991", "http://localhost:19992", "http://localhost:19993"))
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/api/auth/", "application/json", strings.NewReader(`{"query":"{ user }"}`))
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if _, ok := result["data"]; !ok {
		t.Fatalf("expected 'data' key in response, got %v", result)
	}
}

// --- Idempotency (requires real Redis) ---

func TestIntegration_Idempotency_ReplaysResult(t *testing.T) {
	rc := redisOrSkip(t)
	defer rc.Close()

	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"charge":%d}`, n)
	}))
	defer upstream.Close()

	svc := service.NewForwardService(upstream.URL, "bill-service", "bill unavailable", cbCfg, nopLogger)
	h := middleware.Idempotency(rc, time.Hour, 30*time.Second, nopLogger)(transport.NewHandler(svc))
	gw := httptest.NewServer(h)
	defer gw.Close()

	do := func(key, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, gw.URL+"/api/billing/charge", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Same key + same body twice: backend runs once, both responses identical.
	s1, b1 := do("key-1", `{"amount":100}`)
	s2, b2 := do("key-1", `{"amount":100}`)
	if s1 != http.StatusCreated || s2 != http.StatusCreated {
		t.Fatalf("expected 201 both times, got %d and %d", s1, s2)
	}
	if b1 != b2 {
		t.Fatalf("expected identical replayed body, got %q then %q", b1, b2)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("expected backend hit once, got %d", n)
	}

	// Same key, different body: rejected as misuse.
	if s, _ := do("key-1", `{"amount":999}`); s != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for key reuse with different body, got %d", s)
	}
}

// --- Cache stampede (requires real Redis) ---

func TestIntegration_Cache_StampedeCoalesced(t *testing.T) {
	rc := redisOrSkip(t)
	defer rc.Close()

	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(150 * time.Millisecond) // slow enough that misses overlap
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":"ok"}`))
	}))
	defer upstream.Close()

	svc := service.NewCachingProxy(
		service.NewForwardService(upstream.URL, "bill-service", "bill unavailable", cbCfg, nopLogger),
		rc, 30*time.Second, nopLogger,
	)

	auth := http.Header{"Authorization": {"Bearer user1"}}
	const n = 30
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			svc.Forward(context.Background(), nil, auth, "/api/billing/report", http.MethodGet)
		}()
	}
	wg.Wait()

	// singleflight collapses the concurrent misses on this instance to one call.
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 upstream call under stampede, got %d", got)
	}
}
