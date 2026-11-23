package middleware

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// idemResult is the stored response for a completed idempotent request.
type idemResult struct {
	Fingerprint string      `json:"fp"`
	Status      int         `json:"status"`
	Header      http.Header `json:"header,omitempty"`
	Body        []byte      `json:"body,omitempty"`
}

// idemStore is the state machine idempotency needs, kept behind an interface
// for testability at the unit level.
type idemStore interface {
	// Result returns the stored response for key, or ok=false if none exists.
	Result(ctx context.Context, key string) (res idemResult, ok bool, err error)
	// Acquire sets the lock to token iff absent; ok reports whether we accquired the lock.
	Acquire(ctx context.Context, key, token string, ttl time.Duration) (ok bool, err error)
	// Save stores a completed response.
	Save(ctx context.Context, key string, res idemResult, ttl time.Duration) error
	// Release drops the lock, but only if we still hold token.
	Release(ctx context.Context, key, token string) error
}

type idemConfig struct {
	resultTTL    time.Duration
	lockTTL      time.Duration
	waitBudget   time.Duration // how long a duplicate waits for the first result
	pollInterval time.Duration
	storeTimeout time.Duration
	maxBody      int // responses larger than this aren't stored
}

func defaultIdemConfig(resultTTL, lockTTL time.Duration) idemConfig {
	return idemConfig{
		resultTTL:    resultTTL,
		lockTTL:      lockTTL,
		waitBudget:   2 * time.Second,
		pollInterval: 50 * time.Millisecond,
		storeTimeout: 2 * time.Second,
		maxBody:      256 * 1024,
	}
}

// Idempotency dedupes retried unsafe requests. It engages only when the client
// sends an Idempotency-Key header and the method isn't GET. The first request
// runs; a retry with the same key replays the stored response without hitting
// the backend. Keyed per caller so one tenant's key can't collide with another's.
func Idempotency(rdb *redis.Client, resultTTL, lockTTL time.Duration, logger *slog.Logger) func(http.Handler) http.Handler {
	return idempotency(&redisIdemStore{rdb: rdb}, defaultIdemConfig(resultTTL, lockTTL), logger)
}

func idempotency(store idemStore, cfg idemConfig, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key == "" || r.Method == http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				idemError(w, http.StatusBadRequest, "could not read request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			scope := idemHash(r.Header.Get("Authorization"), r.Method, r.URL.Path, key)
			resultKey := "idem:res:" + scope
			lockKey := "idem:lock:" + scope
			fp := idemHash(string(body))
			ctx := r.Context()

			// Already completed? Replay it.
			if replayIfPresent(ctx, store, resultKey, fp, w, logger) {
				return
			}

			// Acquire the per-key lock so only one request runs at a time.
			token, err := idemToken()
			if err != nil {
				logger.WarnContext(ctx, "idempotency token failed, passing through", "err", err)
				next.ServeHTTP(w, r)
				return
			}
			acquired, err := store.Acquire(ctx, lockKey, token, cfg.lockTTL)
			if err != nil {
				// Redis down: fail open rather than block the request path.
				logger.WarnContext(ctx, "idempotency lock failed, passing through", "err", err)
				next.ServeHTTP(w, r)
				return
			}

			if !acquired {
				// Someone else is in-flight: wait briefly for their result, else 409.
				if waitForResult(ctx, store, resultKey, fp, cfg, w, logger) {
					return
				}
				w.Header().Set("Retry-After", "1")
				idemError(w, http.StatusConflict, "a request with this Idempotency-Key is still in progress")
				return
			}
			defer func() {
				relCtx, cancel := context.WithTimeout(context.Background(), cfg.storeTimeout)
				defer cancel()
				_ = store.Release(relCtx, lockKey, token)
			}()

			// Won the lock, but a result may have landed between our check and the lock.
			if replayIfPresent(ctx, store, resultKey, fp, w, logger) {
				return
			}

			// Run the handler, streaming the response to the client while capturing
			// it (up to the size cap) for storage.
			rec := &captureWriter{ResponseWriter: w, limit: cfg.maxBody}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}

			// Store 1xx–4xx so clients can replay; let 5xx be retried, and skip
			// responses too large to cache.
			if rec.status < 500 && !rec.tooBig {
				res := idemResult{Fingerprint: fp, Status: rec.status, Header: pruneHeaders(rec.header), Body: rec.buf.Bytes()}
				storeCtx, cancel := context.WithTimeout(context.Background(), cfg.storeTimeout)
				defer cancel()
				if err := store.Save(storeCtx, resultKey, res, cfg.resultTTL); err != nil {
					logger.WarnContext(ctx, "idempotency result store failed", "err", err)
				}
			}
		})
	}
}

// replayIfPresent writes the stored response if one exists and reports whether
// it handled the request. A store error is treated as a miss (fail open).
func replayIfPresent(ctx context.Context, store idemStore, resultKey, fp string, w http.ResponseWriter, logger *slog.Logger) bool {
	res, ok, err := store.Result(ctx, resultKey)
	if err != nil {
		logger.WarnContext(ctx, "idempotency result lookup failed", "err", err)
		return false
	}
	if !ok {
		return false
	}
	if res.Fingerprint != fp {
		idemError(w, http.StatusUnprocessableEntity, "Idempotency-Key reused with a different request body")
		return true
	}
	writeStored(w, res)
	return true
}

// waitForResult polls for the in-flight request's result. 
// It returns true when it has handled the response false 
// when the wait budget elapsed with no result.
func waitForResult(ctx context.Context, store idemStore, resultKey, fp string, cfg idemConfig, w http.ResponseWriter, logger *slog.Logger) bool {
	deadline := time.NewTimer(cfg.waitBudget)
	defer deadline.Stop()
	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return true // client is gone; nothing to write
		case <-deadline.C:
			return false // give up -> caller returns 409
		case <-ticker.C:
			if replayIfPresent(ctx, store, resultKey, fp, w, logger) {
				return true
			}
		}
	}
}

func writeStored(w http.ResponseWriter, res idemResult) {
	for k, vals := range res.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func idemError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

func idemHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func idemToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hopByHop headers are connection-specific and must not be stored or replayed.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

func pruneHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, v := range h {
		if !hopByHop[http.CanonicalHeaderKey(k)] {
			out[k] = v
		}
	}
	return out
}
