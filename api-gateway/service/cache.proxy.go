package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

const (
	cacheLockTTL   = 10 * time.Second // self-expiring: a dead holder can't block refreshes
	cacheRefreshTO = 15 * time.Second // budget for a background revalidation
	cachePollEvery = 100 * time.Millisecond
	cachePollTries = 20 // ~2s waiting for the lock holder to populate
)

// cacheReleaseLock drops the lock only if we still hold the token.
var cacheReleaseLock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`)

// cacheProxy caches GET responses in Redis, keyed per caller so users never read
// each other's data.
type cacheProxy struct {
	next        ForwardService
	rdb         *redis.Client
	softTTL     time.Duration // logical freshness; after this, serve stale + refresh
	staleWindow time.Duration // how long a stale entry stays servable
	sg          singleflight.Group
	refreshing  sync.Map // dataKey -> in-flight background refresh (one per key per instance)
	logger      *slog.Logger
}

// cached is the stored entry. SoftExpires marks when the body becomes stale.
type cached struct {
	Status      int    `json:"status"`
	Body        []byte `json:"body"`
	SoftExpires int64  `json:"soft"`
}

// NewCachingProxy wraps next with a Redis response cache. Returns next unchanged
// when caching is off (nil client or ttl <= 0).
func NewCachingProxy(next ForwardService, rdb *redis.Client, ttl time.Duration, logger *slog.Logger) ForwardService {
	if rdb == nil || ttl <= 0 {
		return next
	}
	return &cacheProxy{
		next:        next,
		rdb:         rdb,
		softTTL:     ttl,
		staleWindow: ttl, // serve stale for up to one more TTL while revalidating
		logger:      logger,
	}
}

func (p *cacheProxy) Forward(ctx context.Context, body []byte, headers http.Header, path, method string) ([]byte, int, error) {
	if method != http.MethodGet {
		return p.next.Forward(ctx, body, headers, path, method)
	}

	dataKey, lockKey := p.keys(path, headers.Get("Authorization"))

	if raw, err := p.rdb.Get(ctx, dataKey).Bytes(); err == nil {
		var c cached
		if json.Unmarshal(raw, &c) == nil {
			if time.Now().Unix() < c.SoftExpires {
				return c.Body, c.Status, nil // fresh
			}
			// Stale but servable: hand it back now, revalidate behind the scenes.
			p.revalidate(dataKey, lockKey, body, headers.Clone(), path, method)
			return c.Body, c.Status, nil
		}
	}

	// Miss: collapse concurrent callers on this instance to one fetch.
	v, err, _ := p.sg.Do(dataKey, func() (interface{}, error) {
		return p.fetchAndStore(ctx, dataKey, lockKey, body, headers, path, method)
	})
	if err != nil {
		return nil, 0, err
	}
	c := v.(cached)
	return c.Body, c.Status, nil
}

// fetchAndStore does the upstream call under a cross-instance Redis lock. The
// lock winner fetches and caches; losers wait briefly for its result, then fall
// back to fetching directly so no one blocks indefinitely.
func (p *cacheProxy) fetchAndStore(ctx context.Context, dataKey, lockKey string, body []byte, headers http.Header, path, method string) (cached, error) {
	token := randToken()
	got, err := p.rdb.SetNX(ctx, lockKey, token, cacheLockTTL).Result()
	if err == nil && got {
		defer func() { _ = cacheReleaseLock.Run(context.Background(), p.rdb, []string{lockKey}, token).Err() }()
		return p.callAndCache(ctx, dataKey, body, headers, path, method)
	}

	// Another instance holds the lock: wait for it to populate the cache.
	deadline := time.NewTimer(cachePollEvery * cachePollTries)
	defer deadline.Stop()
	ticker := time.NewTicker(cachePollEvery)
	defer ticker.Stop()

waitLoop:
	for {
		select {
		case <-ctx.Done():
			return cached{}, ctx.Err()
		case <-deadline.C:
			break waitLoop
		case <-ticker.C:
			if raw, gErr := p.rdb.Get(ctx, dataKey).Bytes(); gErr == nil {
				var c cached
				if json.Unmarshal(raw, &c) == nil {
					return c, nil
				}
			}
		}
	}

	// Gave up waiting: fetch directly (don't cache; the lock holder owns that).
	respBody, status, fErr := p.next.Forward(ctx, body, headers, path, method)
	return cached{Status: status, Body: respBody}, fErr
}

// revalidate refreshes a stale entry in the background: at most one goroutine per
// key per instance (the refreshing map) and one per fleet (the Redis lock), on a
// detached context so it outlives the request that triggered it.
func (p *cacheProxy) revalidate(dataKey, lockKey string, body []byte, headers http.Header, path, method string) {
	if _, busy := p.refreshing.LoadOrStore(dataKey, struct{}{}); busy {
		return // a refresh for this key is already running here
	}
	go func() {
		defer p.refreshing.Delete(dataKey)

		ctx, cancel := context.WithTimeout(context.Background(), cacheRefreshTO)
		defer cancel()

		token := randToken()
		got, err := p.rdb.SetNX(ctx, lockKey, token, cacheLockTTL).Result()
		if err != nil || !got {
			return // another worker is already refreshing across the fleet
		}
		defer func() { _ = cacheReleaseLock.Run(context.Background(), p.rdb, []string{lockKey}, token).Err() }()

		if _, cErr := p.callAndCache(ctx, dataKey, body, headers, path, method); cErr != nil {
			p.logger.WarnContext(ctx, "cache revalidation failed", "key", dataKey, "err", cErr)
		}
	}()
}

// callAndCache forwards upstream and stores a 2xx response with jittered TTLs.
func (p *cacheProxy) callAndCache(ctx context.Context, dataKey string, body []byte, headers http.Header, path, method string) (cached, error) {
	respBody, status, err := p.next.Forward(ctx, body, headers, path, method)
	if err != nil || status < 200 || status >= 300 {
		return cached{Status: status, Body: respBody}, err
	}

	soft := jitter(p.softTTL)
	entry := cached{Status: status, Body: respBody, SoftExpires: time.Now().Add(soft).Unix()}
	if raw, mErr := json.Marshal(entry); mErr == nil {
		// Hard TTL outlives soft TTL so the entry stays servable while stale.
		_ = p.rdb.Set(ctx, dataKey, raw, soft+p.staleWindow).Err()
	}
	return entry, nil
}

// keys namespaces by path and a hash of the auth token (per-caller isolation).
func (p *cacheProxy) keys(path, auth string) (dataKey, lockKey string) {
	sum := sha256.Sum256([]byte(auth))
	base := path + ":" + hex.EncodeToString(sum[:8])
	return "cache:" + base, "cachelock:" + base
}

// jitter spreads expiry by ±10% so many keys don't expire in lockstep.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.9 + 0.2*mrand.Float64()))
}

func randToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
