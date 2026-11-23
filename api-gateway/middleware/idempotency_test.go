package middleware

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var idemNop = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeStore is an in-memory idemStore so the middleware can be tested without Redis.
type fakeStore struct {
	mu          sync.Mutex
	results     map[string]idemResult
	locks       map[string]string
	failAcquire bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{results: map[string]idemResult{}, locks: map[string]string{}}
}

func (f *fakeStore) Result(_ context.Context, key string) (idemResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[key]
	return r, ok, nil
}

func (f *fakeStore) Acquire(_ context.Context, key, token string, _ time.Duration) (bool, error) {
	if f.failAcquire {
		return false, errors.New("redis down")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, held := f.locks[key]; held {
		return false, nil
	}
	f.locks[key] = token
	return true, nil
}

func (f *fakeStore) Save(_ context.Context, key string, res idemResult, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[key] = res
	return nil
}

func (f *fakeStore) Release(_ context.Context, key, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locks[key] == token {
		delete(f.locks, key)
	}
	return nil
}

func testCfg() idemConfig {
	c := defaultIdemConfig(time.Hour, 30*time.Second)
	c.waitBudget = 150 * time.Millisecond
	c.pollInterval = 20 * time.Millisecond
	return c
}

func idemReq(method, path, key, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	return r
}

func scopeKeys(method, path, key string) (resultKey, lockKey string) {
	s := idemHash("", method, path, key)
	return "idem:res:" + s, "idem:lock:" + s
}

func TestIdempotency_RunsThenReplays(t *testing.T) {
	var calls int
	h := idempotency(newFakeStore(), testCfg(), idemNop)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"charge":%d}`, calls)
	}))

	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, idemReq(http.MethodPost, "/x", "k1", `{"a":1}`))
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, idemReq(http.MethodPost, "/x", "k1", `{"a":1}`))

	if calls != 1 {
		t.Fatalf("expected backend hit once, got %d", calls)
	}
	if rr1.Code != http.StatusCreated || rr2.Code != http.StatusCreated {
		t.Fatalf("expected 201 twice, got %d and %d", rr1.Code, rr2.Code)
	}
	if rr1.Body.String() != rr2.Body.String() {
		t.Fatalf("replay differs: %q vs %q", rr1.Body.String(), rr2.Body.String())
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("expected Idempotency-Replayed header on the replay")
	}
}

func TestIdempotency_DifferentBodySameKey(t *testing.T) {
	h := idempotency(newFakeStore(), testCfg(), idemNop)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	h.ServeHTTP(httptest.NewRecorder(), idemReq(http.MethodPost, "/x", "k1", `{"a":1}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, idemReq(http.MethodPost, "/x", "k1", `{"a":2}`))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for key reuse with different body, got %d", rr.Code)
	}
}

func TestIdempotency_NoKeyPassesThrough(t *testing.T) {
	var calls int
	store := newFakeStore()
	h := idempotency(store, testCfg(), idemNop)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		h.ServeHTTP(httptest.NewRecorder(), idemReq(http.MethodPost, "/x", "", `{}`))
	}
	if calls != 2 {
		t.Fatalf("expected both requests to run, got %d", calls)
	}
	if len(store.results) != 0 {
		t.Fatalf("expected nothing stored without a key, got %d", len(store.results))
	}
}

func TestIdempotency_InFlightTimesOutTo409(t *testing.T) {
	store := newFakeStore()
	_, lockKey := scopeKeys(http.MethodPost, "/x", "k1")
	store.locks[lockKey] = "someone-else" // simulate an in-flight request

	h := idempotency(store, testCfg(), idemNop)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run while another request holds the lock")
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, idemReq(http.MethodPost, "/x", "k1", `{}`))

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After on 409")
	}
}

func TestIdempotency_InFlightResolvesByReplay(t *testing.T) {
	store := newFakeStore()
	resultKey, lockKey := scopeKeys(http.MethodPost, "/x", "k1")
	store.locks[lockKey] = "someone-else"

	// The in-flight request completes mid-wait and publishes its result.
	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = store.Save(context.Background(), resultKey, idemResult{
			Fingerprint: idemHash(`{}`), Status: http.StatusCreated, Body: []byte(`{"ok":true}`),
		}, time.Hour)
	}()

	h := idempotency(store, testCfg(), idemNop)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run; the in-flight request owns it")
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, idemReq(http.MethodPost, "/x", "k1", `{}`))

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected replayed 201, got %d", rr.Code)
	}
	if rr.Body.String() != `{"ok":true}` {
		t.Fatalf("expected replayed body, got %q", rr.Body.String())
	}
}

func TestIdempotency_LargeResponseNotStored(t *testing.T) {
	var calls int
	cfg := testCfg()
	cfg.maxBody = 16
	h := idempotency(newFakeStore(), cfg, idemNop)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(strings.Repeat("a", 100))) // exceeds the cap
	}))

	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, idemReq(http.MethodPost, "/x", "k1", `{}`))
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, idemReq(http.MethodPost, "/x", "k1", `{}`))

	if calls != 2 {
		t.Fatalf("oversized response should not be cached; expected 2 runs, got %d", calls)
	}
	if len(rr1.Body.String()) != 100 || len(rr2.Body.String()) != 100 {
		t.Fatalf("client must still get the full body: %d, %d", rr1.Body.Len(), rr2.Body.Len())
	}
}

func TestIdempotency_PreservesHeadersOnReplay(t *testing.T) {
	h := idempotency(newFakeStore(), testCfg(), idemNop)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Location", "/things/42")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("created"))
	}))

	h.ServeHTTP(httptest.NewRecorder(), idemReq(http.MethodPost, "/x", "k1", `{}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, idemReq(http.MethodPost, "/x", "k1", `{}`))

	if ct := rr.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("replay lost Content-Type, got %q", ct)
	}
	if loc := rr.Header().Get("Location"); loc != "/things/42" {
		t.Fatalf("replay lost Location, got %q", loc)
	}
}

func TestIdempotency_FailsOpenWhenStoreDown(t *testing.T) {
	var calls int
	store := newFakeStore()
	store.failAcquire = true
	h := idempotency(store, testCfg(), idemNop)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, idemReq(http.MethodPost, "/x", "k1", `{}`))
	if calls != 1 || rr.Code != http.StatusOK {
		t.Fatalf("expected pass-through when the store is down: calls=%d code=%d", calls, rr.Code)
	}
}
