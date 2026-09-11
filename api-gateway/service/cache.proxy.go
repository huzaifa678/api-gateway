package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// cacheProxy caches GET responses in Redis. Keyed per-caller so users
// never read each other's data. Only 2xx responses are stored
type cacheProxy struct {
	next ForwardService
	rdb  *redis.Client
	ttl  time.Duration
}

type cached struct {
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}

// NewCachingProxy wraps next with a Redis response cache
func NewCachingProxy(next ForwardService, rdb *redis.Client, ttl time.Duration) ForwardService {
	if rdb == nil || ttl <= 0 {
		return next
	}
	return &cacheProxy{next: next, rdb: rdb, ttl: ttl}
}

func (p *cacheProxy) Forward(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error) {
	if method != http.MethodGet {
		return p.next.Forward(ctx, body, headers, path, method)
	}

	key := p.key(path, headers.Get("Authorization"))

	// When cache hit, serve from Redis, skip breaker + upstream
	if raw, err := p.rdb.Get(ctx, key).Bytes(); err == nil {
		var c cached
		if json.Unmarshal(raw, &c) == nil {
			return c.Body, c.Status, nil
		}
	}

	respBody, status, err := p.next.Forward(ctx, body, headers, path, method)

	// Only cache the successful HTTP responses
	if err == nil && status >= 200 && status < 300 {
		if raw, mErr := json.Marshal(cached{Status: status, Body: respBody}); mErr == nil {
			p.rdb.Set(ctx, key, raw, p.ttl)
		}
	}

	return respBody, status, err
}

// key namespaces by path and a hash of the auth token
func (p *cacheProxy) key(path, auth string) string {
	sum := sha256.Sum256([]byte(auth))
	return "cache:" + path + ":" + hex.EncodeToString(sum[:8])
}
